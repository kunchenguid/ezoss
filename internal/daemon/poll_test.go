package daemon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/triage"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestPollOnceUpsertsRepoAndItems(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "panic in sync loop",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: time.Unix(1713511200, 0).UTC(),
				},
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindPR,
					Number:    7,
					Title:     "feat: add sync status",
					Author:    "bob",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/pull/7",
					UpdatedAt: time.Unix(1713514800, 0).UTC(),
				},
			},
		},
	}

	err := PollOnce(context.Background(), Poller{DB: database, GitHub: client}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if !reflect.DeepEqual(client.calls, []string{"acme/widgets"}) {
		t.Fatalf("poll calls = %#v, want single repo poll", client.calls)
	}

	repo, err := database.GetRepo("acme/widgets")
	if err != nil {
		t.Fatalf("GetRepo() error = %v", err)
	}
	if repo == nil {
		t.Fatal("expected repo to be created")
	}
	if repo.LastPollAt == nil {
		t.Fatal("expected repo last_poll_at to be set")
	}

	issue, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem(issue) error = %v", err)
	}
	if issue == nil {
		t.Fatal("expected issue to be stored")
	}
	if issue.RepoID != "acme/widgets" || issue.Kind != sharedtypes.ItemKindIssue || issue.Number != 42 {
		t.Fatalf("unexpected issue item = %#v", issue)
	}
	if issue.LastEventAt == nil || !issue.LastEventAt.Equal(time.Unix(1713511200, 0).UTC()) {
		t.Fatalf("issue LastEventAt = %v, want %v", issue.LastEventAt, time.Unix(1713511200, 0).UTC())
	}
	if issue.GHTriaged {
		t.Fatal("expected stored item to remain untriaged")
	}

	pr, err := database.GetItem("acme/widgets#7")
	if err != nil {
		t.Fatalf("GetItem(pr) error = %v", err)
	}
	if pr == nil {
		t.Fatal("expected pr to be stored")
	}
	if pr.Kind != sharedtypes.ItemKindPR || pr.Number != 7 {
		t.Fatalf("unexpected pr item = %#v", pr)
	}
}

func TestPollOnceReturnsRepoContextOnListFailure(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{},
		errByRepo: map[string]error{
			"acme/widgets": errors.New("gh failed"),
		},
	}

	err := PollOnce(context.Background(), Poller{DB: database, GitHub: client}, []string{"acme/widgets"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "poll repo acme/widgets: list needing triage: gh failed" {
		t.Fatalf("unexpected error %q", got)
	}
	if repo, err := database.GetRepo("acme/widgets"); err != nil {
		t.Fatalf("GetRepo() error = %v", err)
	} else if repo != nil {
		t.Fatalf("expected repo not to be upserted on failed poll, got %#v", repo)
	}
}

func TestPollOnceContinuesOtherReposAfterListFailure(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/healthy": {
				{
					Repo:      "acme/healthy",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    7,
					Title:     "healthy repo item",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/healthy/issues/7",
					UpdatedAt: time.Unix(1713511200, 0).UTC(),
				},
			},
		},
		errByRepo: map[string]error{
			"acme/broken": errors.New("gh failed"),
		},
	}

	err := PollOnce(context.Background(), Poller{DB: database, GitHub: client}, []string{"acme/broken", "acme/healthy"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "poll repo acme/broken: list needing triage: gh failed" {
		t.Fatalf("unexpected error %q", got)
	}
	if !reflect.DeepEqual(client.calls, []string{"acme/broken", "acme/healthy"}) {
		t.Fatalf("poll calls = %#v, want both repos polled", client.calls)
	}

	repo, err := database.GetRepo("acme/healthy")
	if err != nil {
		t.Fatalf("GetRepo(acme/healthy) error = %v", err)
	}
	if repo == nil {
		t.Fatal("expected healthy repo to be upserted despite earlier repo failure")
	}

	item, err := database.GetItem("acme/healthy#7")
	if err != nil {
		t.Fatalf("GetItem(acme/healthy#7) error = %v", err)
	}
	if item == nil {
		t.Fatal("expected healthy repo item to be stored despite earlier repo failure")
	}
}

func TestPollOnceStoresRecommendationFromTriageRunner(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "panic in sync loop",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: time.Unix(1713511200, 0).UTC(),
				},
			},
		},
	}
	runner := &stubRecommendationRunner{
		result: &TriageResult{
			Agent: sharedtypes.AgentCodex,
			Model: "gpt-5.4",
			Recommendation: &triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "Needs a repro before investigation.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Please share a minimal repro.",
					Confidence:   sharedtypes.ConfidenceMedium,
				}},
			},
			TokensIn:  1200,
			TokensOut: 180,
		},
	}

	err := PollOnce(context.Background(), Poller{
		DB:                 database,
		GitHub:             client,
		Triage:             runner,
		AgentsInstructions: "Always ask for a minimal repro before labeling as bug.",
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("triage runner calls = %d, want 1", len(runner.calls))
	}
	if runner.calls[0].Item.URL != "https://github.com/acme/widgets/issues/42" {
		t.Fatalf("triage item URL = %q", runner.calls[0].Item.URL)
	}
	if !strings.Contains(runner.calls[0].Prompt, "https://github.com/acme/widgets/issues/42") {
		t.Fatalf("prompt missing item URL: %q", runner.calls[0].Prompt)
	}
	if !strings.Contains(runner.calls[0].Prompt, "Always ask for a minimal repro") {
		t.Fatalf("prompt missing AGENTS instructions: %q", runner.calls[0].Prompt)
	}
	if string(runner.calls[0].Schema) != string(triage.Schema()) {
		t.Fatalf("schema mismatch: got %s", string(runner.calls[0].Schema))
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("active recommendations = %d, want 1", len(recommendations))
	}
	rec := recommendations[0]
	if rec.ItemID != "acme/widgets#42" || rec.Agent != sharedtypes.AgentCodex || rec.Model != "gpt-5.4" {
		t.Fatalf("unexpected recommendation identity = %#v", rec)
	}
	if len(rec.Options) != 1 {
		t.Fatalf("len(Options) = %d, want 1", len(rec.Options))
	}
	opt := rec.Options[0]
	if opt.StateChange != sharedtypes.StateChangeNone || opt.Confidence != sharedtypes.ConfidenceMedium {
		t.Fatalf("unexpected option action/confidence = %#v", opt)
	}
	if len(opt.ProposedLabels) != 0 {
		t.Fatalf("labels = %#v, want empty - agent no longer proposes user labels", opt.ProposedLabels)
	}
	if rec.TokensIn != 1200 || rec.TokensOut != 180 {
		t.Fatalf("unexpected token usage = in:%d out:%d", rec.TokensIn, rec.TokensOut)
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected item to remain stored")
	}
	if item.WaitingOn != sharedtypes.WaitingOnContributor {
		t.Fatalf("item waiting_on = %q, want contributor", item.WaitingOn)
	}
}

func TestPollOnceSupersedesExistingRecommendationWhenItemHasNewActivity(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "panic in sync loop",
		Author:    "alice",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnNone,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	oldRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Model:  "sonnet",
		Options: []db.NewRecommendationOption{{
			StateChange:    sharedtypes.StateChangeNone,
			Rationale:      "Old rationale",
			DraftComment:   "Old draft",
			ProposedLabels: []string{"needs-info"},
			Confidence:     sharedtypes.ConfidenceLow,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	// Item updated on GitHub _after_ the existing recommendation - the
	// dedup check should re-triage and supersede the old rec.
	freshActivityAt := time.Now().UTC().Add(1 * time.Hour).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "panic in sync loop",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: freshActivityAt,
				},
			},
		},
	}
	runner := &stubRecommendationRunner{
		result: &TriageResult{
			Agent: sharedtypes.AgentCodex,
			Model: "gpt-5.4",
			Recommendation: &triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "Re-triaged after more context.",
					WaitingOn:    sharedtypes.WaitingOnMaintainer,
					DraftComment: "",
					Confidence:   sharedtypes.ConfidenceHigh,
				}},
			},
		},
	}

	err = PollOnce(context.Background(), Poller{DB: database, GitHub: client, Triage: runner}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	updatedOldRec, err := database.GetRecommendation(oldRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation(old) error = %v", err)
	}
	if updatedOldRec == nil || updatedOldRec.SupersededAt == nil {
		t.Fatalf("expected old recommendation to be superseded, got %#v", updatedOldRec)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("active recommendations = %d, want 1", len(recommendations))
	}
	if recommendations[0].ID == oldRec.ID {
		t.Fatal("expected a newly inserted active recommendation")
	}
	if len(recommendations[0].Options) != 1 || recommendations[0].Options[0].Rationale != "Re-triaged after more context." {
		t.Fatalf("unexpected new options %#v", recommendations[0].Options)
	}
}

func TestPollOnceSkipsTriageWhenActiveRecommendationIsFresh(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "panic in sync loop",
		Author:    "alice",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnNone,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	oldRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Model:  "sonnet",
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Rationale:   "Existing rationale",
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	// GitHub item updated _before_ the recommendation was made - dedup
	// should treat the existing rec as still relevant and skip triage.
	staleActivityAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "panic in sync loop",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: staleActivityAt,
				},
			},
		},
	}
	runner := &stubRecommendationRunner{
		err: errors.New("triage runner should not be invoked"),
	}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: client, Triage: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if len(runner.calls) != 0 {
		t.Fatalf("triage calls = %d, want 0 (existing rec is fresh)", len(runner.calls))
	}

	stillActive, err := database.GetRecommendation(oldRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation() error = %v", err)
	}
	if stillActive == nil || stillActive.SupersededAt != nil {
		t.Fatalf("expected old recommendation to remain active, got %#v", stillActive)
	}
}

func TestPollOnceSurfacesStaleContributorItem(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	lastEventAt := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Need more logs",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &lastEventAt,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
	}

	err := PollOnce(context.Background(), Poller{
		DB:             database,
		GitHub:         client,
		StaleThreshold: 48 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("active recommendations = %d, want 1", len(recommendations))
	}
	if recommendations[0].ItemID != "acme/widgets#42" {
		t.Fatalf("recommendation item_id = %q, want %q", recommendations[0].ItemID, "acme/widgets#42")
	}
	if len(recommendations[0].Options) == 0 {
		t.Fatal("expected stale recommendation to have an option")
	}
	primary := recommendations[0].Options[0]
	if primary.StateChange != sharedtypes.StateChangeClose {
		t.Fatalf("state_change = %q, want %q", primary.StateChange, sharedtypes.StateChangeClose)
	}
	if primary.DraftComment == "" {
		t.Fatal("expected stale recommendation to include a close comment")
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || item.StaleSince == nil {
		t.Fatalf("stale item = %#v, want stale_since to be set", item)
	}
	wantStaleSince := lastEventAt.Add(48 * time.Hour)
	if !item.StaleSince.Equal(wantStaleSince) {
		t.Fatalf("stale_since = %v, want %v", item.StaleSince, wantStaleSince)
	}
}

// TestPollOnceReconcilesLocalStateForOutOfBandClose covers the case where an
// item gets closed on GitHub outside of ezoss (manual close in the UI,
// auto-close from a linked PR merge, etc). The triaged-refresh path must
// reconcile the local row's state from the GitHub view so the stale detector
// doesn't keep firing on a stale state=open record. Also asserts the delta
// query passes the prior LastTriagedRefreshAt as the since timestamp so we
// don't fetch the entire closed-history.
func TestPollOnceReconcilesLocalStateForOutOfBandClose(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	previousRefresh := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", LastTriagedRefreshAt: &previousRefresh}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	oldEvent := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Closed on GitHub directly",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &oldEvent,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	closedAt := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
		triagedItemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "Closed on GitHub directly",
					Author:    "alice",
					State:     sharedtypes.ItemStateClosed,
					Labels:    []string{"ezoss/triaged"},
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: closedAt,
				},
			},
		},
	}

	err := PollOnce(context.Background(), Poller{
		DB:             database,
		GitHub:         client,
		StaleThreshold: 48 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if len(client.triagedSinceByCall) != 1 {
		t.Fatalf("triaged since calls = %d, want 1", len(client.triagedSinceByCall))
	}
	if !client.triagedSinceByCall[0].Equal(previousRefresh) {
		t.Fatalf("triaged since = %v, want delta-bounded to last refresh %v", client.triagedSinceByCall[0], previousRefresh)
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected item to remain after refresh")
	}
	if item.State != sharedtypes.ItemStateClosed {
		t.Fatalf("local state after out-of-band close = %q, want %q", item.State, sharedtypes.ItemStateClosed)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 0 {
		t.Fatalf("active recommendations after reconciliation = %d, want 0", len(recommendations))
	}
}

// TestPollOnceSkipsStaleRecommendationForClosedContributorItem covers the
// regression where an item closed via approval (local state=closed,
// gh_triaged=true, waiting_on=contributor) would otherwise re-trigger the
// stale detector. With local state=closed we must not surface a new stale
// recommendation, even though the item still has waiting_on=contributor and
// gh_triaged=true left over from before the close.
func TestPollOnceSkipsStaleRecommendationForClosedContributorItem(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	lastEventAt := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Closed via approval",
		Author:      "alice",
		State:       sharedtypes.ItemStateClosed,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &lastEventAt,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
	}

	err := PollOnce(context.Background(), Poller{
		DB:             database,
		GitHub:         client,
		StaleThreshold: 48 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 0 {
		t.Fatalf("active recommendations = %d, want 0 for closed item", len(recommendations))
	}
}

func TestPollOnceRefreshesTriagedContributorItemBeforeStaleCheck(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	oldLastEventAt := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	oldStaleSince := oldLastEventAt.Add(48 * time.Hour)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Need more logs",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &oldLastEventAt,
		StaleSince:  &oldStaleSince,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	refreshedLastEventAt := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
		triagedItemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "Need more logs",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					Labels:    []string{"ezoss/triaged"},
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: refreshedLastEventAt,
				},
			},
		},
	}

	err := PollOnce(context.Background(), Poller{
		DB:             database,
		GitHub:         client,
		StaleThreshold: 48 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if !reflect.DeepEqual(client.calls, []string{"acme/widgets"}) {
		t.Fatalf("needing-triage calls = %#v, want single repo poll", client.calls)
	}
	if !reflect.DeepEqual(client.triagedCalls, []string{"acme/widgets"}) {
		t.Fatalf("triaged calls = %#v, want single repo refresh", client.triagedCalls)
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected refreshed item to remain stored")
	}
	if item.LastEventAt == nil || !item.LastEventAt.Equal(refreshedLastEventAt) {
		t.Fatalf("last_event_at = %v, want %v", item.LastEventAt, refreshedLastEventAt)
	}
	if item.StaleSince != nil {
		t.Fatalf("stale_since = %v, want cleared after fresh activity", item.StaleSince)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(recommendations))
	}
}

func TestPollOnceSupersedesStaleRecommendationAfterFreshContributorActivity(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	oldLastEventAt := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	oldStaleSince := oldLastEventAt.Add(48 * time.Hour)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Need more logs",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &oldLastEventAt,
		StaleSince:  &oldStaleSince,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	staleRecommendation, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentAuto,
		Model:  "stale-detector",
		Options: []db.NewRecommendationOption{{
			StateChange:    sharedtypes.StateChangeClose,
			Rationale:      "The contributor has been inactive past the configured stale threshold.",
			DraftComment:   "Closing as stale for now. Feel free to reopen with more detail.",
			ProposedLabels: []string{"stale"},
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	refreshedLastEventAt := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
		triagedItemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "Need more logs",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					Labels:    []string{"ezoss/triaged"},
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: refreshedLastEventAt,
				},
			},
		},
	}

	err = PollOnce(context.Background(), Poller{
		DB:             database,
		GitHub:         client,
		StaleThreshold: 48 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(recommendations))
	}

	updatedStaleRecommendation, err := database.GetRecommendation(staleRecommendation.ID)
	if err != nil {
		t.Fatalf("GetRecommendation() error = %v", err)
	}
	if updatedStaleRecommendation == nil || updatedStaleRecommendation.SupersededAt == nil {
		t.Fatalf("stale recommendation = %#v, want superseded recommendation", updatedStaleRecommendation)
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected refreshed item to remain stored")
	}
	if item.StaleSince != nil {
		t.Fatalf("stale_since = %v, want cleared after fresh activity", item.StaleSince)
	}
}

func TestPollOnceSkipsTriagedRefreshUntilRefreshIntervalElapsed(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	lastPollAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	lastTriagedRefreshAt := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	oldLastEventAt := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	if err := database.UpsertRepo(db.Repo{
		ID:                   "acme/widgets",
		LastPollAt:           &lastPollAt,
		LastTriagedRefreshAt: &lastTriagedRefreshAt,
	}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Need more logs",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &oldLastEventAt,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	refreshedLastEventAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
		triagedItemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    42,
					Title:     "Need more logs",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					Labels:    []string{"ezoss/triaged"},
					URL:       "https://github.com/acme/widgets/issues/42",
					UpdatedAt: refreshedLastEventAt,
				},
			},
		},
	}

	err := PollOnce(context.Background(), Poller{
		DB:             database,
		GitHub:         client,
		StaleThreshold: 48 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if !reflect.DeepEqual(client.calls, []string{"acme/widgets"}) {
		t.Fatalf("needing-triage calls = %#v, want single repo poll", client.calls)
	}
	if len(client.triagedCalls) != 0 {
		t.Fatalf("triaged calls = %#v, want none before refresh interval", client.triagedCalls)
	}

	repo, err := database.GetRepo("acme/widgets")
	if err != nil {
		t.Fatalf("GetRepo() error = %v", err)
	}
	if repo == nil {
		t.Fatal("expected repo to remain stored")
	}
	if repo.LastTriagedRefreshAt == nil || !repo.LastTriagedRefreshAt.Equal(lastTriagedRefreshAt) {
		t.Fatalf("last_triaged_refresh_at = %v, want unchanged %v", repo.LastTriagedRefreshAt, lastTriagedRefreshAt)
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("expected cached item to remain stored")
	}
	if item.LastEventAt == nil || !item.LastEventAt.Equal(oldLastEventAt) {
		t.Fatalf("last_event_at = %v, want unchanged %v", item.LastEventAt, oldLastEventAt)
	}
	if item.StaleSince == nil {
		t.Fatal("expected stale_since to be set from cached stale check")
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("active recommendations = %d, want 1 stale recommendation", len(recommendations))
	}
}

func TestPollOnceFiresPerRepoHooksAroundEachPoll(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {{
				Repo:      "acme/widgets",
				Kind:      sharedtypes.ItemKindIssue,
				Number:    1,
				Title:     "x",
				State:     sharedtypes.ItemStateOpen,
				URL:       "https://github.com/acme/widgets/issues/1",
				UpdatedAt: time.Unix(1713511200, 0).UTC(),
			}},
			"acme/healthy": nil,
		},
		errByRepo: map[string]error{
			"acme/broken": errors.New("rate limited"),
		},
	}

	type beginEvent struct {
		repo  string
		idx   int
		total int
	}
	type endEvent struct {
		repo string
		err  string
	}
	var beginCalls []beginEvent
	var endCalls []endEvent

	hooks := PollHooks{
		OnRepoBegin: func(repo string, idx, total int) {
			beginCalls = append(beginCalls, beginEvent{repo: repo, idx: idx, total: total})
		},
		OnRepoEnd: func(repo string, err error) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			endCalls = append(endCalls, endEvent{repo: repo, err: msg})
		},
	}

	repos := []string{"acme/widgets", "acme/broken", "acme/healthy"}
	err := PollOnce(context.Background(), Poller{DB: database, GitHub: client, Hooks: hooks}, repos)
	if err == nil {
		t.Fatal("expected aggregated error from broken repo")
	}

	wantBegin := []beginEvent{
		{"acme/widgets", 0, 3},
		{"acme/broken", 1, 3},
		{"acme/healthy", 2, 3},
	}
	if !reflect.DeepEqual(beginCalls, wantBegin) {
		t.Fatalf("OnRepoBegin calls = %#v, want %#v", beginCalls, wantBegin)
	}

	if len(endCalls) != 3 {
		t.Fatalf("OnRepoEnd calls = %d, want 3", len(endCalls))
	}
	if endCalls[0].err != "" {
		t.Fatalf("OnRepoEnd[0].err = %q, want empty", endCalls[0].err)
	}
	if !strings.Contains(endCalls[1].err, "rate limited") {
		t.Fatalf("OnRepoEnd[1].err = %q, want contains rate limited", endCalls[1].err)
	}
	if endCalls[2].err != "" {
		t.Fatalf("OnRepoEnd[2].err = %q, want empty", endCalls[2].err)
	}
}

func TestPollOnceTimesOutSlowTriageAndContinues(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    1,
					Title:     "first item triage will hang",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/1",
					UpdatedAt: time.Unix(1713511200, 0).UTC(),
				},
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    2,
					Title:     "second item triage succeeds",
					Author:    "bob",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/2",
					UpdatedAt: time.Unix(1713511500, 0).UTC(),
				},
			},
		},
	}
	successResult := &TriageResult{
		Agent: sharedtypes.AgentClaude,
		Model: "sonnet",
		Recommendation: &triage.Recommendation{
			Options: []triage.RecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Rationale:   "ok",
				WaitingOn:   sharedtypes.WaitingOnContributor,
				Confidence:  sharedtypes.ConfidenceMedium,
			}},
		},
	}
	hangingRunner := &hangingTriageRunner{
		successFor: func(item ghclient.Item) bool { return item.Number == 2 },
		result:     successResult,
	}

	var logged []string
	poller := Poller{
		DB:                   database,
		GitHub:               client,
		Triage:               hangingRunner,
		PerItemTriageTimeout: 50 * time.Millisecond,
		Logger: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	}

	if err := PollOnce(context.Background(), poller, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if hangingRunner.calls != 2 {
		t.Fatalf("triage calls = %d, want 2 (hanging item plus successful item)", hangingRunner.calls)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("active recommendations = %d, want 1 (only successful item)", len(recommendations))
	}
	if recommendations[0].ItemID != "acme/widgets#2" {
		t.Fatalf("recommendation item_id = %q, want acme/widgets#2", recommendations[0].ItemID)
	}

	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "deadline") && !strings.Contains(joined, "context deadline") {
		t.Fatalf("logger output should mention deadline/timeout: %s", joined)
	}
}

func TestPollOnceContinuesAfterPerItemTriageFailure(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    1,
					Title:     "first item triage will fail",
					Author:    "alice",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/1",
					UpdatedAt: time.Unix(1713511200, 0).UTC(),
				},
				{
					Repo:      "acme/widgets",
					Kind:      sharedtypes.ItemKindIssue,
					Number:    2,
					Title:     "second item triage succeeds",
					Author:    "bob",
					State:     sharedtypes.ItemStateOpen,
					URL:       "https://github.com/acme/widgets/issues/2",
					UpdatedAt: time.Unix(1713511500, 0).UTC(),
				},
			},
		},
	}
	successResult := &TriageResult{
		Agent: sharedtypes.AgentClaude,
		Model: "sonnet",
		Recommendation: &triage.Recommendation{
			Options: []triage.RecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Rationale:   "needs follow-up",
				WaitingOn:   sharedtypes.WaitingOnContributor,
				Confidence:  sharedtypes.ConfidenceMedium,
			}},
		},
	}
	runner := &stubRecommendationRunner{
		resultFor: func(item ghclient.Item) (*TriageResult, error) {
			if item.Number == 1 {
				return nil, errors.New("claude exited: exit status 1")
			}
			return successResult, nil
		},
	}

	var logged []string
	poller := Poller{
		DB:     database,
		GitHub: client,
		Triage: runner,
		Logger: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	}

	if err := PollOnce(context.Background(), poller, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v, want nil after non-fatal per-item triage failure", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("triage calls = %d, want 2 (failed item plus successful item)", len(runner.calls))
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("active recommendations = %d, want 1 (only successful item)", len(recommendations))
	}
	if recommendations[0].ItemID != "acme/widgets#2" {
		t.Fatalf("recommendation item_id = %q, want acme/widgets#2", recommendations[0].ItemID)
	}

	if len(logged) == 0 {
		t.Fatal("expected logger to receive a message for the failed item")
	}
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "acme/widgets") || !strings.Contains(joined, "1") {
		t.Fatalf("logger output missing repo/item context: %s", joined)
	}
	if !strings.Contains(joined, "claude exited: exit status 1") {
		t.Fatalf("logger output missing underlying error: %s", joined)
	}
}

type stubTriageClient struct {
	itemsByRepo        map[string][]ghclient.Item
	triagedItemsByRepo map[string][]ghclient.Item
	errByRepo          map[string]error
	triagedErrByRepo   map[string]error
	calls              []string
	triagedCalls       []string
	triagedSinceByCall []time.Time
}

type stubRecommendationRunner struct {
	result    *TriageResult
	err       error
	resultFor func(item ghclient.Item) (*TriageResult, error)
	calls     []TriageRequest
}

// hangingTriageRunner blocks until the per-item context expires for any
// item that is not flagged as fast via successFor. Used to verify the
// daemon's per-item timeout escape hatch.
type hangingTriageRunner struct {
	successFor func(ghclient.Item) bool
	result     *TriageResult
	calls      int
}

func (h *hangingTriageRunner) Triage(ctx context.Context, req TriageRequest) (*TriageResult, error) {
	h.calls++
	if h.successFor != nil && h.successFor(req.Item) {
		return h.result, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *stubTriageClient) ListNeedingTriage(_ context.Context, repo string) ([]ghclient.Item, error) {
	s.calls = append(s.calls, repo)
	if err := s.errByRepo[repo]; err != nil {
		return nil, err
	}
	return append([]ghclient.Item(nil), s.itemsByRepo[repo]...), nil
}

func (s *stubTriageClient) ListTriaged(_ context.Context, repo string, sinceUpdated time.Time) ([]ghclient.Item, error) {
	s.triagedCalls = append(s.triagedCalls, repo)
	s.triagedSinceByCall = append(s.triagedSinceByCall, sinceUpdated)
	if err := s.triagedErrByRepo[repo]; err != nil {
		return nil, err
	}
	return append([]ghclient.Item(nil), s.triagedItemsByRepo[repo]...), nil
}

func (s *stubRecommendationRunner) Triage(_ context.Context, req TriageRequest) (*TriageResult, error) {
	s.calls = append(s.calls, req)
	if s.resultFor != nil {
		return s.resultFor(req.Item)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()

	database, err := db.Open(t.TempDir() + "/test.sqlite")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close test db: %v", err)
		}
	})
	return database
}

func TestSyncSkipsItemsOlderThanIgnoreThreshold(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	now := time.Now().UTC()
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {
				{
					Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 1,
					Title: "fresh", State: sharedtypes.ItemStateOpen,
					UpdatedAt: now.Add(-30 * 24 * time.Hour),
				},
				{
					Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 2,
					Title: "ancient", State: sharedtypes.ItemStateOpen,
					UpdatedAt: now.Add(-400 * 24 * time.Hour),
				},
			},
		},
	}

	err := PollOnce(context.Background(), Poller{
		DB:              database,
		GitHub:          client,
		IgnoreOlderThan: 365 * 24 * time.Hour,
	}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	fresh, _ := database.GetItem("acme/widgets#1")
	if fresh == nil {
		t.Fatal("expected fresh item to be stored")
	}
	ancient, _ := database.GetItem("acme/widgets#2")
	if ancient != nil {
		t.Fatalf("expected ancient item to be skipped, got %#v", ancient)
	}
}

func TestSyncIgnoreOlderThanZeroDisablesFilter(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	now := time.Now().UTC()
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {{
				Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 99,
				Title: "very old", State: sharedtypes.ItemStateOpen,
				UpdatedAt: now.Add(-10 * 365 * 24 * time.Hour),
			}},
		},
	}

	err := PollOnce(context.Background(), Poller{DB: database, GitHub: client}, []string{"acme/widgets"})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	got, _ := database.GetItem("acme/widgets#99")
	if got == nil {
		t.Fatal("threshold=0 should keep ancient items")
	}
}

func TestAgentsStageSkipsAncientPendingItems(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	fresh := time.Time(now.Add(-10 * 24 * time.Hour))
	ancient := time.Time(now.Add(-400 * 24 * time.Hour))
	items := []db.Item{
		{ID: "acme/widgets#1", RepoID: "acme/widgets", LastEventAt: &fresh},
		{ID: "acme/widgets#2", RepoID: "acme/widgets", LastEventAt: &ancient},
		{ID: "acme/widgets#3", RepoID: "acme/widgets", LastEventAt: nil},
	}

	got := filterItemsByAge(items, now, 365*24*time.Hour)
	if len(got) != 2 {
		t.Fatalf("expected 2 items kept, got %d: %#v", len(got), got)
	}
	for _, it := range got {
		if it.ID == "acme/widgets#2" {
			t.Fatalf("ancient item should have been filtered out")
		}
	}
}

func TestFilterItemsByAgeZeroThresholdIsNoop(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	old := time.Time(now.Add(-10 * 365 * 24 * time.Hour))
	items := []db.Item{{ID: "x", LastEventAt: &old}}

	got := filterItemsByAge(items, now, 0)
	if len(got) != 1 {
		t.Fatalf("threshold=0 should keep all items, got %d", len(got))
	}
}
