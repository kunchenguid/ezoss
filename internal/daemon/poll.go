package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

type itemGetter interface {
	GetItem(ctx context.Context, repo string, kind sharedtypes.ItemKind, number int) (ghclient.Item, error)
}

type triageRunner interface {
	Triage(ctx context.Context, req TriageRequest) (*TriageResult, error)
}

type fixRunner interface {
	RunFix(ctx context.Context, job db.FixJob, progress func(db.FixJobUpdate) error) (*FixResult, error)
	DetectPR(ctx context.Context, job db.FixJob) (string, error)
}

type FixResult struct {
	Branch       string
	WorktreePath string
	PRURL        string
	WaitingForPR bool
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
}

func (p Poller) log() *slog.Logger {
	if p.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Logger
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

	if err := poller.DB.UpsertRepo(db.Repo{ID: repoID, LastPollAt: &polledAt}); err != nil {
		return fmt.Errorf("poll repo %s: upsert repo: %w", repoID, err)
	}

	seenOpenUntriaged := make(map[int]struct{}, len(items))
	for _, item := range items {
		if isOlderThan(item.UpdatedAt, polledAt, poller.IgnoreOlderThan) {
			continue
		}
		seenOpenUntriaged[item.Number] = struct{}{}
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
		itemRecord := db.Item{
			ID:          cached.ID,
			RepoID:      repoID,
			Kind:        current.Kind,
			Number:      current.Number,
			Title:       current.Title,
			Author:      current.Author,
			State:       current.State,
			IsDraft:     current.IsDraft,
			GHTriaged:   hasLabel(current.Labels, triagedLabel),
			WaitingOn:   cached.WaitingOn,
			LastEventAt: timePtr(current.UpdatedAt.UTC()),
			StaleSince:  cached.StaleSince,
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
	result, err := poller.Triage.Triage(itemCtx, TriageRequest{
		Item:   ghItem,
		Prompt: triage.PromptWithRerunInstructions(ghItem.URL, poller.AgentsInstructions, poller.RerunInstructions),
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
