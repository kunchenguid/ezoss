package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/triage"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

const triagedLabel = "ezoss/triaged"

const triagedRefreshInterval = time.Hour

const staleDetectorModel = "stale-detector"

// defaultPerItemTriageTimeout caps how long a single agent invocation
// is allowed to run before we give up on it and move to the next item.
// 30 minutes is generous enough for slow Claude calls plus retries but
// short enough that a stuck subprocess can't wedge the daemon for
// hours.
const defaultPerItemTriageTimeout = 30 * time.Minute

const defaultPerFixJobTimeout = 30 * time.Minute

type triageLister interface {
	ListNeedingTriage(ctx context.Context, repo string) ([]ghclient.Item, error)
	ListTriaged(ctx context.Context, repo string, sinceUpdated time.Time) ([]ghclient.Item, error)
}

// contribSearcher is the optional capability the contributor sweep
// needs. The daemon checks for it via type assertion on Poller.GitHub
// so triageLister stays minimal and existing test stubs that don't
// implement search keep working.
type contribSearcher interface {
	SearchAuthoredOpenPRs(ctx context.Context) ([]ghclient.Item, error)
	SearchAuthoredOpenIssues(ctx context.Context) ([]ghclient.Item, error)
	ListOwnedRepos(ctx context.Context, visibility ghclient.RepoVisibility) ([]string, error)
}

type itemGetter interface {
	GetItem(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int) (ghclient.Item, error)
}

// currentUserResolver lets the poller learn the authenticated user's
// GitHub login. It is optional - tests using minimal stubs don't need to
// implement it; mock daemons skip the self-author filter entirely.
type currentUserResolver interface {
	CurrentUser(ctx context.Context) (string, error)
}

// nonSelfActivityChecker reports whether a PR's timeline contains any
// activity by an actor other than the running user that occurred at or
// after since. A zero since scans the whole timeline. It is the signal
// we use to re-queue self-authored maintainer PRs once someone else
// engages.
type nonSelfActivityChecker interface {
	HasNonSelfActivity(ctx context.Context, repo string, number int, selfLogin string, since time.Time) (bool, error)
}

type labelActivityChecker interface {
	HasActivityAfterLabel(ctx context.Context, repo string, number int, label string) (bool, error)
}

type labelActivitySinceChecker interface {
	HasActivityAfterLabelSince(ctx context.Context, repo string, number int, label string, since time.Time) (bool, error)
}

type labelActivitySinceUpdatedChecker interface {
	HasActivityAfterLabelSinceUpdated(ctx context.Context, repo string, number int, label string, since time.Time, updatedAt time.Time) (bool, error)
}

type triageRunner interface {
	Triage(ctx context.Context, req TriageRequest) (*TriageResult, error)
}

type fixRunner interface {
	RunFix(ctx context.Context, job db.FixJob, progress func(db.FixJobUpdate) error) (*FixResult, error)
	DetectPR(ctx context.Context, job db.FixJob) (string, error)
}

type FixResult struct {
	Branch                 string
	WorktreePath           string
	PRURL                  string
	WaitingForPR           bool
	WaitingForManualReview bool
}

type TriageRequest struct {
	Item   ghclient.Item
	Prompt string
	Schema json.RawMessage
}

type TriageResult struct {
	Agent          sharedtypes.AgentName
	Model          string
	Recommendation *triage.Recommendation
	TokensIn       int
	TokensOut      int
}

// ActivityProbeState holds the per-repo throttle for the deep activity
// probe pass. It is process-local on purpose: the bounded ListTriaged
// delta query is the durable backstop, so a daemon restart re-probing
// every repo once is harmless and avoids a schema migration.
type ActivityProbeState struct {
	mu          sync.Mutex
	lastProbeAt map[string]time.Time
}

func NewActivityProbeState() *ActivityProbeState {
	return &ActivityProbeState{lastProbeAt: make(map[string]time.Time)}
}

func (s *ActivityProbeState) shouldProbe(repoID string, now time.Time, interval time.Duration) bool {
	if s == nil || interval <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.lastProbeAt[repoID]
	if !ok {
		return true
	}
	return now.Sub(last) >= interval
}

func (s *ActivityProbeState) markProbed(repoID string, at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastProbeAt[repoID] = at
}

type Poller struct {
	DB                 *db.DB
	GitHub             triageLister
	Triage             triageRunner
	Fix                fixRunner
	AgentsInstructions string
	RerunInstructions  string
	StaleThreshold     time.Duration
	// IgnoreOlderThan skips items whose last update is older than this.
	// Zero disables the filter (every item is processed).
	IgnoreOlderThan time.Duration
	// Hooks observes per-repo and per-item progress. Optional; zero
	// value disables.
	Hooks PollHooks
	// Logger receives structured progress and failure events. Optional;
	// nil silently drops every log call so tests and bare callers don't
	// have to construct one.
	Logger *slog.Logger
	// PerItemTriageTimeout caps a single triage invocation. Zero means
	// defaultPerItemTriageTimeout (30m).
	PerItemTriageTimeout time.Duration
	// PerFixJobTimeout caps a single fix job invocation. Zero means
	// defaultPerFixJobTimeout (30m).
	PerFixJobTimeout time.Duration
	// ContribEnabled gates Stage A.2 (the contributor sweep). When
	// false the daemon behaves exactly as before: maintainer items
	// only.
	ContribEnabled bool
	// ContribIgnoreRepos is the list of "owner/name" strings to drop
	// from contributor sweep results before upsert. Useful for noisy
	// upstreams the user does not want in their inbox.
	ContribIgnoreRepos       []string
	PreserveExistingItemRole bool
	// ActivityProbeInterval throttles the deep activity probe that
	// detects post-triage timeline activity GitHub does not surface
	// via issue.updated_at - in particular, "Refs"-style PRs being
	// merged. Zero disables the probe.
	ActivityProbeInterval time.Duration
	// ActivityProbeState is the per-repo throttle for the probe.
	// Nil disables the probe even when ActivityProbeInterval > 0;
	// the daemon constructs one in cli/root.go and tests opt in by
	// passing NewActivityProbeState().
	ActivityProbeState *ActivityProbeState
}

func (p Poller) log() *slog.Logger {
	if p.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Logger
}

// PollOnce runs a full poll cycle in sequential stages: stage A fetches
// GitHub data for every configured repo into the local DB, stage B processes
// daemon-backed fix jobs, and stage C invokes the triage runner for items
// that need agent attention. If stage B does fix work, the cycle stops before
// agent triage so fix runs and triage runs do not contend for agent capacity.
func PollOnce(ctx context.Context, poller Poller, repos []string) error {
	if poller.DB == nil {
		return fmt.Errorf("poller db: nil")
	}
	if poller.GitHub == nil {
		return fmt.Errorf("poller github client: nil")
	}

	polledAt := time.Now().UTC()

	var errs []error
	if err := runSyncStage(ctx, poller, repos, polledAt); err != nil {
		errs = append(errs, err)
	}
	if err := runActivityProbeStage(ctx, poller, repos, polledAt); err != nil {
		errs = append(errs, err)
	}
	contribRepos, err := runContribSweep(ctx, poller, repos, polledAt)
	if err != nil {
		errs = append(errs, err)
	}
	fixDidWork, fixErr := runFixStage(ctx, poller)
	if fixErr != nil {
		errs = append(errs, fixErr)
	}
	if fixDidWork {
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		return nil
	}
	agentRepos := repos
	if len(contribRepos) > 0 {
		agentRepos = mergeUnique(repos, contribRepos)
	}
	if err := runAgentsStage(ctx, poller, agentRepos, polledAt); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func runSyncStage(ctx context.Context, poller Poller, repos []string, polledAt time.Time) error {
	total := len(repos)
	if poller.Hooks.OnSyncBegin != nil {
		poller.Hooks.OnSyncBegin(total)
	}
	var errs []error
	for i, repoID := range repos {
		if poller.Hooks.OnRepoBegin != nil {
			poller.Hooks.OnRepoBegin(repoID, i, total)
		}
		startedAt := time.Now()
		err := syncRepoData(ctx, poller, repoID, polledAt)
		duration := time.Since(startedAt)
		if poller.Hooks.OnRepoEnd != nil {
			poller.Hooks.OnRepoEnd(repoID, err)
		}
		if err != nil {
			poller.log().Warn("repo sync failed",
				"repo", repoID,
				"duration", duration,
				"err", err,
			)
			errs = append(errs, err)
		} else {
			poller.log().Info("repo synced",
				"repo", repoID,
				"duration", duration,
			)
		}
	}
	if poller.Hooks.OnSyncEnd != nil {
		poller.Hooks.OnSyncEnd()
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// syncRepoData performs the GitHub-side data sync for one repo: fetches
// untriaged items, upserts them and the repo record, and refreshes
// already-triaged items if the refresh interval has elapsed. It does
// not invoke the triage agent - that runs later in the agents stage
// after every repo's data is up to date.
func syncRepoData(ctx context.Context, poller Poller, repoID string, polledAt time.Time) error {
	items, err := poller.GitHub.ListNeedingTriage(ctx, repoID)
	if err != nil {
		return fmt.Errorf("poll repo %s: list needing triage: %w", repoID, err)
	}

	repoSource := db.RepoSourceConfig
	if poller.PreserveExistingItemRole {
		repoSource = db.RepoSourceContrib
	}
	if err := poller.DB.UpsertRepo(db.Repo{ID: repoID, Source: repoSource, LastPollAt: &polledAt}); err != nil {
		return fmt.Errorf("poll repo %s: upsert repo: %w", repoID, err)
	}

	// Self-authored PRs in maintainer (configured) repos are not interesting
	// for triage - the user already knows what's in their own work. We
	// flip gh_triaged on locally so Stage C skips them. Issues are out
	// of scope: a self-filed bug report may still warrant a triage
	// recommendation. Contributor-mode repos go through PreserveExistingItemRole
	// and are handled as authored work by Stage A.2 instead.
	selfLogin := ""
	if !poller.PreserveExistingItemRole {
		selfLogin = resolveSelfLogin(ctx, poller)
	}

	activityChecker, _ := poller.GitHub.(nonSelfActivityChecker)

	seenOpenUntriaged := make(map[int]struct{}, len(items))
	for _, item := range items {
		if isOlderThan(item.UpdatedAt, polledAt, poller.IgnoreOlderThan) {
			continue
		}
		seenOpenUntriaged[item.Number] = struct{}{}
		existing, err := poller.DB.GetItem(itemID(repoID, item.Number))
		if err != nil {
			return fmt.Errorf("poll repo %s: get item %d: %w", repoID, item.Number, err)
		}
		ghTriaged := hasLabel(item.Labels, triagedLabel)
		if !ghTriaged && isSelfAuthoredMaintainerPR(item, selfLogin) {
			ghTriaged = selfPRGHTriaged(ctx, poller, activityChecker, repoID, item, selfLogin, existing)
		}
		itemRecord := db.Item{
			ID:          itemID(repoID, item.Number),
			RepoID:      repoID,
			Kind:        item.Kind,
			Number:      item.Number,
			Title:       item.Title,
			Author:      item.Author,
			State:       item.State,
			IsDraft:     item.IsDraft,
			GHTriaged:   ghTriaged,
			WaitingOn:   sharedtypes.WaitingOnNone,
			LastEventAt: timePtr(item.UpdatedAt.UTC()),
		}
		if existing != nil {
			if poller.PreserveExistingItemRole {
				itemRecord.Role = existing.Role
				itemRecord.HeadRepo = existing.HeadRepo
				itemRecord.HeadRef = existing.HeadRef
				itemRecord.HeadCloneURL = existing.HeadCloneURL
			}
			itemRecord.WaitingOn = existing.WaitingOn
			itemRecord.StaleSince = existing.StaleSince
		}
		if err := poller.DB.UpsertItem(itemRecord); err != nil {
			return fmt.Errorf("poll repo %s: upsert item %d: %w", repoID, item.Number, err)
		}
		// One-shot backfill: if a self-authored PR is being suppressed
		// for the first time but had recommendations from earlier
		// cycles, drop them from the inbox so the user does not have
		// to dismiss their own work by hand.
		if itemRecord.GHTriaged && existing != nil && !existing.GHTriaged && isSelfAuthoredMaintainerPR(item, selfLogin) {
			if err := poller.DB.MarkActiveRecommendationsForItemSuperseded(itemRecord.ID, polledAt); err != nil {
				return fmt.Errorf("poll repo %s: supersede self-authored recs for %d: %w", repoID, item.Number, err)
			}
		}
	}
	if err := reconcileMissingActiveRecommendations(ctx, poller, repoID, seenOpenUntriaged, polledAt); err != nil {
		return fmt.Errorf("poll repo %s: reconcile active recommendations: %w", repoID, err)
	}

	if shouldRefreshTriagedItems(poller, repoID, polledAt) {
		if err := refreshTriagedItems(ctx, poller, repoID, polledAt); err != nil {
			return fmt.Errorf("poll repo %s: refresh triaged items: %w", repoID, err)
		}
	}

	return nil
}

func reconcileMissingActiveRecommendations(ctx context.Context, poller Poller, repoID string, seenOpenUntriaged map[int]struct{}, polledAt time.Time) error {
	getter, ok := poller.GitHub.(itemGetter)
	if !ok {
		return nil
	}
	recommendations, err := poller.DB.ListActiveRecommendations()
	if err != nil {
		return err
	}
	activityChecker, _ := poller.GitHub.(labelActivityChecker)
	for _, recommendation := range recommendations {
		cached, err := poller.DB.GetItem(recommendation.ItemID)
		if err != nil {
			return err
		}
		if cached == nil || cached.RepoID != repoID || cached.GHTriaged {
			continue
		}
		if _, ok := seenOpenUntriaged[cached.Number]; ok {
			continue
		}
		current, err := getter.GetItem(ctx, cached.RepoID, cached.Kind, cached.Number)
		if err != nil {
			return fmt.Errorf("get item %d: %w", cached.Number, err)
		}
		ghTriaged, err := reconciledGHTriaged(ctx, activityChecker, cached, current)
		if err != nil {
			return fmt.Errorf("check post-triage activity for item %d: %w", cached.Number, err)
		}
		itemRecord := db.Item{
			ID:                 cached.ID,
			RepoID:             repoID,
			Kind:               current.Kind,
			Role:               cached.Role,
			Number:             current.Number,
			Title:              current.Title,
			Author:             current.Author,
			State:              current.State,
			IsDraft:            current.IsDraft,
			GHTriaged:          ghTriaged,
			WaitingOn:          cached.WaitingOn,
			LastEventAt:        timePtr(current.UpdatedAt.UTC()),
			LastSelfActivityAt: cached.LastSelfActivityAt,
			StaleSince:         cached.StaleSince,
			HeadRepo:           cached.HeadRepo,
			HeadRef:            cached.HeadRef,
			HeadCloneURL:       cached.HeadCloneURL,
		}
		if current.State != sharedtypes.ItemStateOpen {
			itemRecord.StaleSince = nil
		}
		if err := poller.DB.UpsertItem(itemRecord); err != nil {
			return fmt.Errorf("upsert item %d: %w", cached.Number, err)
		}
		if current.State != sharedtypes.ItemStateOpen || itemRecord.GHTriaged {
			if err := poller.DB.MarkRecommendationSuperseded(recommendation.ID, polledAt); err != nil {
				return fmt.Errorf("supersede recommendation for item %d: %w", cached.Number, err)
			}
		}
	}
	return nil
}

// runContribSweep runs Stage A.2: the contributor item source. When
// ContribEnabled is true and the underlying GitHub client supports the
// search methods, it queries `gh search prs/issues --author=@me`,
// upserts each item with role=contributor and source=contrib on its
// repo, and prunes contrib repos that no longer have any open
// contributor items. Returns the list of repos touched so the agents
// stage can include them when picking items to triage.
//
// Items whose repo is in maintainerRepos are deliberately skipped: the
// maintainer sync (Stage A.1) is the source of truth for those repos
// and treats authored items the same as any other. Contributor mode is
// only for repos the user does not maintain.
func runContribSweep(ctx context.Context, poller Poller, maintainerRepos []string, polledAt time.Time) ([]string, error) {
	if !poller.ContribEnabled {
		return nil, nil
	}
	searcher, ok := poller.GitHub.(contribSearcher)
	if !ok {
		return nil, nil
	}

	ignore := make(map[string]struct{}, len(poller.ContribIgnoreRepos))
	for _, r := range poller.ContribIgnoreRepos {
		if r = strings.TrimSpace(r); r != "" {
			ignore[r] = struct{}{}
		}
	}
	maintainer := make(map[string]struct{}, len(maintainerRepos))
	for _, r := range maintainerRepos {
		if r = strings.TrimSpace(r); r != "" {
			maintainer[r] = struct{}{}
		}
	}
	// Repos the authenticated user owns on GitHub are also "mine" -
	// even when not in cfg.Repos, the user shouldn't see their own
	// items as "contributor" in the inbox. A failure here downgrades to
	// "no ownership filter applied" rather than aborting the sweep,
	// since gh repo list errors shouldn't blackhole the inbox.
	owned := make(map[string]struct{})
	if ownedList, err := searcher.ListOwnedRepos(ctx, ghclient.RepoVisibilityAll); err != nil {
		poller.log().Warn("contrib sweep: list owned repos failed (no ownership filter this cycle)", "err", err)
	} else {
		for _, r := range ownedList {
			if r = strings.TrimSpace(r); r != "" {
				owned[r] = struct{}{}
			}
		}
	}

	var errs []error
	authoredSearchesComplete := true
	prs, err := searcher.SearchAuthoredOpenPRs(ctx)
	if err != nil {
		authoredSearchesComplete = false
		errs = append(errs, fmt.Errorf("contrib sweep: list authored prs: %w", err))
	}
	issues, err := searcher.SearchAuthoredOpenIssues(ctx)
	if err != nil {
		authoredSearchesComplete = false
		errs = append(errs, fmt.Errorf("contrib sweep: list authored issues: %w", err))
	}

	repoSet := make(map[string]struct{})
	seenItemIDs := make(map[string]struct{})

	upsert := func(item ghclient.Item) error {
		if item.Repo == "" {
			return nil
		}
		if _, skip := ignore[item.Repo]; skip {
			return nil
		}
		// Items in repos the user maintains belong to Stage A.1, not
		// the contributor sweep. Skipping here avoids two stages
		// fighting over the same item with different roles.
		if _, isMaintainer := maintainer[item.Repo]; isMaintainer {
			return nil
		}
		// Items in repos the user owns on GitHub but hasn't added to
		// cfg.Repos: still not "contributor" - the user can review
		// them via the maintainer flow whenever they configure the
		// repo. Surfacing them as contributor in the meantime is
		// misleading.
		if _, isOwned := owned[item.Repo]; isOwned {
			return nil
		}
		if isOlderThan(item.UpdatedAt, polledAt, poller.IgnoreOlderThan) {
			return nil
		}
		repoSet[item.Repo] = struct{}{}
		if err := poller.DB.UpsertRepo(db.Repo{
			ID:         item.Repo,
			Source:     db.RepoSourceContrib,
			LastPollAt: &polledAt,
		}); err != nil {
			return fmt.Errorf("upsert contrib repo %s: %w", item.Repo, err)
		}
		id := itemID(item.Repo, item.Number)
		seenItemIDs[id] = struct{}{}
		existing, err := poller.DB.GetItem(id)
		if err != nil {
			return fmt.Errorf("get contrib item %s: %w", id, err)
		}
		updated := item.UpdatedAt.UTC()
		record := db.Item{
			ID:                id,
			RepoID:            item.Repo,
			Kind:              item.Kind,
			Role:              sharedtypes.RoleContributor,
			Number:            item.Number,
			Title:             item.Title,
			Author:            item.Author,
			State:             item.State,
			IsDraft:           item.IsDraft,
			GHTriaged:         false,
			WaitingOn:         sharedtypes.WaitingOnNone,
			LastSeenUpdatedAt: timePtr(updated),
			HeadRepo:          item.HeadRepo,
			HeadRef:           item.HeadRef,
			HeadCloneURL:      item.HeadCloneURL,
		}
		if existing != nil {
			record.GHTriaged = existing.GHTriaged
			record.WaitingOn = existing.WaitingOn
			record.StaleSince = existing.StaleSince
			record.LastSelfActivityAt = existing.LastSelfActivityAt
			record.LastSeenCommentID = existing.LastSeenCommentID
			if record.HeadRepo == "" {
				record.HeadRepo = existing.HeadRepo
			}
			if record.HeadRef == "" {
				record.HeadRef = existing.HeadRef
			}
			if record.HeadCloneURL == "" {
				record.HeadCloneURL = existing.HeadCloneURL
			}
			// Only bump LastEventAt when GitHub reports a newer
			// updated_at than we already saw. This is the watermark
			// that drives ListItemsNeedingTriage.
			prev := time.Time{}
			if existing.LastSeenUpdatedAt != nil {
				prev = existing.LastSeenUpdatedAt.UTC()
			}
			selfActivity := time.Time{}
			if existing.LastSelfActivityAt != nil {
				selfActivity = existing.LastSelfActivityAt.UTC()
			}
			if updated.After(prev) && (selfActivity.IsZero() || updated.After(selfActivity)) {
				record.GHTriaged = false
				record.LastEventAt = timePtr(updated)
			} else {
				record.LastEventAt = existing.LastEventAt
			}
		} else {
			record.LastEventAt = timePtr(updated)
		}
		if err := poller.DB.UpsertItem(record); err != nil {
			return fmt.Errorf("upsert contrib item %s: %w", id, err)
		}
		return nil
	}

	for _, item := range prs {
		if err := upsert(item); err != nil {
			errs = append(errs, err)
		}
	}
	for _, item := range issues {
		if err := upsert(item); err != nil {
			errs = append(errs, err)
		}
	}

	if authoredSearchesComplete {
		if err := pruneContribRepos(poller, maintainer, repoSet, seenItemIDs); err != nil {
			errs = append(errs, err)
		}
	}

	repos := make([]string, 0, len(repoSet))
	for r := range repoSet {
		repos = append(repos, r)
	}
	if len(errs) > 0 {
		return repos, errors.Join(errs...)
	}
	return repos, nil
}

// pruneContribRepos removes contributor items that the latest sweep no
// longer returned (their PR was merged, the issue closed, etc) and then
// removes any contrib-source repo that no longer holds any contributor
// items. Maintainer (config) repos are left alone.
func pruneContribRepos(poller Poller, maintainerRepos map[string]struct{}, sweptRepos map[string]struct{}, sweptItems map[string]struct{}) error {
	repos, err := poller.DB.ListReposWithContributorItems()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		repoID := repo.ID
		if _, ok := maintainerRepos[repoID]; ok {
			continue
		}
		items, err := poller.DB.ListContributorItemsForRepo(repoID)
		if err != nil {
			return err
		}
		for _, it := range items {
			if _, ok := sweptItems[it.ID]; ok {
				continue
			}
			if err := poller.DB.DeleteItem(it.ID); err != nil {
				return err
			}
		}
		remaining, err := poller.DB.ListContributorItemsForRepo(repoID)
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			if _, err := poller.DB.DeleteRepoIfContrib(repoID); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeUnique(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, v := range a {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range b {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func runAgentsStage(ctx context.Context, poller Poller, repos []string, polledAt time.Time) error {
	pendingItems, err := poller.DB.ListItemsNeedingTriage()
	if err != nil {
		return fmt.Errorf("agents stage: list pending items: %w", err)
	}
	pendingItems = filterItemsByRepo(pendingItems, repos)
	pendingItems = filterItemsByAge(pendingItems, polledAt, poller.IgnoreOlderThan)

	if poller.Hooks.OnAgentsBegin != nil {
		poller.Hooks.OnAgentsBegin(len(pendingItems))
	}
	defer func() {
		if poller.Hooks.OnAgentsEnd != nil {
			poller.Hooks.OnAgentsEnd()
		}
	}()

	if poller.Triage != nil {
		for i, item := range pendingItems {
			triageItem(ctx, poller, item, polledAt, i, len(pendingItems))
		}
	}

	var errs []error
	for _, repoID := range repos {
		if err := surfaceStaleRecommendations(poller, repoID, polledAt); err != nil {
			errs = append(errs, fmt.Errorf("agents stage: stale check %s: %w", repoID, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// triageItem invokes the triage runner on a single item and persists
// the resulting recommendation. Per-item failures are non-fatal: they
// are logged via poller.Logger and the loop moves on so a single bad
// agent invocation cannot stall the whole cycle.
func triageItem(ctx context.Context, poller Poller, item db.Item, polledAt time.Time, idx, total int) {
	itemID := item.ID
	if poller.Hooks.OnAgentItemBegin != nil {
		poller.Hooks.OnAgentItemBegin(itemID, idx, total)
	}
	startedAt := time.Now()
	result, err := runTriageForItem(ctx, poller, item, polledAt)
	duration := time.Since(startedAt)
	if poller.Hooks.OnAgentItemEnd != nil {
		poller.Hooks.OnAgentItemEnd(itemID, err)
	}
	if err != nil {
		// Distinguish per-item timeout from generic agent failure so the
		// log can answer "did claude hang?" without scraping the error
		// string.
		reason := "error"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		poller.log().Warn("triage failed",
			"item", itemID,
			"reason", reason,
			"duration", duration,
			"err", err,
		)
		return
	}
	attrs := []any{
		"item", itemID,
		"duration", duration,
	}
	if result != nil {
		attrs = append(attrs,
			"agent", string(result.Agent),
			"model", result.Model,
			"tokens_in", result.TokensIn,
			"tokens_out", result.TokensOut,
		)
		if result.Recommendation != nil {
			attrs = append(attrs, "options", len(result.Recommendation.Options))
		}
	}
	poller.log().Info("triage done", attrs...)
}

func runTriageForItem(ctx context.Context, poller Poller, item db.Item, polledAt time.Time) (*TriageResult, error) {
	timeout := poller.PerItemTriageTimeout
	if timeout <= 0 {
		timeout = defaultPerItemTriageTimeout
	}
	itemCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ghItem := dbItemToGHItem(item)
	role := item.Role
	if role == "" {
		role = sharedtypes.RoleMaintainer
	}
	result, err := poller.Triage.Triage(itemCtx, TriageRequest{
		Item:   ghItem,
		Prompt: triage.PromptForRole(role, ghItem.URL, poller.AgentsInstructions, poller.RerunInstructions),
		Schema: triage.Schema(),
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Recommendation == nil {
		return nil, errors.New("empty recommendation")
	}

	options := make([]db.NewRecommendationOption, 0, len(result.Recommendation.Options))
	for _, opt := range result.Recommendation.Options {
		options = append(options, db.NewRecommendationOption{
			StateChange:  opt.StateChange,
			Rationale:    opt.Rationale,
			DraftComment: opt.DraftComment,
			FixPrompt:    opt.FixPrompt,
			Followups:    opt.Followups,
			Confidence:   opt.Confidence,
			WaitingOn:    opt.WaitingOn,
		})
	}
	_, inserted, err := poller.DB.InsertRecommendationReplacingActiveBefore(db.NewRecommendation{
		ItemID:            item.ID,
		Agent:             result.Agent,
		Model:             result.Model,
		TokensIn:          result.TokensIn,
		TokensOut:         result.TokensOut,
		RerunInstructions: poller.RerunInstructions,
		Options:           options,
	}, polledAt)
	if err != nil {
		return nil, fmt.Errorf("insert recommendation: %w", err)
	}
	if !inserted {
		poller.log().Info("discarded stale triage result", "item", item.ID)
	}

	return result, nil
}

func filterItemsByRepo(items []db.Item, repos []string) []db.Item {
	if len(repos) == 0 {
		return nil
	}
	configured := make(map[string]struct{}, len(repos))
	for _, repoID := range repos {
		configured[repoID] = struct{}{}
	}
	out := items[:0]
	for _, item := range items {
		if _, ok := configured[item.RepoID]; ok {
			out = append(out, item)
		}
	}
	return out
}

// filterItemsByAge drops items whose LastEventAt is older than threshold
// relative to now. threshold <= 0 disables the filter (all items pass).
// Items with a nil LastEventAt are kept - we have no signal to age them out.
func filterItemsByAge(items []db.Item, now time.Time, threshold time.Duration) []db.Item {
	if threshold <= 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if item.LastEventAt == nil {
			out = append(out, item)
			continue
		}
		if isOlderThan(*item.LastEventAt, now, threshold) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// isOlderThan reports whether updatedAt + threshold is before now. A
// non-positive threshold disables the check (returns false).
func isOlderThan(updatedAt, now time.Time, threshold time.Duration) bool {
	if threshold <= 0 || updatedAt.IsZero() {
		return false
	}
	return updatedAt.Add(threshold).Before(now)
}

func dbItemToGHItem(item db.Item) ghclient.Item {
	url := fmt.Sprintf("https://github.com/%s/%s/%d", item.RepoID, ghPathSegment(item.Kind), item.Number)
	updatedAt := time.Time{}
	if item.LastEventAt != nil {
		updatedAt = item.LastEventAt.UTC()
	}
	return ghclient.Item{
		Repo:      item.RepoID,
		Kind:      item.Kind,
		Number:    item.Number,
		Title:     item.Title,
		Author:    item.Author,
		State:     item.State,
		IsDraft:   item.IsDraft,
		URL:       url,
		UpdatedAt: updatedAt,
	}
}

func ghPathSegment(kind sharedtypes.ItemKind) string {
	if kind == sharedtypes.ItemKindPR {
		return "pull"
	}
	return "issues"
}

func reconciledGHTriaged(ctx context.Context, checker labelActivityChecker, cached *db.Item, item ghclient.Item) (bool, error) {
	ghTriaged := hasLabel(item.Labels, triagedLabel)
	if !ghTriaged || cached == nil || cached.GHTriaged || cached.LastEventAt == nil || item.UpdatedAt.IsZero() || item.State != sharedtypes.ItemStateOpen {
		return ghTriaged, nil
	}
	if cached.LastEventAt.UTC().Equal(item.UpdatedAt.UTC()) {
		return false, nil
	}
	if checker == nil {
		return ghTriaged, nil
	}
	hasActivity, err := hasActivityAfterLabel(ctx, checker, cached, cached.RepoID, cached.Number, item.UpdatedAt)
	if err != nil {
		return false, err
	}
	if hasActivity {
		return false, nil
	}
	return ghTriaged, nil
}

func hasActivityAfterLabel(ctx context.Context, checker labelActivityChecker, cached *db.Item, repoID string, number int, updatedAt time.Time) (bool, error) {
	if cached != nil && cached.LastSelfActivityAt != nil {
		since := cached.LastSelfActivityAt.UTC()
		if updatedChecker, ok := checker.(labelActivitySinceUpdatedChecker); ok {
			return updatedChecker.HasActivityAfterLabelSinceUpdated(ctx, repoID, number, triagedLabel, since, updatedAt.UTC())
		}
		if sinceChecker, ok := checker.(labelActivitySinceChecker); ok {
			return sinceChecker.HasActivityAfterLabelSince(ctx, repoID, number, triagedLabel, since)
		}
	}
	return checker.HasActivityAfterLabel(ctx, repoID, number, triagedLabel)
}

func shouldCheckPostLabelActivity(cached *db.Item, item ghclient.Item) bool {
	if cached == nil {
		return false
	}
	if !cached.GHTriaged || cached.LastEventAt == nil || item.UpdatedAt.IsZero() {
		return true
	}
	if cached.LastSelfActivityAt != nil && !item.UpdatedAt.UTC().After(cached.LastSelfActivityAt.UTC()) {
		return false
	}
	return !cached.LastEventAt.UTC().Equal(item.UpdatedAt.UTC())
}

// runActivityProbeStage walks every locally-known open triaged
// maintainer item per repo and runs the timeline activity check
// unconditionally, throttled by Poller.ActivityProbeInterval.
//
// The bounded ListTriaged delta query in refreshTriagedItems uses
// `updated:>=<last_refresh>` and therefore excludes items whose
// GitHub updated_at has not advanced. Non-self timeline activity
// that does not bump updated_at - notably "Refs"-style PRs being
// merged - would otherwise never re-queue the issue. This stage is
// the catch-all.
//
// The watermark is the latest recommendation timestamp for the item:
// "what was the agent looking at the last time it ran here". Probe
// asks ghclient "any non-self activity after the watermark?" If yes,
// gh_triaged is cleared and Stage C re-triages.
func runActivityProbeStage(ctx context.Context, poller Poller, repos []string, polledAt time.Time) error {
	if poller.ActivityProbeInterval <= 0 || poller.ActivityProbeState == nil {
		return nil
	}
	sinceChecker, ok := poller.GitHub.(labelActivitySinceChecker)
	if !ok {
		return nil
	}

	var errs []error
	for _, repoID := range repos {
		if !poller.ActivityProbeState.shouldProbe(repoID, polledAt, poller.ActivityProbeInterval) {
			continue
		}
		if err := probeActivityForRepo(ctx, poller, sinceChecker, repoID); err != nil {
			poller.log().Warn("activity probe failed", "repo", repoID, "err", err)
			errs = append(errs, fmt.Errorf("probe activity %s: %w", repoID, err))
			continue
		}
		poller.ActivityProbeState.markProbed(repoID, polledAt)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func probeActivityForRepo(ctx context.Context, poller Poller, checker labelActivitySinceChecker, repoID string) error {
	items, err := poller.DB.ListMaintainerOpenTriagedItems(repoID)
	if err != nil {
		return fmt.Errorf("list maintainer open triaged items: %w", err)
	}
	for _, item := range items {
		watermark, err := poller.DB.LatestRecommendationCreatedAtForItem(item.ID)
		if err != nil {
			poller.log().Warn("activity probe watermark lookup failed",
				"repo", repoID,
				"number", item.Number,
				"err", err,
			)
			continue
		}
		hasActivity, err := checker.HasActivityAfterLabelSince(ctx, repoID, item.Number, triagedLabel, watermark)
		if err != nil {
			poller.log().Warn("activity probe item failed",
				"repo", repoID,
				"number", item.Number,
				"err", err,
			)
			continue
		}
		if !hasActivity {
			continue
		}
		cleared := item
		cleared.GHTriaged = false
		if err := poller.DB.UpsertItem(cleared); err != nil {
			return fmt.Errorf("clear gh_triaged for item %d: %w", item.Number, err)
		}
		if err := poller.DB.MarkActiveRecommendationsForItemSuperseded(item.ID, time.Now().UTC()); err != nil {
			return fmt.Errorf("supersede active recommendations for item %d: %w", item.Number, err)
		}
		poller.log().Info("activity probe re-queued item",
			"repo", repoID,
			"number", item.Number,
		)
	}
	return nil
}

func shouldRefreshTriagedItems(poller Poller, repoID string, polledAt time.Time) bool {
	repo, err := poller.DB.GetRepo(repoID)
	if err != nil || repo == nil || repo.LastTriagedRefreshAt == nil {
		return true
	}
	return polledAt.Sub(repo.LastTriagedRefreshAt.UTC()) >= triagedRefreshInterval
}

func refreshTriagedItems(ctx context.Context, poller Poller, repoID string, polledAt time.Time) error {
	// Bound the triaged-list query by the previous refresh time so we only
	// pay for items that actually changed since then. On the first refresh
	// (no prior timestamp) we fall back to fetching open items only - the
	// closed-item history can be unbounded and we don't need it for cold
	// start; subsequent delta refreshes will reconcile any closes.
	var sinceUpdated time.Time
	if repo, err := poller.DB.GetRepo(repoID); err == nil && repo != nil && repo.LastTriagedRefreshAt != nil {
		sinceUpdated = repo.LastTriagedRefreshAt.UTC()
	}
	items, err := poller.GitHub.ListTriaged(ctx, repoID, sinceUpdated)
	if err != nil {
		return fmt.Errorf("list triaged: %w", err)
	}
	activityChecker, _ := poller.GitHub.(labelActivityChecker)

	for _, item := range items {
		cached, err := poller.DB.GetItem(itemID(repoID, item.Number))
		if err != nil {
			return fmt.Errorf("get item %d: %w", item.Number, err)
		}

		ghTriaged := hasLabel(item.Labels, triagedLabel)
		if ghTriaged && item.State == sharedtypes.ItemStateOpen && activityChecker != nil && shouldCheckPostLabelActivity(cached, item) {
			hasActivity, err := hasActivityAfterLabel(ctx, activityChecker, cached, repoID, item.Number, item.UpdatedAt)
			if err != nil {
				return fmt.Errorf("check post-triage activity for item %d: %w", item.Number, err)
			}
			if hasActivity {
				ghTriaged = false
			}
		}

		itemRecord := db.Item{
			ID:          itemID(repoID, item.Number),
			RepoID:      repoID,
			Kind:        item.Kind,
			Number:      item.Number,
			Title:       item.Title,
			Author:      item.Author,
			State:       item.State,
			IsDraft:     item.IsDraft,
			GHTriaged:   ghTriaged,
			WaitingOn:   sharedtypes.WaitingOnNone,
			LastEventAt: timePtr(item.UpdatedAt.UTC()),
		}
		if cached != nil {
			itemRecord.WaitingOn = cached.WaitingOn
			itemRecord.StaleSince = cached.StaleSince
			itemRecord.LastSelfActivityAt = cached.LastSelfActivityAt
		}

		if itemRecord.WaitingOn == sharedtypes.WaitingOnContributor && poller.StaleThreshold > 0 {
			staleSince := item.UpdatedAt.UTC().Add(poller.StaleThreshold)
			if polledAt.Before(staleSince) {
				itemRecord.StaleSince = nil
			}
		} else {
			itemRecord.StaleSince = nil
		}

		if err := poller.DB.UpsertItem(itemRecord); err != nil {
			return fmt.Errorf("upsert item %d: %w", item.Number, err)
		}
		if itemRecord.StaleSince == nil {
			if err := supersedeActiveStaleRecommendationsForItem(poller.DB, itemRecord.ID, polledAt); err != nil {
				return fmt.Errorf("clear stale recommendation for item %d: %w", item.Number, err)
			}
		}
	}

	if err := poller.DB.UpsertRepo(db.Repo{ID: repoID, LastTriagedRefreshAt: &polledAt}); err != nil {
		return fmt.Errorf("update repo triaged refresh time: %w", err)
	}

	return nil
}

func surfaceStaleRecommendations(poller Poller, repoID string, polledAt time.Time) error {
	if poller.StaleThreshold <= 0 {
		return nil
	}

	items, err := poller.DB.ListTriagedItemsWaitingOnContributor(repoID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	active, err := poller.DB.ListActiveRecommendations()
	if err != nil {
		return err
	}
	activeByItemID := make(map[string]struct{}, len(active))
	for _, recommendation := range active {
		activeByItemID[recommendation.ItemID] = struct{}{}
	}

	for _, item := range items {
		if item.LastEventAt == nil {
			continue
		}
		staleSince := item.LastEventAt.Add(poller.StaleThreshold)
		if polledAt.Before(staleSince) {
			continue
		}

		alreadyStale := item.StaleSince != nil
		item.StaleSince = timePtr(staleSince.UTC())
		if err := poller.DB.UpsertItem(item); err != nil {
			return fmt.Errorf("update item %d stale_since: %w", item.Number, err)
		}
		if alreadyStale {
			continue
		}
		if _, ok := activeByItemID[item.ID]; ok {
			continue
		}

		if _, err := poller.DB.InsertRecommendation(db.NewRecommendation{
			ItemID: item.ID,
			Agent:  sharedtypes.AgentAuto,
			Model:  staleDetectorModel,
			Options: []db.NewRecommendationOption{{
				StateChange:    sharedtypes.StateChangeClose,
				Rationale:      "The contributor has been inactive past the configured stale threshold.",
				DraftComment:   "Closing as stale for now. Feel free to reopen with more detail.",
				FixPrompt:      "",
				ProposedLabels: []string{"stale"},
				Confidence:     sharedtypes.ConfidenceMedium,
				WaitingOn:      sharedtypes.WaitingOnContributor,
			}},
		}); err != nil {
			return fmt.Errorf("insert stale recommendation for item %d: %w", item.Number, err)
		}
		poller.log().Info("auto-recommended close (stale)",
			"item", item.ID,
			"stale_since", staleSince.UTC(),
			"threshold", poller.StaleThreshold,
		)
	}

	return nil
}

func supersedeActiveRecommendationsForItem(database *db.DB, itemID string, supersededAt time.Time) error {
	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		return err
	}
	for _, recommendation := range recommendations {
		if recommendation.ItemID != itemID {
			continue
		}
		if err := database.MarkRecommendationSuperseded(recommendation.ID, supersededAt); err != nil {
			return err
		}
	}
	return nil
}

func supersedeActiveStaleRecommendationsForItem(database *db.DB, itemID string, supersededAt time.Time) error {
	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		return err
	}
	for _, recommendation := range recommendations {
		if recommendation.ItemID != itemID || recommendation.Model != staleDetectorModel {
			continue
		}
		if err := database.MarkRecommendationSuperseded(recommendation.ID, supersededAt); err != nil {
			return err
		}
	}
	return nil
}

func itemID(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

// resolveSelfLogin returns the authenticated GitHub login for the
// running user, or "" if the GitHub client cannot resolve it. Callers
// treat "" as "skip the self-author filter" rather than as an error,
// since an unreachable gh CLI should not block the rest of the sync.
func resolveSelfLogin(ctx context.Context, poller Poller) string {
	resolver, ok := poller.GitHub.(currentUserResolver)
	if !ok {
		return ""
	}
	login, err := resolver.CurrentUser(ctx)
	if err != nil {
		poller.log().Warn("resolve current user", "err", err)
		return ""
	}
	return strings.TrimSpace(login)
}

// selfPRGHTriaged decides whether a self-authored maintainer PR should
// be marked locally triaged this cycle. The default is "yes" (suppress
// it from the inbox), but if the PR's timeline shows any non-self
// activity since we last saw it we flip the gate off so Stage C
// re-triages with that new context. Unchanged PRs (UpdatedAt has not
// moved since the cached LastEventAt) are kept suppressed without an
// extra timeline call - that's how we avoid spamming the GitHub API on
// every poll cycle. The timeline scan is bounded by the prior
// LastEventAt so old foreign activity (e.g. a CI bot comment from
// months ago) does not keep re-queueing the PR every time UpdatedAt
// advances; first-sighting (existing == nil) scans the full timeline.
func selfPRGHTriaged(ctx context.Context, poller Poller, checker nonSelfActivityChecker, repoID string, item ghclient.Item, selfLogin string, existing *db.Item) bool {
	if existing != nil && existing.GHTriaged && existing.LastEventAt != nil && existing.LastEventAt.Equal(item.UpdatedAt.UTC()) {
		return true
	}
	if checker == nil {
		return true
	}
	var since time.Time
	if existing != nil && existing.LastEventAt != nil {
		since = existing.LastEventAt.UTC()
	}
	hasOther, err := checker.HasNonSelfActivity(ctx, repoID, item.Number, selfLogin, since)
	if err != nil {
		// On failure, fall back to the safe option: leave the PR
		// suppressed. The next cycle that sees a fresh UpdatedAt
		// will retry the timeline check.
		poller.log().Warn("non-self activity check failed", "repo", repoID, "number", item.Number, "err", err)
		return true
	}
	return !hasOther
}

// isSelfAuthoredMaintainerPR reports whether item is a PR authored by
// the running user. It is only consulted for maintainer-role syncs
// (PreserveExistingItemRole == false); contributor sweep PRs and issues
// authored by the user are handled separately.
func isSelfAuthoredMaintainerPR(item ghclient.Item, selfLogin string) bool {
	if selfLogin == "" {
		return false
	}
	if item.Kind != sharedtypes.ItemKindPR {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.Author), selfLogin)
}

func hasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func timePtr(value time.Time) *time.Time {
	return &value
}
