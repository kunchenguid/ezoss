package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type triageLister interface {
	ListNeedingTriage(ctx context.Context, repo string) ([]ghclient.Item, error)
	ListTriaged(ctx context.Context, repo string, sinceUpdated time.Time) ([]ghclient.Item, error)
}

type triageRunner interface {
	Triage(ctx context.Context, req TriageRequest) (*TriageResult, error)
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

type Poller struct {
	DB                 *db.DB
	GitHub             triageLister
	Triage             triageRunner
	AgentsInstructions string
	StaleThreshold     time.Duration
	// IgnoreOlderThan skips items whose last update is older than this.
	// Zero disables the filter (every item is processed).
	IgnoreOlderThan time.Duration
	// Hooks observes per-repo and per-item progress. Optional; zero
	// value disables.
	Hooks PollHooks
	// Logger receives non-fatal per-item triage failures. Optional; nil
	// drops the messages.
	Logger func(format string, args ...any)
	// PerItemTriageTimeout caps a single triage invocation. Zero means
	// defaultPerItemTriageTimeout (30m).
	PerItemTriageTimeout time.Duration
}

func (p Poller) logf(format string, args ...any) {
	if p.Logger == nil {
		return
	}
	p.Logger(format, args...)
}

// PollOnce runs a full poll cycle in two sequential stages: stage A
// fetches GitHub data for every configured repo into the local DB,
// stage B then iterates items in the DB that need agent attention and
// invokes the triage runner. Both stages are sequential to avoid
// hitting GitHub or agent provider rate limits.
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
	if err := runAgentsStage(ctx, poller, repos, polledAt); err != nil {
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
		err := syncRepoData(ctx, poller, repoID, polledAt)
		if poller.Hooks.OnRepoEnd != nil {
			poller.Hooks.OnRepoEnd(repoID, err)
		}
		if err != nil {
			errs = append(errs, err)
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

	if err := poller.DB.UpsertRepo(db.Repo{ID: repoID, LastPollAt: &polledAt}); err != nil {
		return fmt.Errorf("poll repo %s: upsert repo: %w", repoID, err)
	}

	for _, item := range items {
		if isOlderThan(item.UpdatedAt, polledAt, poller.IgnoreOlderThan) {
			continue
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
			GHTriaged:   hasLabel(item.Labels, triagedLabel),
			WaitingOn:   sharedtypes.WaitingOnNone,
			LastEventAt: timePtr(item.UpdatedAt.UTC()),
		}
		existing, err := poller.DB.GetItem(itemRecord.ID)
		if err != nil {
			return fmt.Errorf("poll repo %s: get item %d: %w", repoID, item.Number, err)
		}
		if existing != nil {
			itemRecord.WaitingOn = existing.WaitingOn
			itemRecord.StaleSince = existing.StaleSince
		}
		if err := poller.DB.UpsertItem(itemRecord); err != nil {
			return fmt.Errorf("poll repo %s: upsert item %d: %w", repoID, item.Number, err)
		}
	}

	if shouldRefreshTriagedItems(poller, repoID, polledAt) {
		if err := refreshTriagedItems(ctx, poller, repoID, polledAt); err != nil {
			return fmt.Errorf("poll repo %s: refresh triaged items: %w", repoID, err)
		}
	}

	return nil
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
	err := runTriageForItem(ctx, poller, item, polledAt)
	if poller.Hooks.OnAgentItemEnd != nil {
		poller.Hooks.OnAgentItemEnd(itemID, err)
	}
	if err != nil {
		poller.logf("triage item %s failed: %v", itemID, err)
	}
}

func runTriageForItem(ctx context.Context, poller Poller, item db.Item, polledAt time.Time) error {
	timeout := poller.PerItemTriageTimeout
	if timeout <= 0 {
		timeout = defaultPerItemTriageTimeout
	}
	itemCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ghItem := dbItemToGHItem(item)
	result, err := poller.Triage.Triage(itemCtx, TriageRequest{
		Item:   ghItem,
		Prompt: triage.Prompt(ghItem.URL, poller.AgentsInstructions),
		Schema: triage.Schema(),
	})
	if err != nil {
		return err
	}
	if result == nil || result.Recommendation == nil {
		return errors.New("empty recommendation")
	}

	if err := supersedeActiveRecommendationsForItem(poller.DB, item.ID, polledAt); err != nil {
		return fmt.Errorf("supersede recommendations: %w", err)
	}

	options := make([]db.NewRecommendationOption, 0, len(result.Recommendation.Options))
	for _, opt := range result.Recommendation.Options {
		options = append(options, db.NewRecommendationOption{
			StateChange:  opt.StateChange,
			Rationale:    opt.Rationale,
			DraftComment: opt.DraftComment,
			Followups:    opt.Followups,
			Confidence:   opt.Confidence,
			WaitingOn:    opt.WaitingOn,
		})
	}
	if _, err := poller.DB.InsertRecommendation(db.NewRecommendation{
		ItemID:    item.ID,
		Agent:     result.Agent,
		Model:     result.Model,
		TokensIn:  result.TokensIn,
		TokensOut: result.TokensOut,
		Options:   options,
	}); err != nil {
		return fmt.Errorf("insert recommendation: %w", err)
	}

	item.WaitingOn = result.Recommendation.Options[0].WaitingOn
	if err := poller.DB.UpsertItem(item); err != nil {
		return fmt.Errorf("update waiting_on: %w", err)
	}
	return nil
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

	for _, item := range items {
		cached, err := poller.DB.GetItem(itemID(repoID, item.Number))
		if err != nil {
			return fmt.Errorf("get item %d: %w", item.Number, err)
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
			GHTriaged:   hasLabel(item.Labels, triagedLabel),
			WaitingOn:   sharedtypes.WaitingOnNone,
			LastEventAt: timePtr(item.UpdatedAt.UTC()),
		}
		if cached != nil {
			itemRecord.WaitingOn = cached.WaitingOn
			itemRecord.StaleSince = cached.StaleSince
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
				ProposedLabels: []string{"stale"},
				Confidence:     sharedtypes.ConfidenceMedium,
				WaitingOn:      sharedtypes.WaitingOnContributor,
			}},
		}); err != nil {
			return fmt.Errorf("insert stale recommendation for item %d: %w", item.Number, err)
		}
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
