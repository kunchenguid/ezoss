package daemon

import (
	"bytes"
	"context"
	"errors"
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

func TestSyncRepoDataPreservesContributorMetadata(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "upstream/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:           "upstream/widgets#12",
		RepoID:       "upstream/widgets",
		Kind:         sharedtypes.ItemKindPR,
		Number:       12,
		Role:         sharedtypes.RoleContributor,
		HeadRepo:     "kun/widgets",
		HeadRef:      "fix-race",
		HeadCloneURL: "https://github.com/kun/widgets.git",
		State:        sharedtypes.ItemStateOpen,
		WaitingOn:    sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	client := &stubTriageClient{itemsByRepo: map[string][]ghclient.Item{
		"upstream/widgets": {{
			Repo:      "upstream/widgets",
			Kind:      sharedtypes.ItemKindPR,
			Number:    12,
			Title:     "Fix race",
			Author:    "kun",
			State:     sharedtypes.ItemStateOpen,
			UpdatedAt: time.Unix(1713511200, 0).UTC(),
		}},
	}}

	if err := syncRepoData(context.Background(), Poller{DB: database, GitHub: client}, "upstream/widgets", time.Now()); err != nil {
		t.Fatalf("syncRepoData() error = %v", err)
	}

	got, err := database.GetItem("upstream/widgets#12")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetItem() = nil")
	}
	if got.Role != sharedtypes.RoleContributor || got.HeadRepo != "kun/widgets" || got.HeadRef != "fix-race" || got.HeadCloneURL != "https://github.com/kun/widgets.git" {
		t.Fatalf("contributor metadata = role %q head %q/%q clone %q", got.Role, got.HeadRepo, got.HeadRef, got.HeadCloneURL)
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
	if item.WaitingOn != sharedtypes.WaitingOnNone {
		t.Fatalf("item waiting_on = %q, want none before recommendation approval", item.WaitingOn)
	}
}

func TestRunTriageForItemDoesNotSupersedeNewerActiveRecommendation(t *testing.T) {
	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	item := db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "panic in sync loop",
		Author:    "alice",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnNone,
	}
	if err := database.UpsertItem(item); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	staleRunStartedAt := time.Now().Add(-2 * time.Minute)
	newerRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Model:  "claude",
		Options: []db.NewRecommendationOption{{
			StateChange:  sharedtypes.StateChangeNone,
			Rationale:    "Guided rerun result that should remain active.",
			DraftComment: "Guided response.",
			Confidence:   sharedtypes.ConfidenceHigh,
			WaitingOn:    sharedtypes.WaitingOnContributor,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation(newer) error = %v", err)
	}
	if newerRec.CreatedAt <= staleRunStartedAt.Unix() {
		t.Fatalf("test setup invalid: newer rec created_at=%d, stale start=%d", newerRec.CreatedAt, staleRunStartedAt.Unix())
	}

	runner := &stubRecommendationRunner{result: &TriageResult{
		Agent: sharedtypes.AgentClaude,
		Model: "claude",
		Recommendation: &triage.Recommendation{Options: []triage.RecommendationOption{{
			StateChange:  sharedtypes.StateChangeNone,
			Rationale:    "Uninstructed daemon result from an older in-flight run.",
			DraftComment: "Plain response.",
			Confidence:   sharedtypes.ConfidenceMedium,
			WaitingOn:    sharedtypes.WaitingOnMaintainer,
		}}},
	}}
	if _, err := runTriageForItem(context.Background(), Poller{DB: database, Triage: runner}, item, staleRunStartedAt); err != nil {
		t.Fatalf("runTriageForItem() error = %v", err)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active recommendations = %d, want 1: %#v", len(active), active)
	}
	if active[0].ID != newerRec.ID {
		t.Fatalf("active recommendation id = %q, want newer %q", active[0].ID, newerRec.ID)
	}
	if active[0].Options[0].Rationale != "Guided rerun result that should remain active." {
		t.Fatalf("active rationale = %q", active[0].Options[0].Rationale)
	}
	storedNewer, err := database.GetRecommendation(newerRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation(newer) error = %v", err)
	}
	if storedNewer.SupersededAt != nil {
		t.Fatalf("newer recommendation was superseded at %v", storedNewer.SupersededAt)
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

func TestPollOnceSupersedesActiveRecommendationForMergedPR(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	oldEvent := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindPR,
		Number:      42,
		Title:       "feat: already merged",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   false,
		WaitingOn:   sharedtypes.WaitingOnMaintainer,
		LastEventAt: &oldEvent,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	oldRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Model:  "sonnet",
		Options: []db.NewRecommendationOption{{
			StateChange:  sharedtypes.StateChangeNone,
			Rationale:    "Needs maintainer review.",
			DraftComment: "",
			Confidence:   sharedtypes.ConfidenceHigh,
			WaitingOn:    sharedtypes.WaitingOnMaintainer,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	mergedAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
		itemByKey: map[string]ghclient.Item{
			"acme/widgets#42": {
				Repo:      "acme/widgets",
				Kind:      sharedtypes.ItemKindPR,
				Number:    42,
				Title:     "feat: already merged",
				Author:    "alice",
				State:     sharedtypes.ItemStateMerged,
				Labels:    nil,
				URL:       "https://github.com/acme/widgets/pull/42",
				UpdatedAt: mergedAt,
			},
		},
	}
	runner := &stubRecommendationRunner{
		result: &TriageResult{
			Agent: sharedtypes.AgentCodex,
			Model: "gpt-5.4",
			Recommendation: &triage.Recommendation{Options: []triage.RecommendationOption{{
				StateChange:  sharedtypes.StateChangeNone,
				Rationale:    "Should not be called for a merged PR.",
				DraftComment: "",
				Confidence:   sharedtypes.ConfidenceHigh,
			}}},
		},
	}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: client, Triage: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if len(runner.calls) != 0 {
		t.Fatalf("triage runner calls = %d, want 0 for merged PR", len(runner.calls))
	}
	updatedRec, err := database.GetRecommendation(oldRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation() error = %v", err)
	}
	if updatedRec == nil || updatedRec.SupersededAt == nil {
		t.Fatalf("expected active recommendation to be superseded, got %#v", updatedRec)
	}
	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0 for merged PR", len(active))
	}
	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || item.State != sharedtypes.ItemStateMerged {
		t.Fatalf("item state after reconciliation = %#v, want merged", item)
	}
}

func TestPollOnceSupersedesActiveRecommendationForExternallyTriagedItem(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	oldEvent := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#42",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "needs maintainer review",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   false,
		WaitingOn:   sharedtypes.WaitingOnMaintainer,
		LastEventAt: &oldEvent,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	oldRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Model:  "sonnet",
		Options: []db.NewRecommendationOption{{
			StateChange:  sharedtypes.StateChangeNone,
			Rationale:    "Needs maintainer review.",
			DraftComment: "",
			Confidence:   sharedtypes.ConfidenceHigh,
			WaitingOn:    sharedtypes.WaitingOnMaintainer,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	triagedAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{"acme/widgets": nil},
		itemByKey: map[string]ghclient.Item{
			"acme/widgets#42": {
				Repo:      "acme/widgets",
				Kind:      sharedtypes.ItemKindIssue,
				Number:    42,
				Title:     "needs maintainer review",
				Author:    "alice",
				State:     sharedtypes.ItemStateOpen,
				Labels:    []string{triagedLabel},
				URL:       "https://github.com/acme/widgets/issues/42",
				UpdatedAt: triagedAt,
			},
		},
	}
	runner := &stubRecommendationRunner{
		result: &TriageResult{
			Agent: sharedtypes.AgentCodex,
			Model: "gpt-5.4",
			Recommendation: &triage.Recommendation{Options: []triage.RecommendationOption{{
				StateChange:  sharedtypes.StateChangeNone,
				Rationale:    "Should not be called for an externally triaged item.",
				DraftComment: "",
				Confidence:   sharedtypes.ConfidenceHigh,
			}}},
		},
	}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: client, Triage: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	if len(runner.calls) != 0 {
		t.Fatalf("triage runner calls = %d, want 0 for externally triaged item", len(runner.calls))
	}
	updatedRec, err := database.GetRecommendation(oldRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation() error = %v", err)
	}
	if updatedRec == nil || updatedRec.SupersededAt == nil {
		t.Fatalf("expected active recommendation to be superseded, got %#v", updatedRec)
	}
	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0 for externally triaged item", len(active))
	}
	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item gh_triaged after reconciliation = %#v, want true", item)
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

	var logBuf bytes.Buffer
	poller := Poller{
		DB:                   database,
		GitHub:               client,
		Triage:               hangingRunner,
		PerItemTriageTimeout: 50 * time.Millisecond,
		Logger:               NewLogger(&logBuf),
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

	logOut := logBuf.String()
	if !strings.Contains(logOut, "reason=timeout") {
		t.Fatalf("logger output should classify failure as timeout: %s", logOut)
	}
	if !strings.Contains(logOut, "msg=\"triage failed\"") {
		t.Fatalf("logger output should contain triage failed message: %s", logOut)
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

	var logBuf bytes.Buffer
	poller := Poller{
		DB:     database,
		GitHub: client,
		Triage: runner,
		Logger: NewLogger(&logBuf),
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

	logOut := logBuf.String()
	if !strings.Contains(logOut, "msg=\"triage failed\"") {
		t.Fatalf("expected a triage failed log line: %s", logOut)
	}
	if !strings.Contains(logOut, "item=acme/widgets#1") {
		t.Fatalf("logger output missing item context: %s", logOut)
	}
	if !strings.Contains(logOut, "reason=error") {
		t.Fatalf("non-timeout failure should be classified reason=error: %s", logOut)
	}
	if !strings.Contains(logOut, "claude exited: exit status 1") {
		t.Fatalf("logger output missing underlying error: %s", logOut)
	}
}

func TestPollOnceLogsRepoSyncAndTriageSuccess(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		itemsByRepo: map[string][]ghclient.Item{
			"acme/widgets": {{
				Repo:      "acme/widgets",
				Kind:      sharedtypes.ItemKindIssue,
				Number:    7,
				Title:     "log audit",
				Author:    "alice",
				State:     sharedtypes.ItemStateOpen,
				URL:       "https://github.com/acme/widgets/issues/7",
				UpdatedAt: time.Unix(1713511200, 0).UTC(),
			}},
		},
	}
	runner := &stubRecommendationRunner{
		result: &TriageResult{
			Agent: sharedtypes.AgentClaude,
			Model: "sonnet",
			Recommendation: &triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange: sharedtypes.StateChangeNone,
					Rationale:   "needs repro",
					WaitingOn:   sharedtypes.WaitingOnContributor,
					Confidence:  sharedtypes.ConfidenceMedium,
				}},
			},
			TokensIn:  1234,
			TokensOut: 56,
		},
	}

	var logBuf bytes.Buffer
	poller := Poller{
		DB:     database,
		GitHub: client,
		Triage: runner,
		Logger: NewLogger(&logBuf),
	}
	if err := PollOnce(context.Background(), poller, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	out := logBuf.String()
	if !strings.Contains(out, `msg="repo synced"`) || !strings.Contains(out, "repo=acme/widgets") {
		t.Fatalf("expected repo synced log: %s", out)
	}
	if !strings.Contains(out, `msg="triage done"`) {
		t.Fatalf("expected triage done log: %s", out)
	}
	for _, want := range []string{"item=acme/widgets#7", "agent=claude", "model=sonnet", "tokens_in=1234", "tokens_out=56", "options=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("triage done log missing %q\nfull log:\n%s", want, out)
		}
	}
}

func TestPollOnceLogsStaleAutoRecommendation(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	threshold := 24 * time.Hour
	now := time.Unix(1713600000, 0).UTC()
	lastEvent := now.Add(-2 * threshold)

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:          "acme/widgets#11",
		RepoID:      "acme/widgets",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      11,
		Title:       "ghosted",
		Author:      "alice",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &lastEvent,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	var logBuf bytes.Buffer
	poller := Poller{
		DB:             database,
		StaleThreshold: threshold,
		Logger:         NewLogger(&logBuf),
	}
	if err := surfaceStaleRecommendations(poller, "acme/widgets", now); err != nil {
		t.Fatalf("surfaceStaleRecommendations() error = %v", err)
	}

	out := logBuf.String()
	if !strings.Contains(out, `msg="auto-recommended close (stale)"`) {
		t.Fatalf("expected stale auto-rec log line, got:\n%s", out)
	}
	if !strings.Contains(out, "item=acme/widgets#11") {
		t.Fatalf("stale log missing item id: %s", out)
	}
}

type stubTriageClient struct {
	itemsByRepo        map[string][]ghclient.Item
	triagedItemsByRepo map[string][]ghclient.Item
	itemByKey          map[string]ghclient.Item
	errByRepo          map[string]error
	triagedErrByRepo   map[string]error
	calls              []string
	triagedCalls       []string
	triagedSinceByCall []time.Time

	authoredPRs    []ghclient.Item
	authoredIssues []ghclient.Item
	authoredErr    error
	authoredCalls  int

	ownedRepos    []string
	ownedReposErr error
}

func (s *stubTriageClient) ListOwnedRepos(_ context.Context, _ ghclient.RepoVisibility) ([]string, error) {
	if s.ownedReposErr != nil {
		return nil, s.ownedReposErr
	}
	return append([]string(nil), s.ownedRepos...), nil
}

func (s *stubTriageClient) SearchAuthoredOpenPRs(_ context.Context) ([]ghclient.Item, error) {
	s.authoredCalls++
	if s.authoredErr != nil {
		return nil, s.authoredErr
	}
	return append([]ghclient.Item(nil), s.authoredPRs...), nil
}

func (s *stubTriageClient) SearchAuthoredOpenIssues(_ context.Context) ([]ghclient.Item, error) {
	if s.authoredErr != nil {
		return nil, s.authoredErr
	}
	return append([]ghclient.Item(nil), s.authoredIssues...), nil
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

func (s *stubTriageClient) GetItem(_ context.Context, repo string, _ sharedtypes.ItemKind, number int) (ghclient.Item, error) {
	item, ok := s.itemByKey[itemID(repo, number)]
	if !ok {
		return ghclient.Item{}, errors.New("item not found")
	}
	return item, nil
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

func TestPollOnceContribSweepUpsertsAuthoredItems(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	now := time.Date(2026, time.April, 30, 12, 0, 0, 0, time.UTC)
	client := &stubTriageClient{
		authoredPRs: []ghclient.Item{
			{
				Repo:         "upstream/widgets",
				Kind:         sharedtypes.ItemKindPR,
				Number:       321,
				Title:        "fix race",
				Author:       "kun",
				State:        sharedtypes.ItemStateOpen,
				URL:          "https://github.com/upstream/widgets/pull/321",
				UpdatedAt:    now.Add(-1 * time.Hour),
				HeadRepo:     "kun/widgets",
				HeadRef:      "fix-race",
				HeadCloneURL: "https://github.com/kun/widgets.git",
			},
		},
		authoredIssues: []ghclient.Item{
			{
				Repo:      "upstream/widgets",
				Kind:      sharedtypes.ItemKindIssue,
				Number:    310,
				Title:     "panic on race",
				Author:    "kun",
				State:     sharedtypes.ItemStateOpen,
				URL:       "https://github.com/upstream/widgets/issues/310",
				UpdatedAt: now.Add(-2 * time.Hour),
			},
		},
	}

	poller := Poller{DB: database, GitHub: client, ContribEnabled: true}
	if err := PollOnce(context.Background(), poller, nil); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}

	if client.authoredCalls != 1 {
		t.Fatalf("authoredCalls = %d, want 1", client.authoredCalls)
	}

	repo, err := database.GetRepo("upstream/widgets")
	if err != nil {
		t.Fatalf("GetRepo error: %v", err)
	}
	if repo == nil {
		t.Fatal("expected contrib repo to be auto-created")
	}
	if repo.Source != db.RepoSourceContrib {
		t.Fatalf("repo source = %q, want %q", repo.Source, db.RepoSourceContrib)
	}

	pr, err := database.GetItem("upstream/widgets#321")
	if err != nil {
		t.Fatalf("GetItem(pr) error: %v", err)
	}
	if pr == nil {
		t.Fatal("expected PR item to be stored")
	}
	if pr.Role != sharedtypes.RoleContributor {
		t.Fatalf("PR role = %q, want contributor", pr.Role)
	}
	if pr.HeadRepo != "kun/widgets" || pr.HeadRef != "fix-race" || pr.HeadCloneURL == "" {
		t.Fatalf("PR head info missing: %#v", pr)
	}
	if pr.LastSeenUpdatedAt == nil {
		t.Fatal("PR LastSeenUpdatedAt should be set after sweep")
	}

	issue, err := database.GetItem("upstream/widgets#310")
	if err != nil {
		t.Fatalf("GetItem(issue) error: %v", err)
	}
	if issue == nil || issue.Role != sharedtypes.RoleContributor {
		t.Fatalf("issue not stored as contributor: %#v", issue)
	}
}

func TestPollOnceContribSweepDisabledByDefault(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	client := &stubTriageClient{
		authoredPRs: []ghclient.Item{{
			Repo: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Number: 1, State: sharedtypes.ItemStateOpen,
			URL: "https://github.com/upstream/widgets/pull/1", UpdatedAt: time.Now().UTC(),
		}},
	}
	poller := Poller{DB: database, GitHub: client}
	if err := PollOnce(context.Background(), poller, nil); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}
	if client.authoredCalls != 0 {
		t.Fatalf("authoredCalls = %d, want 0 (contrib disabled)", client.authoredCalls)
	}
}

func TestPollOnceContribSweepSkipsItemsInOwnedReposEvenWhenNotConfigured(t *testing.T) {
	// User owns kun/scratch on GitHub but hasn't added it to cfg.Repos
	// yet. The sweep must still skip authored items there - they
	// belong to the maintainer flow whenever the user wires the repo
	// up, and showing them as "contributor" in the inbox is misleading.
	t.Parallel()

	database := openTestDB(t)
	now := time.Now().UTC()
	client := &stubTriageClient{
		ownedRepos: []string{"kun/scratch"},
		authoredPRs: []ghclient.Item{
			{Repo: "kun/scratch", Kind: sharedtypes.ItemKindPR, Number: 1, State: sharedtypes.ItemStateOpen, URL: "https://github.com/kun/scratch/pull/1", UpdatedAt: now},
			{Repo: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, State: sharedtypes.ItemStateOpen, URL: "https://github.com/upstream/widgets/pull/99", UpdatedAt: now, HeadRepo: "kun/widgets", HeadRef: "fix", HeadCloneURL: "https://github.com/kun/widgets.git"},
		},
	}
	poller := Poller{DB: database, GitHub: client, ContribEnabled: true}
	if err := PollOnce(context.Background(), poller, nil); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}
	if got, _ := database.GetItem("kun/scratch#1"); got != nil {
		t.Fatalf("kun/scratch#1 should be skipped (user owns the repo): %#v", got)
	}
	if got, _ := database.GetItem("upstream/widgets#99"); got == nil {
		t.Fatalf("upstream/widgets#99 should be tracked as contributor")
	}
}

func TestPollOnceContribSweepProceedsWhenOwnedReposLookupFails(t *testing.T) {
	// gh repo list failures must not blackhole the inbox - the sweep
	// downgrades to "no ownership filter this cycle" rather than
	// dropping all authored items on the floor.
	t.Parallel()

	database := openTestDB(t)
	now := time.Now().UTC()
	client := &stubTriageClient{
		ownedReposErr: errors.New("gh down"),
		authoredPRs: []ghclient.Item{
			{Repo: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, State: sharedtypes.ItemStateOpen, URL: "https://github.com/upstream/widgets/pull/99", UpdatedAt: now, HeadRepo: "kun/widgets", HeadRef: "fix", HeadCloneURL: "https://github.com/kun/widgets.git"},
		},
	}
	poller := Poller{DB: database, GitHub: client, ContribEnabled: true}
	if err := PollOnce(context.Background(), poller, nil); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}
	if got, _ := database.GetItem("upstream/widgets#99"); got == nil {
		t.Fatal("upstream/widgets#99 should still be tracked when the ownership filter is unavailable")
	}
}

func TestPollOnceContribSweepSkipsItemsInMaintainerRepos(t *testing.T) {
	// When a user has their own repo in cfg.Repos, the maintainer sync
	// (Stage A.1) is the source of truth - even for items they
	// authored. The contrib sweep must skip those so the two stages
	// don't fight over the same item with different roles.
	t.Parallel()

	database := openTestDB(t)
	now := time.Now().UTC()
	client := &stubTriageClient{
		// Stage A.1 returns nothing for the configured repo here; the
		// point of this test is just that Stage A.2 doesn't write a
		// contributor row for the same item.
		itemsByRepo: map[string][]ghclient.Item{"kun/own-repo": nil},
		authoredPRs: []ghclient.Item{
			{
				Repo: "kun/own-repo", Kind: sharedtypes.ItemKindPR, Number: 7,
				State: sharedtypes.ItemStateOpen, URL: "https://github.com/kun/own-repo/pull/7",
				UpdatedAt: now, HeadRepo: "kun/own-repo", HeadRef: "feat", HeadCloneURL: "https://github.com/kun/own-repo.git",
			},
			{
				Repo: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Number: 99,
				State: sharedtypes.ItemStateOpen, URL: "https://github.com/upstream/widgets/pull/99",
				UpdatedAt: now, HeadRepo: "kun/widgets", HeadRef: "fix", HeadCloneURL: "https://github.com/kun/widgets.git",
			},
		},
	}
	poller := Poller{DB: database, GitHub: client, ContribEnabled: true}
	if err := PollOnce(context.Background(), poller, []string{"kun/own-repo"}); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}

	// The own-repo PR must NOT have been written as a contributor item.
	if got, _ := database.GetItem("kun/own-repo#7"); got != nil {
		t.Fatalf("kun/own-repo#7 should be skipped by contrib sweep, got %#v", got)
	}
	// The upstream PR (truly contributor-only) is still tracked.
	if got, _ := database.GetItem("upstream/widgets#99"); got == nil || got.Role != sharedtypes.RoleContributor {
		t.Fatalf("upstream/widgets#99 should be tracked as contributor, got %#v", got)
	}
}

func TestPollOnceContribSweepPrunesReposMissingFromLatestSweep(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "upstream/widgets", Source: db.RepoSourceContrib}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "upstream/widgets#321", RepoID: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Role: sharedtypes.RoleContributor,
		Number: 321, Title: "old", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:  "upstream/widgets#321",
		Agent:   sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	client := &stubTriageClient{}
	poller := Poller{DB: database, GitHub: client, ContribEnabled: true}
	if err := PollOnce(context.Background(), poller, nil); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}
	if got, err := database.GetItem("upstream/widgets#321"); err != nil || got != nil {
		t.Fatalf("contributor item after prune = %#v, %v; want nil", got, err)
	}
	if got, err := database.GetRepo("upstream/widgets"); err != nil || got != nil {
		t.Fatalf("contrib repo after prune = %#v, %v; want nil", got, err)
	}
}

func TestPollOnceContribSweepDoesNotPruneAfterAuthoredSearchError(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "upstream/widgets", Source: db.RepoSourceContrib}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "upstream/widgets#321", RepoID: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Role: sharedtypes.RoleContributor,
		Number: 321, Title: "old", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}

	client := &stubTriageClient{authoredErr: errors.New("search failed")}
	poller := Poller{DB: database, GitHub: client, ContribEnabled: true}
	if err := PollOnce(context.Background(), poller, nil); err == nil {
		t.Fatal("PollOnce error = nil, want authored search error")
	}
	if got, err := database.GetItem("upstream/widgets#321"); err != nil || got == nil {
		t.Fatalf("contributor item after failed sweep = %#v, %v; want existing item", got, err)
	}
	if got, err := database.GetRepo("upstream/widgets"); err != nil || got == nil {
		t.Fatalf("contrib repo after failed sweep = %#v, %v; want existing repo", got, err)
	}
}

func TestPollOnceContribSweepIgnoresConfiguredRepos(t *testing.T) {
	t.Parallel()

	database := openTestDB(t)
	now := time.Now().UTC()
	client := &stubTriageClient{
		authoredPRs: []ghclient.Item{
			{Repo: "noisy/repo", Kind: sharedtypes.ItemKindPR, Number: 1, State: sharedtypes.ItemStateOpen, URL: "https://github.com/noisy/repo/pull/1", UpdatedAt: now},
			{Repo: "good/repo", Kind: sharedtypes.ItemKindPR, Number: 2, State: sharedtypes.ItemStateOpen, URL: "https://github.com/good/repo/pull/2", UpdatedAt: now},
		},
	}
	poller := Poller{DB: database, GitHub: client, ContribEnabled: true, ContribIgnoreRepos: []string{"noisy/repo"}}
	if err := PollOnce(context.Background(), poller, nil); err != nil {
		t.Fatalf("PollOnce error: %v", err)
	}
	if got, _ := database.GetRepo("noisy/repo"); got != nil {
		t.Fatal("noisy/repo should have been suppressed")
	}
	if got, _ := database.GetRepo("good/repo"); got == nil {
		t.Fatal("good/repo should have been registered")
	}
}
