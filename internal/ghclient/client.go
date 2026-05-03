package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

const triagedLabel = "ezoss/triaged"

var retryAfterPattern = regexp.MustCompile(`(?i)retry after\s+([^.,;\n]+)`)
var retryAfterWordPartPattern = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hrs?|hr|minutes?|mins?|min|seconds?|secs?|sec)`)

type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type Client struct {
	runner Runner
}

type RateLimitError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "github rate limit exceeded"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("github rate limit exceeded (retry after %s): %s", e.RetryAfter, e.Message)
	}
	return fmt.Sprintf("github rate limit exceeded: %s", e.Message)
}

type Item struct {
	Repo      string
	Kind      sharedtypes.ItemKind
	Number    int
	Title     string
	Body      string
	Author    string
	State     sharedtypes.ItemState
	IsDraft   bool
	Labels    []string
	URL       string
	UpdatedAt time.Time

	// HeadRepo / HeadRef / HeadCloneURL describe the PR's source branch.
	// Populated only by SearchAuthoredOpenPRs (and similar contributor
	// queries) since they only matter when ezoss is acting as a
	// contributor and needs to push to the existing PR branch on a fork.
	HeadRepo     string
	HeadRef      string
	HeadCloneURL string
}

type commandRunner struct{}

type listItem struct {
	Number         int           `json:"number"`
	Title          string        `json:"title"`
	Body           string        `json:"body"`
	Author         *ghAuthor     `json:"author"`
	State          string        `json:"state"`
	IsDraft        bool          `json:"isDraft"`
	Labels         []ghLabel     `json:"labels"`
	URL            string        `json:"url"`
	UpdatedAt      string        `json:"updatedAt"`
	Repository     *ghRepository `json:"repository"`
	HeadRepository *ghRepository `json:"headRepository"`
	HeadRefName    string        `json:"headRefName"`
	HeadOwner      *ghLogin      `json:"headRepositoryOwner"`
}

type ghRepository struct {
	Name          string   `json:"name"`
	NameWithOwner string   `json:"nameWithOwner"`
	URL           string   `json:"url"`
	Owner         *ghLogin `json:"owner"`
}

type ghLogin struct {
	Login string `json:"login"`
	ID    string `json:"id"`
}

type ghAuthor struct {
	Login string `json:"login"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type timelineItem struct {
	Event       string                  `json:"event"`
	CreatedAt   string                  `json:"created_at"`
	SubmittedAt string                  `json:"submitted_at"`
	CommittedAt string                  `json:"committed_at"`
	Committer   *timelineCommitIdentity `json:"committer"`
	Author      *timelineCommitIdentity `json:"author"`
	Actor       *ghLogin                `json:"actor"`
	User        *ghLogin                `json:"user"`
	Label       *timelineLabel          `json:"label"`
}

type timelineCommitIdentity struct {
	Date string `json:"date"`
}

type timelineLabel struct {
	Name string `json:"name"`
}

func New(runner Runner) *Client {
	if runner == nil {
		runner = commandRunner{}
	}
	return &Client{runner: runner}
}

func (c *Client) ListNeedingTriage(ctx context.Context, repo string) ([]Item, error) {
	return c.listFilteredItems(ctx, repo, "-label:"+triagedLabel, "open")
}

// ListTriaged returns items with the triaged label. When sinceUpdated is
// non-zero the search is bounded by `updated:>=<sinceUpdated>` and pulls
// items in any state (including closed) so callers can reconcile local state
// with GitHub. When sinceUpdated is zero the call is restricted to open
// items - this avoids dragging in unbounded historical closed items on the
// first refresh after a daemon restart.
func (c *Client) ListTriaged(ctx context.Context, repo string, sinceUpdated time.Time) ([]Item, error) {
	search := "label:" + triagedLabel
	state := "open"
	if !sinceUpdated.IsZero() {
		search += " updated:>=" + sinceUpdated.UTC().Format(time.RFC3339)
		state = "all"
	}
	return c.listFilteredItems(ctx, repo, search, state)
}

func (c *Client) listFilteredItems(ctx context.Context, repo string, search string, state string) ([]Item, error) {
	issues, err := c.listItems(ctx, repo, sharedtypes.ItemKindIssue, search, state)
	if err != nil {
		return nil, err
	}
	prs, err := c.listItems(ctx, repo, sharedtypes.ItemKindPR, search, state)
	if err != nil {
		return nil, err
	}
	items := append(issues, prs...)
	filtered := items[:0]
	for _, item := range items {
		if item.IsDraft || isWIPTitle(item.Title) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

func (c *Client) GetItem(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int) (Item, error) {
	resource, err := kindResource(kind)
	if err != nil {
		return Item{}, err
	}

	stdout, err := c.runner.Run(ctx,
		resource, "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", jsonFieldsFor(kind),
	)
	if err != nil {
		return Item{}, fmt.Errorf("gh %s view %s#%d: %w", resource, repo, number, classifyError(err))
	}

	var raw listItem
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return Item{}, fmt.Errorf("decode gh %s view %s#%d: %w", resource, repo, number, err)
	}

	item, err := toItem(repo, kind, raw)
	if err != nil {
		return Item{}, fmt.Errorf("parse gh %s view %s#%d: %w", resource, repo, number, err)
	}
	return item, nil
}

func (c *Client) EditLabels(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, add []string, remove []string) error {
	resource, err := kindResource(kind)
	if err != nil {
		return err
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}

	if len(add) > 0 {
		if err := c.runAddLabels(ctx, resource, repo, number, add); err != nil {
			return err
		}
	}

	// Removes go one label per call so that a single label that's not on
	// the item (or doesn't exist in the repo at all) doesn't take down the
	// whole batch. The desired end state - label absent - is already true
	// in that case, so we treat the gh-CLI "'<label>' not found" error as
	// success and continue with the remaining removes.
	for _, label := range remove {
		args := []string{resource, "edit", strconv.Itoa(number), "--repo", repo, "--remove-label", label}
		if _, err := c.runner.Run(ctx, args...); err != nil {
			if isLabelNotFoundError(err, label) {
				continue
			}
			return fmt.Errorf("gh %s edit %s#%d: %w", resource, repo, number, classifyError(err))
		}
	}
	return nil
}

// runAddLabels issues a bulk --add-label call. If gh reports that a label
// in the ezoss/* namespace doesn't exist in the repo, the client creates
// it on the fly and retries for maintainer/configured-repo label writes -
// ezoss owns that namespace per the design intent ("ezoss/triaged is always
// managed automatically" for maintainer items). Labels outside
// the namespace are not auto-created; missing user labels surface as
// errors so the user can decide how to handle them.
func (c *Client) runAddLabels(ctx context.Context, resource, repo string, number int, add []string) error {
	args := []string{resource, "edit", strconv.Itoa(number), "--repo", repo, "--add-label", strings.Join(add, ",")}
	// Cap at len(add) retries so we can never loop more than once per
	// distinct label even if gh keeps reporting a different missing one.
	for attempt := 0; attempt <= len(add); attempt++ {
		_, err := c.runner.Run(ctx, args...)
		if err == nil {
			return nil
		}
		label, ok := extractLabelNotFoundName(err)
		if !ok || !isManagedLabel(label) {
			return fmt.Errorf("gh %s edit %s#%d: %w", resource, repo, number, classifyError(err))
		}
		if createErr := c.createManagedLabel(ctx, repo, label); createErr != nil {
			return fmt.Errorf("gh %s edit %s#%d: create missing label %q: %w", resource, repo, number, label, classifyError(createErr))
		}
	}
	return fmt.Errorf("gh %s edit %s#%d: gave up after auto-creating labels", resource, repo, number)
}

func (c *Client) createManagedLabel(ctx context.Context, repo, label string) error {
	args := []string{"label", "create", label, "--repo", repo, "--description", "Managed by ezoss"}
	if _, err := c.runner.Run(ctx, args...); err != nil {
		// A concurrent ezoss run (or a manual create) may have raced us
		// to create the label. That's fine - the label exists, which is
		// what we wanted.
		if isLabelAlreadyExistsError(err) {
			return nil
		}
		return err
	}
	return nil
}

func isManagedLabel(label string) bool {
	return strings.HasPrefix(label, "ezoss/")
}

func isLabelNotFoundError(err error, label string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "'"+label+"' not found")
}

func isLabelAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// extractLabelNotFoundName parses the gh-CLI error format `'<label>' not
// found` and returns the quoted label. Returns ok=false if the error
// doesn't match that shape so the caller surfaces it unchanged.
func extractLabelNotFoundName(err error) (string, bool) {
	msg := err.Error()
	start := strings.Index(msg, "'")
	if start < 0 {
		return "", false
	}
	rest := msg[start+1:]
	end := strings.Index(rest, "'")
	if end < 0 {
		return "", false
	}
	if !strings.Contains(rest[end+1:], "not found") {
		return "", false
	}
	return rest[:end], true
}

func (c *Client) Comment(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, body string) error {
	resource, err := kindResource(kind)
	if err != nil {
		return err
	}
	if _, err := c.runner.Run(ctx, resource, "comment", strconv.Itoa(number), "--repo", repo, "--body", body); err != nil {
		return fmt.Errorf("gh %s comment %s#%d: %w", resource, repo, number, classifyError(err))
	}
	return nil
}

func (c *Client) Close(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int, comment string) error {
	resource, err := kindResource(kind)
	if err != nil {
		return err
	}
	args := []string{resource, "close", strconv.Itoa(number), "--repo", repo}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	if _, err := c.runner.Run(ctx, args...); err != nil {
		return fmt.Errorf("gh %s close %s#%d: %w", resource, repo, number, classifyError(err))
	}
	return nil
}

func (c *Client) RequestChanges(ctx context.Context, repo string, number int, body string) error {
	if _, err := c.runner.Run(ctx, "pr", "review", strconv.Itoa(number), "--repo", repo, "--request-changes", "--body", body); err != nil {
		return fmt.Errorf("gh pr review %s#%d: %w", repo, number, classifyError(err))
	}
	return nil
}

// RepoMergeOptions reports which merge methods the repo's GitHub settings
// allow. The three flags map to the per-repo "Allow merge commits / Allow
// squash merging / Allow rebase merging" toggles.
type RepoMergeOptions struct {
	Merge  bool
	Squash bool
	Rebase bool
}

// RepoMergeOptions queries `gh api repos/{repo}` and extracts the three
// merge-method toggles. Useful for picking a merge method that the repo
// will actually accept before invoking `gh pr merge`.
func (c *Client) RepoMergeOptions(ctx context.Context, repo string) (RepoMergeOptions, error) {
	stdout, err := c.runner.Run(ctx, "api", "repos/"+repo)
	if err != nil {
		return RepoMergeOptions{}, fmt.Errorf("gh api repos/%s: %w", repo, classifyError(err))
	}
	var raw struct {
		AllowMergeCommit bool `json:"allow_merge_commit"`
		AllowSquashMerge bool `json:"allow_squash_merge"`
		AllowRebaseMerge bool `json:"allow_rebase_merge"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return RepoMergeOptions{}, fmt.Errorf("decode repos/%s merge options: %w", repo, err)
	}
	return RepoMergeOptions{
		Merge:  raw.AllowMergeCommit,
		Squash: raw.AllowSquashMerge,
		Rebase: raw.AllowRebaseMerge,
	}, nil
}

// Merge merges the PR using the requested method when the repo allows it,
// otherwise falls back through squash > rebase > merge (the order most
// users prefer when their first choice is unavailable). Returns the
// method that was actually used so the caller can surface a notice when
// the chosen method differs from the requested one.
func (c *Client) Merge(ctx context.Context, repo string, number int, requested string) (string, error) {
	options, err := c.RepoMergeOptions(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("read repo merge options for %s: %w", repo, err)
	}
	chosen := pickMergeMethod(requested, options)
	if chosen == "" {
		return "", fmt.Errorf("gh pr merge %s#%d: repo allows no merge methods (check repo settings -> Pull Requests -> Allow merge commits/squash/rebase)", repo, number)
	}
	flag := mergeMethodFlag(chosen)
	if _, err := c.runner.Run(ctx, "pr", "merge", strconv.Itoa(number), "--repo", repo, flag); err != nil {
		return chosen, fmt.Errorf("gh pr merge %s#%d: %w", repo, number, classifyError(err))
	}
	return chosen, nil
}

// pickMergeMethod returns the requested method if the repo allows it.
// Otherwise it falls back through squash > rebase > merge - the order
// reflects what most modern repos prefer when "merge" is disabled. Returns
// "" if no method is allowed.
func pickMergeMethod(requested string, options RepoMergeOptions) string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "squash":
		if options.Squash {
			return "squash"
		}
	case "rebase":
		if options.Rebase {
			return "rebase"
		}
	case "merge", "":
		if options.Merge {
			return "merge"
		}
	}
	if options.Squash {
		return "squash"
	}
	if options.Rebase {
		return "rebase"
	}
	if options.Merge {
		return "merge"
	}
	return ""
}

type RepoVisibility string

const (
	RepoVisibilityAll    RepoVisibility = ""
	RepoVisibilityPublic RepoVisibility = "public"
)

const authoredSearchLimit = 1000

// SearchAuthoredOpenPRs returns open PRs where the authenticated user is
// the author across every repo on GitHub, including repos ezoss does
// not maintain. Used to drive the contributor sweep. The returned Items
// have Repo set to "owner/name" (the upstream repo the PR targets) and
// HeadRepo / HeadRef / HeadCloneURL populated from the PR's head branch
// so the contributor fix runner can push to the right place. Drafts and
// WIP-titled PRs are filtered out, mirroring ListNeedingTriage.
func (c *Client) SearchAuthoredOpenPRs(ctx context.Context) ([]Item, error) {
	stdout, err := c.runner.Run(ctx,
		"search", "prs",
		"--author", "@me",
		"--state", "open",
		"--limit", strconv.Itoa(authoredSearchLimit),
		"--json", "number,title,body,author,state,isDraft,labels,updatedAt,url,repository",
	)
	if err != nil {
		return nil, fmt.Errorf("gh search prs --author=@me: %w", classifyError(err))
	}
	var raw []listItem
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("decode gh search prs: %w", err)
	}
	if len(raw) >= authoredSearchLimit {
		return nil, fmt.Errorf("gh search prs --author=@me: truncated at %d results", authoredSearchLimit)
	}
	items := make([]Item, 0, len(raw))
	for _, entry := range raw {
		repo := repoFromEntry(entry)
		if repo == "" {
			continue
		}
		item, err := toItem(repo, sharedtypes.ItemKindPR, entry)
		if err != nil {
			return nil, fmt.Errorf("parse search prs entry #%d: %w", entry.Number, err)
		}
		if item.IsDraft || isWIPTitle(item.Title) {
			continue
		}
		_ = c.populatePRHeadRefs(ctx, &item)
		items = append(items, item)
	}
	return items, nil
}

func (c *Client) populatePRHeadRefs(ctx context.Context, item *Item) error {
	stdout, err := c.runner.Run(ctx,
		"pr", "view", strconv.Itoa(item.Number),
		"--repo", item.Repo,
		"--json", "headRepository,headRepositoryOwner,headRefName",
	)
	if err != nil {
		return fmt.Errorf("gh pr view: %w", classifyError(err))
	}
	var entry listItem
	if err := json.Unmarshal(stdout, &entry); err != nil {
		return fmt.Errorf("decode gh pr view: %w", err)
	}
	populateHeadRefs(item, entry)
	return nil
}

// SearchAuthoredOpenIssues returns open issues authored by the
// authenticated user across every repo on GitHub. Like
// SearchAuthoredOpenPRs, it powers the contributor sweep so issues filed
// in repos ezoss does not maintain still surface in the inbox.
func (c *Client) SearchAuthoredOpenIssues(ctx context.Context) ([]Item, error) {
	stdout, err := c.runner.Run(ctx,
		"search", "issues",
		"--author", "@me",
		"--state", "open",
		"--limit", strconv.Itoa(authoredSearchLimit),
		"--json", "number,title,body,author,state,labels,updatedAt,url,repository",
	)
	if err != nil {
		return nil, fmt.Errorf("gh search issues --author=@me: %w", classifyError(err))
	}
	var raw []listItem
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("decode gh search issues: %w", err)
	}
	if len(raw) >= authoredSearchLimit {
		return nil, fmt.Errorf("gh search issues --author=@me: truncated at %d results", authoredSearchLimit)
	}
	items := make([]Item, 0, len(raw))
	for _, entry := range raw {
		repo := repoFromEntry(entry)
		if repo == "" {
			continue
		}
		item, err := toItem(repo, sharedtypes.ItemKindIssue, entry)
		if err != nil {
			return nil, fmt.Errorf("parse search issues entry #%d: %w", entry.Number, err)
		}
		if isWIPTitle(item.Title) {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (c *Client) HasActivityAfterLabel(ctx context.Context, repo string, number int, label string) (bool, error) {
	return c.hasActivityAfterLabelSince(ctx, repo, number, label, time.Time{}, time.Time{})
}

func (c *Client) HasActivityAfterLabelSince(ctx context.Context, repo string, number int, label string, since time.Time) (bool, error) {
	return c.hasActivityAfterLabelSince(ctx, repo, number, label, since, time.Time{})
}

func (c *Client) HasActivityAfterLabelSinceUpdated(ctx context.Context, repo string, number int, label string, since time.Time, updatedAt time.Time) (bool, error) {
	return c.hasActivityAfterLabelSince(ctx, repo, number, label, since, updatedAt)
}

func (c *Client) hasActivityAfterLabelSince(ctx context.Context, repo string, number int, label string, since time.Time, updatedAt time.Time) (bool, error) {
	stdout, err := c.runner.Run(ctx, "api", "repos/"+repo+"/issues/"+strconv.Itoa(number)+"/timeline", "--paginate")
	if err != nil {
		return false, fmt.Errorf("gh api repos/%s/issues/%d/timeline: %w", repo, number, classifyError(err))
	}
	events, err := decodeTimelineItems(stdout)
	if err != nil {
		return false, fmt.Errorf("decode issue timeline %s#%d: %w", repo, number, err)
	}

	var labeledAt time.Time
	var labeledBy string
	labeledIndex := -1
	for i, event := range events {
		if event.Event != "labeled" || event.Label == nil || event.Label.Name != label {
			continue
		}
		createdAt, err := timelineEventTime(event)
		if err != nil {
			return false, fmt.Errorf("parse labeled event time %s#%d: %w", repo, number, err)
		}
		if createdAt.After(labeledAt) {
			labeledAt = createdAt
			labeledBy = timelineActorLogin(event)
			labeledIndex = i
		}
	}
	if labeledAt.IsZero() {
		return false, nil
	}

	activityAfter := labeledAt
	if since.UTC().After(activityAfter) {
		activityAfter = since.UTC()
	}
	for i, event := range events {
		if !isPostLabelActivityEvent(event.Event) {
			continue
		}
		createdAt, err := timelineEventTime(event)
		if err != nil {
			if !isCommitAfterLabelByTimelineOrder(events, event, i, labeledIndex, since, labeledAt, labeledBy, updatedAt) {
				return false, fmt.Errorf("parse timeline event time %s#%d: %w", repo, number, err)
			}
		}
		occurredAfter := !createdAt.IsZero() && createdAt.After(activityAfter)
		if !occurredAfter && isCommitAfterLabelByTimelineOrder(events, event, i, labeledIndex, since, labeledAt, labeledBy, updatedAt) {
			occurredAfter = true
		}
		if !occurredAfter {
			continue
		}
		actor := timelineActorLogin(event)
		if actor != "" && actor == labeledBy {
			continue
		}
		return true, nil
	}
	return false, nil
}

func decodeTimelineItems(stdout []byte) ([]timelineItem, error) {
	decoder := json.NewDecoder(strings.NewReader(string(stdout)))
	events := make([]timelineItem, 0)
	for {
		var page []timelineItem
		if err := decoder.Decode(&page); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		events = append(events, page...)
	}
	return events, nil
}

func timelineEventTime(event timelineItem) (time.Time, error) {
	value := event.CreatedAt
	switch event.Event {
	case "committed":
		if strings.TrimSpace(value) == "" {
			if strings.TrimSpace(event.CommittedAt) != "" {
				value = event.CommittedAt
			} else if event.Committer != nil && strings.TrimSpace(event.Committer.Date) != "" {
				value = event.Committer.Date
			} else if event.Author != nil && strings.TrimSpace(event.Author.Date) != "" {
				value = event.Author.Date
			}
		}
	case "reviewed":
		if strings.TrimSpace(event.SubmittedAt) != "" {
			value = event.SubmittedAt
		}
	}
	return time.Parse(time.RFC3339, value)
}

const timelineOrderBoundaryTolerance = 5 * time.Minute

func isCommitAfterLabelByTimelineOrder(events []timelineItem, event timelineItem, eventIndex int, labeledIndex int, since time.Time, labeledAt time.Time, labeledBy string, updatedAt time.Time) bool {
	if event.Event != "committed" || labeledIndex < 0 || eventIndex <= labeledIndex {
		return false
	}
	if since.IsZero() || !since.UTC().After(labeledAt.UTC()) {
		return true
	}
	if hasTimelineOrderBoundaryBeforeCommit(events, eventIndex, labeledIndex, since, labeledBy) {
		return true
	}
	return updatedAt.UTC().After(since.UTC()) && !hasDatedTimelineEventAfter(events, labeledIndex, since)
}

func hasDatedTimelineEventAfter(events []timelineItem, labeledIndex int, since time.Time) bool {
	sinceStart := since.UTC().Add(-timelineOrderBoundaryTolerance)
	for i := labeledIndex + 1; i < len(events); i++ {
		occurredAt, err := timelineEventTime(events[i])
		if err != nil {
			continue
		}
		if !occurredAt.UTC().Before(sinceStart) {
			return true
		}
	}
	return false
}

func hasTimelineOrderBoundaryBeforeCommit(events []timelineItem, eventIndex int, labeledIndex int, since time.Time, labeledBy string) bool {
	if labeledBy == "" {
		return false
	}
	sinceStart := since.UTC().Add(-timelineOrderBoundaryTolerance)
	for i := labeledIndex + 1; i < eventIndex && i < len(events); i++ {
		event := events[i]
		if event.Event == "committed" || timelineActorLogin(event) != labeledBy {
			continue
		}
		occurredAt, err := timelineEventTime(event)
		if err != nil {
			continue
		}
		if !occurredAt.UTC().Before(sinceStart) {
			return true
		}
	}
	return false
}

func loginOf(actor *ghLogin) string {
	if actor == nil {
		return ""
	}
	return strings.TrimSpace(actor.Login)
}

func timelineActorLogin(event timelineItem) string {
	if login := loginOf(event.Actor); login != "" {
		return login
	}
	return loginOf(event.User)
}

func isPostLabelActivityEvent(event string) bool {
	switch event {
	case "commented", "committed", "reviewed":
		return true
	default:
		return false
	}
}

func repoFromEntry(entry listItem) string {
	if entry.Repository == nil {
		return ""
	}
	if name := strings.TrimSpace(entry.Repository.NameWithOwner); name != "" {
		return name
	}
	if entry.Repository.Owner != nil && entry.Repository.Name != "" {
		return entry.Repository.Owner.Login + "/" + entry.Repository.Name
	}
	return ""
}

func populateHeadRefs(item *Item, entry listItem) {
	item.HeadRef = strings.TrimSpace(entry.HeadRefName)
	if entry.HeadRepository != nil {
		if name := strings.TrimSpace(entry.HeadRepository.NameWithOwner); name != "" {
			item.HeadRepo = name
		} else if entry.HeadOwner != nil && entry.HeadRepository.Name != "" {
			item.HeadRepo = entry.HeadOwner.Login + "/" + entry.HeadRepository.Name
		}
		if url := strings.TrimSpace(entry.HeadRepository.URL); url != "" {
			item.HeadCloneURL = url + ".git"
		}
	}
	if item.HeadRepo == "" && entry.HeadOwner != nil && strings.TrimSpace(entry.HeadOwner.Login) != "" && strings.TrimSpace(entry.HeadRefName) != "" {
		// Some payloads carry headRepositoryOwner without headRepository
		// (e.g. when the head fork has been deleted). Synthesize an
		// owner-qualified head repo only when we have enough info; the
		// runner will surface a clear error if push fails.
		item.HeadRepo = entry.HeadOwner.Login
	}
}

// ListOwnedRepos returns repos owned by the authenticated user as
// "owner/name" strings. Pass RepoVisibilityPublic to filter to public
// repos only. Forks and archived repos are excluded.
func (c *Client) ListOwnedRepos(ctx context.Context, visibility RepoVisibility) ([]string, error) {
	args := []string{
		"api", "user/repos",
		"--method", "GET",
		"--paginate",
		"-f", "affiliation=owner",
		"-f", "per_page=100",
	}
	if visibility == RepoVisibilityPublic {
		args = append(args, "-f", "visibility=public")
	} else {
		args = append(args, "-f", "visibility=all")
	}
	args = append(args, "--jq", "[.[] | select(.fork == false and .archived == false) | .full_name]")

	stdout, err := c.runner.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("gh api user/repos: %w", classifyError(err))
	}

	repos := make([]string, 0)
	decoder := json.NewDecoder(strings.NewReader(string(stdout)))
	for {
		var page []string
		if err := decoder.Decode(&page); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode gh api user/repos page: %w", err)
		}
		for _, name := range page {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			repos = append(repos, name)
		}
	}
	return repos, nil
}

// ListStarredRepos returns repos the authenticated user has starred as
// "owner/name" strings. Uses the GitHub REST endpoint via `gh api` because
// `gh repo list` does not expose starred repos.
func (c *Client) ListStarredRepos(ctx context.Context) ([]string, error) {
	stdout, err := c.runner.Run(ctx,
		"api", "user/starred",
		"--paginate",
		"--jq", "[.[] | .full_name]",
	)
	if err != nil {
		return nil, fmt.Errorf("gh api user/starred: %w", classifyError(err))
	}

	// `gh api --paginate` concatenates one JSON document per page, separated
	// by newlines. With `--jq '[...]'` each page yields one bracketed array.
	repos := make([]string, 0)
	seen := make(map[string]struct{})
	for _, chunk := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var page []string
		if err := json.Unmarshal([]byte(chunk), &page); err != nil {
			return nil, fmt.Errorf("decode gh api user/starred page: %w", err)
		}
		for _, name := range page {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			repos = append(repos, name)
		}
	}
	return repos, nil
}

func mergeMethodFlag(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "squash":
		return "--squash"
	case "rebase":
		return "--rebase"
	default:
		return "--merge"
	}
}

func (c *Client) listItems(ctx context.Context, repo string, kind sharedtypes.ItemKind, search string, state string) ([]Item, error) {
	resource, err := kindResource(kind)
	if err != nil {
		return nil, err
	}

	stdout, err := c.runner.Run(ctx,
		resource, "list",
		"--repo", repo,
		"--state", state,
		"--limit", "100",
		"--search", search,
		"--json", jsonFieldsFor(kind),
	)
	if err != nil {
		return nil, fmt.Errorf("gh %s list %s: %w", resource, repo, classifyError(err))
	}

	var raw []listItem
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("decode gh %s list %s: %w", resource, repo, err)
	}

	items := make([]Item, 0, len(raw))
	for _, entry := range raw {
		item, err := toItem(repo, kind, entry)
		if err != nil {
			return nil, fmt.Errorf("parse gh %s list %s #%d: %w", resource, repo, entry.Number, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func toItem(repo string, kind sharedtypes.ItemKind, raw listItem) (Item, error) {
	updatedAt, err := time.Parse(time.RFC3339, raw.UpdatedAt)
	if err != nil {
		return Item{}, fmt.Errorf("updatedAt: %w", err)
	}

	state, err := parseState(raw.State)
	if err != nil {
		return Item{}, err
	}

	item := Item{
		Repo:      repo,
		Kind:      kind,
		Number:    raw.Number,
		Title:     raw.Title,
		Body:      raw.Body,
		State:     state,
		IsDraft:   raw.IsDraft,
		URL:       raw.URL,
		UpdatedAt: updatedAt,
	}
	if raw.Author != nil {
		item.Author = raw.Author.Login
	}
	if len(raw.Labels) > 0 {
		item.Labels = make([]string, 0, len(raw.Labels))
		for _, label := range raw.Labels {
			item.Labels = append(item.Labels, label.Name)
		}
	}
	return item, nil
}

func parseState(value string) (sharedtypes.ItemState, error) {
	switch strings.ToLower(value) {
	case "open":
		return sharedtypes.ItemStateOpen, nil
	case "closed":
		return sharedtypes.ItemStateClosed, nil
	case "merged":
		return sharedtypes.ItemStateMerged, nil
	default:
		return "", fmt.Errorf("unsupported state %q", value)
	}
}

func jsonFieldsFor(kind sharedtypes.ItemKind) string {
	if kind == sharedtypes.ItemKindPR {
		return "number,title,body,author,state,isDraft,labels,updatedAt,url"
	}
	return "number,title,body,author,state,labels,updatedAt,url"
}

func kindResource(kind sharedtypes.ItemKind) (string, error) {
	switch kind {
	case sharedtypes.ItemKindIssue:
		return "issue", nil
	case sharedtypes.ItemKindPR:
		return "pr", nil
	default:
		return "", fmt.Errorf("unsupported item kind %q", kind)
	}
}

func isWIPTitle(title string) bool {
	normalized := strings.ToLower(strings.TrimSpace(title))
	return strings.HasPrefix(normalized, "wip:") || strings.HasPrefix(normalized, "[wip]") || strings.HasPrefix(normalized, "[draft]")
}

func (commandRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	stdout, err := cmd.Output()
	if err == nil {
		return stdout, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return nil, errors.New(stderr)
		}
	}

	return nil, err
}

func classifyError(err error) error {
	message := strings.TrimSpace(err.Error())
	if !strings.Contains(strings.ToLower(message), "rate limit") {
		return err
	}

	return &RateLimitError{
		Message:    message,
		RetryAfter: parseRetryAfter(message),
	}
}

func parseRetryAfter(message string) time.Duration {
	matches := retryAfterPattern.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0
	}

	retryAfterText := strings.TrimSpace(strings.ToLower(matches[1]))
	retryAfter, err := time.ParseDuration(retryAfterText)
	if err != nil {
		return parseRetryAfterWords(retryAfterText)
	}
	return retryAfter
}

func parseRetryAfterWords(value string) time.Duration {
	matches := retryAfterWordPartPattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return 0
	}

	var total time.Duration
	consumed := strings.Builder{}
	for _, match := range matches {
		if len(match) != 3 {
			return 0
		}

		quantity, err := strconv.Atoi(match[1])
		if err != nil {
			return 0
		}

		unitDuration, ok := retryAfterUnitDuration(match[2])
		if !ok {
			return 0
		}

		total += time.Duration(quantity) * unitDuration
		consumed.WriteString(match[0])
	}

	remainder := retryAfterWordPartPattern.ReplaceAllString(value, "")
	remainder = strings.ReplaceAll(remainder, "and", "")
	remainder = strings.TrimSpace(remainder)
	if remainder != "" {
		return 0
	}

	return total
}

func retryAfterUnitDuration(value string) (time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hour", "hours", "hr", "hrs":
		return time.Hour, true
	case "minute", "minutes", "min", "mins":
		return time.Minute, true
	case "second", "seconds", "sec", "secs":
		return time.Second, true
	default:
		return 0, false
	}
}
