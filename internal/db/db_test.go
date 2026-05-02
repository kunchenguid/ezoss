package db

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	database, err := Open(dbPath)
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

// singleOptionRec builds a NewRecommendation with one option for tests
// that don't care about the multi-option shape.
func singleOptionRec(itemID string, agent sharedtypes.AgentName, opt NewRecommendationOption) NewRecommendation {
	return NewRecommendation{
		ItemID:  itemID,
		Agent:   agent,
		Options: []NewRecommendationOption{opt},
	}
}

func TestOpenCreatesSchema(t *testing.T) {
	database := openTestDB(t)

	for _, table := range []string{"repos", "items", "recommendations", "recommendation_options", "approvals", "fix_jobs"} {
		if err := database.assertTableExists(table); err != nil {
			t.Fatalf("expected table %q to exist: %v", table, err)
		}
	}
}

func TestCreateFixJobReturnsExistingActiveJob(t *testing.T) {
	database := openTestDB(t)

	job, err := database.CreateFixJob(NewFixJob{
		ItemID:           "acme/widgets#42",
		RecommendationID: "rec-1",
		OptionID:         "opt-1",
		RepoID:           "acme/widgets",
		ItemNumber:       42,
		ItemKind:         sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		FixPrompt:        "Fix the parser panic.",
		PRCreate:         "no-mistakes",
	})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if job.Status != FixJobStatusQueued || job.Phase != FixJobPhaseQueued {
		t.Fatalf("job status/phase = %q/%q, want queued/queued", job.Status, job.Phase)
	}

	again, err := database.CreateFixJob(NewFixJob{
		ItemID:           "acme/widgets#42",
		RecommendationID: "rec-1",
		OptionID:         "opt-1",
		RepoID:           "acme/widgets",
		ItemNumber:       42,
		ItemKind:         sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		FixPrompt:        "Fix the parser panic.",
		PRCreate:         "no-mistakes",
	})
	if err != nil {
		t.Fatalf("CreateFixJob() second error = %v", err)
	}
	if again.ID != job.ID {
		t.Fatalf("second job ID = %q, want existing active job %q", again.ID, job.ID)
	}
}

func TestCreateFixJobAllowsOnlyOneConcurrentActiveJob(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	database.sql.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = database.Close() })

	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	ids := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
			if err != nil {
				errCh <- err
				return
			}
			ids <- job.ID
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	close(ids)
	for err := range errCh {
		if err != nil {
			t.Fatalf("CreateFixJob() concurrent error = %v", err)
		}
	}
	seen := map[string]struct{}{}
	for id := range ids {
		seen[id] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("concurrent CreateFixJob() returned %d active jobs, want 1", len(seen))
	}
	jobs, err := database.ListFixJobs()
	if err != nil {
		t.Fatalf("ListFixJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListFixJobs() returned %d jobs, want 1", len(jobs))
	}
}

func TestReclaimStaleRunningFixJobsFailsInterruptedJobs(t *testing.T) {
	database := openTestDB(t)
	stale, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() stale error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() stale error = %v", err)
	}
	if err := database.UpdateFixJob(stale.ID, FixJobUpdate{Status: FixJobStatusRunning, Phase: FixJobPhaseRunningAgent}); err != nil {
		t.Fatalf("UpdateFixJob() stale error = %v", err)
	}
	waiting, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#43", RecommendationID: "rec-2", RepoID: "acme/widgets", ItemNumber: 43, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() waiting error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() waiting error = %v", err)
	}
	if err := database.UpdateFixJob(waiting.ID, FixJobUpdate{Status: FixJobStatusRunning, Phase: FixJobPhaseWaitingForPR}); err != nil {
		t.Fatalf("UpdateFixJob() waiting error = %v", err)
	}
	fresh, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#44", RecommendationID: "rec-3", RepoID: "acme/widgets", ItemNumber: 44, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() fresh error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() fresh error = %v", err)
	}
	if err := database.UpdateFixJob(fresh.ID, FixJobUpdate{Status: FixJobStatusRunning, Phase: FixJobPhaseRunningAgent}); err != nil {
		t.Fatalf("UpdateFixJob() fresh error = %v", err)
	}
	oldUpdatedAt := time.Now().Add(-2 * time.Hour).Unix()
	if _, err := database.sql.Exec(`UPDATE fix_jobs SET updated_at = ? WHERE id IN (?, ?)`, oldUpdatedAt, stale.ID, waiting.ID); err != nil {
		t.Fatalf("force old updated_at: %v", err)
	}

	reclaimed, err := database.ReclaimStaleRunningFixJobs(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ReclaimStaleRunningFixJobs() error = %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("ReclaimStaleRunningFixJobs() = %d, want 1", reclaimed)
	}
	gotStale, err := database.GetFixJob(stale.ID)
	if err != nil {
		t.Fatalf("GetFixJob() stale error = %v", err)
	}
	if gotStale.Status != FixJobStatusFailed || gotStale.Phase != FixJobPhaseFailed || gotStale.CompletedAt == nil {
		t.Fatalf("stale job = %#v, want failed", gotStale)
	}
	gotWaiting, err := database.GetFixJob(waiting.ID)
	if err != nil {
		t.Fatalf("GetFixJob() waiting error = %v", err)
	}
	if gotWaiting.Status != FixJobStatusRunning || gotWaiting.Phase != FixJobPhaseWaitingForPR {
		t.Fatalf("waiting job = %#v, want still waiting", gotWaiting)
	}
	gotFresh, err := database.GetFixJob(fresh.ID)
	if err != nil {
		t.Fatalf("GetFixJob() fresh error = %v", err)
	}
	if gotFresh.Status != FixJobStatusRunning || gotFresh.Phase != FixJobPhaseRunningAgent {
		t.Fatalf("fresh job = %#v, want still running", gotFresh)
	}
	newJob, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-4", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix retry.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() retry error = %v", err)
	}
	if newJob.ID == stale.ID {
		t.Fatalf("CreateFixJob() retry returned stale active job %s", stale.ID)
	}
}

func TestClaimNextQueuedFixJobAndUpdateStatus(t *testing.T) {
	database := openTestDB(t)
	job, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}

	claimed, err := database.ClaimNextQueuedFixJob()
	if err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() error = %v", err)
	}
	if claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claimed = %#v, want job %s", claimed, job.ID)
	}
	if claimed.Status != FixJobStatusRunning {
		t.Fatalf("claimed status = %q, want running", claimed.Status)
	}

	if err := database.UpdateFixJob(job.ID, FixJobUpdate{Status: FixJobStatusSucceeded, Phase: FixJobPhasePROpened, Branch: "ezoss/fix-42", WorktreePath: "/tmp/w", PRURL: "https://github.com/acme/widgets/pull/99", Message: "PR opened"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != FixJobStatusSucceeded || got.Phase != FixJobPhasePROpened || got.PRURL == "" || got.CompletedAt == nil {
		t.Fatalf("updated job = %#v, want succeeded with PR URL and completed_at", got)
	}
}

func TestLatestFixJobForItemBreaksCreatedAtTiesByID(t *testing.T) {
	database := openTestDB(t)
	first, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() first error = %v", err)
	}
	if err := database.UpdateFixJob(first.ID, FixJobUpdate{Status: FixJobStatusFailed, Phase: FixJobPhaseFailed, Error: "retry"}); err != nil {
		t.Fatalf("UpdateFixJob() first error = %v", err)
	}
	second, err := database.CreateFixJob(NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-2", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it again.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() second error = %v", err)
	}
	if first.ID >= second.ID {
		t.Fatalf("test setup IDs are not ordered: first=%q second=%q", first.ID, second.ID)
	}
	if _, err := database.sql.Exec(`UPDATE fix_jobs SET created_at = ? WHERE id IN (?, ?)`, int64(1234), first.ID, second.ID); err != nil {
		t.Fatalf("force created_at tie: %v", err)
	}

	got, err := database.LatestFixJobForItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("LatestFixJobForItem() error = %v", err)
	}
	if got == nil || got.ID != second.ID {
		t.Fatalf("LatestFixJobForItem() = %#v, want second job %s", got, second.ID)
	}
}

func TestOpenReportsNoMigrationsForFreshDatabase(t *testing.T) {
	database := openTestDB(t)
	if applied := database.MigrationsApplied(); len(applied) != 0 {
		t.Fatalf("MigrationsApplied() on fresh DB = %v, want empty", applied)
	}
}

func TestOpenReportsAppliedColumnMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.sqlite")
	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	rawDB.SetMaxOpenConns(1)

	// Create a table missing the recommendations.followups column so
	// Open's ensureColumnExists fires for the first time.
	legacySchema := `
CREATE TABLE repos (id TEXT PRIMARY KEY, default_branch TEXT, last_poll_at INTEGER, created_at INTEGER NOT NULL);
CREATE TABLE items (id TEXT PRIMARY KEY, repo_id TEXT NOT NULL REFERENCES repos(id), kind TEXT NOT NULL, number INTEGER NOT NULL, title TEXT, author TEXT, state TEXT, is_draft INTEGER NOT NULL DEFAULT 0, gh_triaged INTEGER NOT NULL DEFAULT 0, waiting_on TEXT, last_event_at INTEGER, stale_since INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE recommendations (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, agent TEXT NOT NULL, model TEXT, rationale TEXT, draft_comment TEXT, proposed_labels TEXT, state_change TEXT, confidence TEXT, tokens_in INTEGER, tokens_out INTEGER, created_at INTEGER NOT NULL, superseded_at INTEGER);
CREATE TABLE approvals (id TEXT PRIMARY KEY, recommendation_id TEXT NOT NULL REFERENCES recommendations(id) ON DELETE CASCADE, decision TEXT NOT NULL, final_comment TEXT, final_labels TEXT, final_state_change TEXT, acted_at INTEGER, acted_error TEXT, created_at INTEGER NOT NULL);
`
	if _, err := rawDB.Exec(legacySchema); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	migrated, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer migrated.Close()

	applied := migrated.MigrationsApplied()
	if len(applied) == 0 {
		t.Fatalf("MigrationsApplied() = empty, want at least one entry naming a column migration")
	}
	hasFollowups := false
	for _, name := range applied {
		if name == "recommendations.followups" {
			hasFollowups = true
		}
	}
	if !hasFollowups {
		t.Errorf("MigrationsApplied() = %v, missing recommendations.followups", applied)
	}

	// Re-opening with the up-to-date schema should now report nothing.
	if err := migrated.Close(); err != nil {
		t.Fatalf("close migrated db: %v", err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() reopen error = %v", err)
	}
	defer reopened.Close()
	if applied := reopened.MigrationsApplied(); len(applied) != 0 {
		t.Errorf("MigrationsApplied() after re-open = %v, want empty", applied)
	}
}

func TestOpenWaitsForBriefWriteLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	seed, err := Open(dbPath)
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	locker, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open locker db: %v", err)
	}
	defer locker.Close()
	locker.SetMaxOpenConns(1)

	tx, err := locker.Begin()
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO repos (id, default_branch, created_at) VALUES ('lock/test', 'main', 1)`); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}

	releaseDelay := 150 * time.Millisecond
	go func() {
		time.Sleep(releaseDelay)
		_ = tx.Commit()
	}()

	opened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() during brief write lock error = %v, want success after lock releases", err)
	}
	defer opened.Close()
}

func TestRepoRoundTrip(t *testing.T) {
	database := openTestDB(t)

	lastPollAt := time.Unix(1710000000, 0)
	lastTriagedRefreshAt := time.Unix(1710003600, 0)

	repo := Repo{
		ID:                   "kunchenguid/ezoss",
		DefaultBranch:        "main",
		LastPollAt:           &lastPollAt,
		LastTriagedRefreshAt: &lastTriagedRefreshAt,
	}
	if err := database.UpsertRepo(repo); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}

	got, err := database.GetRepo(repo.ID)
	if err != nil {
		t.Fatalf("get repo: %v", err)
	}
	if got == nil {
		t.Fatal("expected repo")
	}
	if got.ID != repo.ID {
		t.Fatalf("repo id = %q, want %q", got.ID, repo.ID)
	}
	if got.DefaultBranch != repo.DefaultBranch {
		t.Fatalf("default branch = %q, want %q", got.DefaultBranch, repo.DefaultBranch)
	}
	if got.LastPollAt == nil || !got.LastPollAt.Equal(lastPollAt) {
		t.Fatalf("last_poll_at = %v, want %v", got.LastPollAt, lastPollAt)
	}
	if got.LastTriagedRefreshAt == nil || !got.LastTriagedRefreshAt.Equal(lastTriagedRefreshAt) {
		t.Fatalf("last_triaged_refresh_at = %v, want %v", got.LastTriagedRefreshAt, lastTriagedRefreshAt)
	}
	if got.CreatedAt == 0 {
		t.Fatal("expected created_at to be set")
	}
}

func TestItemRoundTrip(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}

	lastEventAt := time.Unix(1710000000, 0)
	staleSince := time.Unix(1712592000, 0)
	item := Item{
		ID:          "kunchenguid/ezoss#42",
		RepoID:      "kunchenguid/ezoss",
		Kind:        sharedtypes.ItemKindIssue,
		Number:      42,
		Title:       "Bug: triage queue stalls",
		Author:      "octocat",
		State:       sharedtypes.ItemStateOpen,
		IsDraft:     false,
		GHTriaged:   false,
		WaitingOn:   sharedtypes.WaitingOnContributor,
		LastEventAt: &lastEventAt,
		StaleSince:  &staleSince,
	}
	if err := database.UpsertItem(item); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	got, err := database.GetItem(item.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got == nil {
		t.Fatal("expected item")
	}
	if got.Kind != item.Kind {
		t.Fatalf("kind = %q, want %q", got.Kind, item.Kind)
	}
	if got.WaitingOn != item.WaitingOn {
		t.Fatalf("waiting_on = %q, want %q", got.WaitingOn, item.WaitingOn)
	}
	if got.LastEventAt == nil || !got.LastEventAt.Equal(lastEventAt) {
		t.Fatalf("last_event_at = %v, want %v", got.LastEventAt, lastEventAt)
	}
	if got.StaleSince == nil || !got.StaleSince.Equal(staleSince) {
		t.Fatalf("stale_since = %v, want %v", got.StaleSince, staleSince)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatal("expected timestamps to be set")
	}
}

func TestListItemsNeedingTriageDedupesAgainstActiveRecommendations(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}

	staleEvent := time.Unix(1700000000, 0).UTC()
	freshEvent := time.Unix(1800000000, 0).UTC()

	mustUpsertItem := func(id string, number int, state sharedtypes.ItemState, ghTriaged bool, lastEvent *time.Time) {
		if err := database.UpsertItem(Item{
			ID:          id,
			RepoID:      "kunchenguid/ezoss",
			Kind:        sharedtypes.ItemKindIssue,
			Number:      number,
			Title:       "x",
			State:       state,
			GHTriaged:   ghTriaged,
			LastEventAt: lastEvent,
		}); err != nil {
			t.Fatalf("upsert item %s: %v", id, err)
		}
	}
	mustInsertActiveRec := func(itemID string) {
		if _, err := database.InsertRecommendation(singleOptionRec(itemID, sharedtypes.AgentClaude, NewRecommendationOption{
			StateChange: sharedtypes.StateChangeNone,
			Rationale:   "rationale",
			Confidence:  sharedtypes.ConfidenceMedium,
		})); err != nil {
			t.Fatalf("insert recommendation for %s: %v", itemID, err)
		}
	}

	mustUpsertItem("kunchenguid/ezoss#1", 1, sharedtypes.ItemStateOpen, false, &staleEvent)

	mustUpsertItem("kunchenguid/ezoss#2", 2, sharedtypes.ItemStateOpen, false, &staleEvent)
	mustInsertActiveRec("kunchenguid/ezoss#2")

	mustUpsertItem("kunchenguid/ezoss#3", 3, sharedtypes.ItemStateOpen, false, nil)
	mustInsertActiveRec("kunchenguid/ezoss#3")
	mustUpsertItem("kunchenguid/ezoss#3", 3, sharedtypes.ItemStateOpen, false, &freshEvent)

	mustUpsertItem("kunchenguid/ezoss#4", 4, sharedtypes.ItemStateOpen, true, &staleEvent)

	mustUpsertItem("kunchenguid/ezoss#5", 5, sharedtypes.ItemStateClosed, false, &staleEvent)

	mustUpsertItem("kunchenguid/ezoss#6", 6, sharedtypes.ItemStateOpen, false, &staleEvent)
	if _, err := database.sql.Exec(
		`INSERT INTO recommendations (id, item_id, agent, model, rationale, draft_comment, followups, proposed_labels, state_change, confidence, tokens_in, tokens_out, created_at, superseded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sup-1", "kunchenguid/ezoss#6", sharedtypes.AgentClaude, "sonnet", "x", "", nil, nil, sharedtypes.StateChangeNone, sharedtypes.ConfidenceMedium, 0, 0, time.Now().Unix(), time.Now().Unix(),
	); err != nil {
		t.Fatalf("insert superseded recommendation: %v", err)
	}

	got, err := database.ListItemsNeedingTriage()
	if err != nil {
		t.Fatalf("ListItemsNeedingTriage() error = %v", err)
	}

	gotIDs := make([]string, 0, len(got))
	for _, item := range got {
		gotIDs = append(gotIDs, item.ID)
	}
	wantIDs := []string{"kunchenguid/ezoss#1", "kunchenguid/ezoss#3", "kunchenguid/ezoss#6"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("ListItemsNeedingTriage() ids = %v, want %v", gotIDs, wantIDs)
	}
	gotSet := make(map[string]struct{}, len(gotIDs))
	for _, id := range gotIDs {
		gotSet[id] = struct{}{}
	}
	for _, want := range wantIDs {
		if _, ok := gotSet[want]; !ok {
			t.Fatalf("ListItemsNeedingTriage() ids = %v, missing %q", gotIDs, want)
		}
	}
}

func TestListItemsNeedingTriageSkipsLocallyTriagedContributorItems(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "upstream/widgets", Source: RepoSourceContrib}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	lastEvent := time.Unix(1800000000, 0).UTC()
	if err := database.UpsertItem(Item{
		ID:          "upstream/widgets#7",
		RepoID:      "upstream/widgets",
		Kind:        sharedtypes.ItemKindPR,
		Role:        sharedtypes.RoleContributor,
		Number:      7,
		Title:       "fix widgets",
		State:       sharedtypes.ItemStateOpen,
		GHTriaged:   true,
		LastEventAt: &lastEvent,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	if _, err := database.sql.Exec(
		`INSERT INTO recommendations (id, item_id, agent, model, rationale, draft_comment, followups, proposed_labels, state_change, confidence, tokens_in, tokens_out, created_at, superseded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sup-contrib", "upstream/widgets#7", sharedtypes.AgentClaude, "sonnet", "x", "", nil, nil, sharedtypes.StateChangeNone, sharedtypes.ConfidenceMedium, 0, 0, time.Now().Unix(), time.Now().Unix(),
	); err != nil {
		t.Fatalf("insert superseded recommendation: %v", err)
	}

	got, err := database.ListItemsNeedingTriage()
	if err != nil {
		t.Fatalf("ListItemsNeedingTriage() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListItemsNeedingTriage() = %v, want no locally triaged contributor items", got)
	}
}

func TestRecommendationRoundTripAndListActive(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:        "kunchenguid/ezoss#42",
		RepoID:    "kunchenguid/ezoss",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		Author:    "octocat",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	rec, err := database.InsertRecommendation(NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Model:  "claude-sonnet-4-5",
		Options: []NewRecommendationOption{{
			StateChange:    sharedtypes.StateChangeNone,
			Rationale:      "The report includes a repro and points to a likely regression.",
			DraftComment:   "Thanks for the report. I can reproduce this and will take a look.",
			FixPrompt:      "Fix https://github.com/kunchenguid/ezoss/issues/42 by reproducing the queue stall and adding a regression test.",
			Followups:      []string{"Check linked issue history", "Confirm this still reproduces on main"},
			ProposedLabels: []string{"bug", "needs-investigation"},
			Confidence:     sharedtypes.ConfidenceHigh,
			WaitingOn:      sharedtypes.WaitingOnMaintainer,
		}},
		TokensIn:  1200,
		TokensOut: 240,
	})
	if err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}
	if rec.ID == "" {
		t.Fatal("expected generated recommendation id")
	}
	if len(rec.Options) != 1 || rec.Options[0].ID == "" {
		t.Fatalf("expected one option with id, got %#v", rec.Options)
	}

	got, err := database.GetRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("get recommendation: %v", err)
	}
	if got == nil {
		t.Fatal("expected recommendation")
	}
	if got.Model != rec.Model {
		t.Fatalf("model = %q, want %q", got.Model, rec.Model)
	}
	if len(got.Options) != 1 {
		t.Fatalf("len(Options) = %d, want 1", len(got.Options))
	}
	opt := got.Options[0]
	if opt.Position != 0 {
		t.Fatalf("Position = %d, want 0", opt.Position)
	}
	if opt.FixPrompt != "Fix https://github.com/kunchenguid/ezoss/issues/42 by reproducing the queue stall and adding a regression test." {
		t.Fatalf("FixPrompt = %q", opt.FixPrompt)
	}
	if len(opt.Followups) != 2 || opt.Followups[0] != "Check linked issue history" || opt.Followups[1] != "Confirm this still reproduces on main" {
		t.Fatalf("followups = %#v", opt.Followups)
	}
	if len(opt.ProposedLabels) != 2 || opt.ProposedLabels[0] != "bug" || opt.ProposedLabels[1] != "needs-investigation" {
		t.Fatalf("proposed labels = %#v", opt.ProposedLabels)
	}
	if opt.WaitingOn != sharedtypes.WaitingOnMaintainer {
		t.Fatalf("WaitingOn = %q, want maintainer", opt.WaitingOn)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("list active recommendations: %v", err)
	}
	if len(active) != 1 || active[0].ID != rec.ID {
		t.Fatalf("active recommendations = %#v", active)
	}
	if len(active[0].Options) != 1 {
		t.Fatalf("active recommendation options = %#v", active[0].Options)
	}
	if active[0].Options[0].FixPrompt != opt.FixPrompt {
		t.Fatalf("active FixPrompt = %q, want %q", active[0].Options[0].FixPrompt, opt.FixPrompt)
	}

	if err := database.MarkRecommendationSuperseded(rec.ID, time.Unix(1713000000, 0)); err != nil {
		t.Fatalf("mark superseded: %v", err)
	}

	active, err = database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("list active recommendations after supersede: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("expected no active recommendations, got %#v", active)
	}
}

func TestInsertRecommendationStoresMultipleOptionsInOrder(t *testing.T) {
	database := openTestDB(t)
	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID: "kunchenguid/ezoss#42", RepoID: "kunchenguid/ezoss",
		Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "x", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	rec, err := database.InsertRecommendation(NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []NewRecommendationOption{
			{StateChange: sharedtypes.StateChangeClose, Rationale: "close as stale", DraftComment: "Closing.", Confidence: sharedtypes.ConfidenceHigh, WaitingOn: sharedtypes.WaitingOnContributor},
			{StateChange: sharedtypes.StateChangeNone, Rationale: "one more nudge", DraftComment: "Ping.", Confidence: sharedtypes.ConfidenceMedium, WaitingOn: sharedtypes.WaitingOnContributor},
		},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation: %v", err)
	}
	if len(rec.Options) != 2 {
		t.Fatalf("len(rec.Options) = %d, want 2", len(rec.Options))
	}
	if rec.Options[0].Position != 0 || rec.Options[1].Position != 1 {
		t.Fatalf("positions = [%d, %d], want [0, 1]", rec.Options[0].Position, rec.Options[1].Position)
	}

	got, err := database.GetRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation: %v", err)
	}
	if len(got.Options) != 2 {
		t.Fatalf("got.Options len = %d, want 2", len(got.Options))
	}
	if got.Options[0].StateChange != sharedtypes.StateChangeClose {
		t.Fatalf("got.Options[0].StateChange = %q, want close", got.Options[0].StateChange)
	}
	if got.Options[1].StateChange != sharedtypes.StateChangeNone {
		t.Fatalf("got.Options[1].StateChange = %q, want none", got.Options[1].StateChange)
	}
}

func TestInsertRecommendationStoresRerunInstructions(t *testing.T) {
	database := openTestDB(t)
	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID: "kunchenguid/ezoss#42", RepoID: "kunchenguid/ezoss",
		Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "x", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	rec, err := database.InsertRecommendation(NewRecommendation{
		ItemID:            "kunchenguid/ezoss#42",
		Agent:             sharedtypes.AgentClaude,
		Model:             "claude-test",
		RerunInstructions: "Focus on whether this is safe to close after maintainer clarification.",
		Options: []NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	got, err := database.GetRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation() error = %v", err)
	}
	if got.RerunInstructions != "Focus on whether this is safe to close after maintainer clarification." {
		t.Fatalf("RerunInstructions = %q", got.RerunInstructions)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 1 || active[0].RerunInstructions != got.RerunInstructions {
		t.Fatalf("active recommendations = %#v, want rerun instructions", active)
	}
}

func TestInsertRecommendationReplacingActiveBeforeKeepsSameSecondNewerRecommendation(t *testing.T) {
	database := openTestDB(t)
	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID: "kunchenguid/ezoss#42", RepoID: "kunchenguid/ezoss",
		Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "x", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	cutoff := time.Now()
	time.Sleep(time.Millisecond)
	newer, err := database.InsertRecommendation(NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceHigh,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() newer error = %v", err)
	}
	if _, err := database.sql.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, cutoff.Unix(), newer.ID); err != nil {
		t.Fatalf("force same-second created_at: %v", err)
	}

	_, inserted, err := database.InsertRecommendationReplacingActiveBefore(NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeClose,
			Confidence:  sharedtypes.ConfidenceLow,
		}},
	}, cutoff)
	if err != nil {
		t.Fatalf("InsertRecommendationReplacingActiveBefore() error = %v", err)
	}
	if inserted {
		t.Fatal("InsertRecommendationReplacingActiveBefore() inserted stale same-second result")
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 1 || active[0].ID != newer.ID {
		t.Fatalf("active recommendations = %#v, want only newer %s", active, newer.ID)
	}
}

func TestInsertRecommendationRejectsEmptyOptions(t *testing.T) {
	database := openTestDB(t)
	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID: "kunchenguid/ezoss#1", RepoID: "kunchenguid/ezoss",
		Kind: sharedtypes.ItemKindIssue, Number: 1, State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	if _, err := database.InsertRecommendation(NewRecommendation{
		ItemID: "kunchenguid/ezoss#1",
		Agent:  sharedtypes.AgentClaude,
	}); err == nil {
		t.Fatal("expected error inserting recommendation with no options")
	}
}

func TestApprovalRoundTrip(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:        "kunchenguid/ezoss#42",
		RepoID:    "kunchenguid/ezoss",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		Author:    "octocat",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	rec, err := database.InsertRecommendation(singleOptionRec("kunchenguid/ezoss#42", sharedtypes.AgentClaude, NewRecommendationOption{
		StateChange: sharedtypes.StateChangeNone,
		Confidence:  sharedtypes.ConfidenceMedium,
	}))
	if err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}

	actedAt := time.Unix(1713000000, 0)
	approval, err := database.InsertApproval(NewApproval{
		RecommendationID: rec.ID,
		OptionID:         rec.Options[0].ID,
		Decision:         sharedtypes.ApprovalDecisionApproved,
		FinalComment:     "Approved as drafted.",
		FinalLabels:      []string{"bug"},
		FinalStateChange: sharedtypes.StateChangeNone,
		ActedAt:          &actedAt,
	})
	if err != nil {
		t.Fatalf("insert approval: %v", err)
	}

	got, err := database.GetApproval(approval.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got == nil {
		t.Fatal("expected approval")
	}
	if got.Decision != approval.Decision {
		t.Fatalf("decision = %q, want %q", got.Decision, approval.Decision)
	}
	if got.OptionID != rec.Options[0].ID {
		t.Fatalf("OptionID = %q, want %q", got.OptionID, rec.Options[0].ID)
	}
	if len(got.FinalLabels) != 1 || got.FinalLabels[0] != "bug" {
		t.Fatalf("final labels = %#v", got.FinalLabels)
	}
	if got.ActedAt == nil || !got.ActedAt.Equal(actedAt) {
		t.Fatalf("acted_at = %v, want %v", got.ActedAt, actedAt)
	}
	if got.CreatedAt == 0 {
		t.Fatal("expected created_at to be set")
	}
}

func TestListActiveRecommendationsReturnsNewestFirst(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	older, err := database.InsertRecommendation(singleOptionRec("kunchenguid/ezoss#42", sharedtypes.AgentClaude, NewRecommendationOption{
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceLow,
		DraftComment: "Older recommendation",
	}))
	if err != nil {
		t.Fatalf("insert older recommendation: %v", err)
	}
	newer, err := database.InsertRecommendation(singleOptionRec("kunchenguid/ezoss#42", sharedtypes.AgentClaude, NewRecommendationOption{
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceHigh,
		DraftComment: "Newer recommendation",
	}))
	if err != nil {
		t.Fatalf("insert newer recommendation: %v", err)
	}

	if _, err := database.sql.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, 1713000000, older.ID); err != nil {
		t.Fatalf("set older created_at: %v", err)
	}
	if _, err := database.sql.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, 1713000300, newer.ID); err != nil {
		t.Fatalf("set newer created_at: %v", err)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("list active recommendations: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("len(active) = %d, want 2", len(active))
	}
	if active[0].ID != newer.ID || active[1].ID != older.ID {
		t.Fatalf("recommendation order = [%q, %q], want [%q, %q]", active[0].ID, active[1].ID, newer.ID, older.ID)
	}
}

func TestDismissOptionCreatesDismissalApprovalAndSupersedesRecommendation(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	rec, err := database.InsertRecommendation(singleOptionRec("kunchenguid/ezoss#42", sharedtypes.AgentClaude, NewRecommendationOption{
		StateChange: sharedtypes.StateChangeNone,
		Confidence:  sharedtypes.ConfidenceMedium,
	}))
	if err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}

	actedAt := time.Unix(1713000000, 0)
	approval, err := database.DismissOption(rec.Options[0].ID, []string{"ezoss/triaged"}, actedAt)
	if err != nil {
		t.Fatalf("DismissOption() error = %v", err)
	}
	if approval.Decision != sharedtypes.ApprovalDecisionDismissed {
		t.Fatalf("approval decision = %q, want %q", approval.Decision, sharedtypes.ApprovalDecisionDismissed)
	}
	if approval.RecommendationID != rec.ID {
		t.Fatalf("approval recommendation id = %q, want %q", approval.RecommendationID, rec.ID)
	}
	if approval.OptionID != rec.Options[0].ID {
		t.Fatalf("approval option id = %q, want %q", approval.OptionID, rec.Options[0].ID)
	}

	storedRec, err := database.GetRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation() error = %v", err)
	}
	if storedRec == nil || storedRec.SupersededAt == nil || !storedRec.SupersededAt.Equal(actedAt) {
		t.Fatalf("stored recommendation = %#v, want superseded at %v", storedRec, actedAt)
	}
}

func TestApproveOptionPicksAlternateOption(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertRepo(Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("upsert repo: %v", err)
	}
	if err := database.UpsertItem(Item{
		ID:     "kunchenguid/ezoss#42",
		RepoID: "kunchenguid/ezoss",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	rec, err := database.InsertRecommendation(NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []NewRecommendationOption{
			{StateChange: sharedtypes.StateChangeClose, DraftComment: "Closing.", Confidence: sharedtypes.ConfidenceHigh, WaitingOn: sharedtypes.WaitingOnContributor},
			{StateChange: sharedtypes.StateChangeNone, DraftComment: "Ping.", Confidence: sharedtypes.ConfidenceMedium, WaitingOn: sharedtypes.WaitingOnContributor},
		},
	})
	if err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}

	// Approve the alternate (position 1).
	chosen := rec.Options[1]
	actedAt := time.Unix(1713000000, 0)
	approval, err := database.ApproveOption(chosen.ID, "Ping.", []string{"ezoss/triaged"}, sharedtypes.StateChangeNone, actedAt)
	if err != nil {
		t.Fatalf("ApproveOption: %v", err)
	}
	if approval.OptionID != chosen.ID {
		t.Fatalf("approval option_id = %q, want %q", approval.OptionID, chosen.ID)
	}
	if approval.RecommendationID != rec.ID {
		t.Fatalf("approval recommendation_id = %q, want %q", approval.RecommendationID, rec.ID)
	}
	if approval.FinalStateChange != sharedtypes.StateChangeNone {
		t.Fatalf("approval final_state_change = %q, want none", approval.FinalStateChange)
	}

	storedRec, err := database.GetRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation: %v", err)
	}
	if storedRec.SupersededAt == nil || !storedRec.SupersededAt.Equal(actedAt) {
		t.Fatalf("recommendation superseded_at = %v, want %v", storedRec.SupersededAt, actedAt)
	}
}

func TestBackfillRecommendationOptionsMigratesLegacyRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite")
	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer rawDB.Close()
	rawDB.SetMaxOpenConns(1)

	// Create the legacy schema (recommendations only, no options table).
	legacySchema := `
CREATE TABLE repos (id TEXT PRIMARY KEY, default_branch TEXT, last_poll_at INTEGER, last_triaged_refresh_at INTEGER, created_at INTEGER NOT NULL);
CREATE TABLE items (id TEXT PRIMARY KEY, repo_id TEXT NOT NULL REFERENCES repos(id), kind TEXT NOT NULL, number INTEGER NOT NULL, title TEXT, author TEXT, state TEXT, is_draft INTEGER NOT NULL DEFAULT 0, gh_triaged INTEGER NOT NULL DEFAULT 0, waiting_on TEXT, last_event_at INTEGER, stale_since INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE recommendations (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, agent TEXT NOT NULL, model TEXT, rationale TEXT, draft_comment TEXT, followups TEXT, proposed_labels TEXT, state_change TEXT, confidence TEXT, tokens_in INTEGER, tokens_out INTEGER, created_at INTEGER NOT NULL, superseded_at INTEGER);
CREATE TABLE approvals (id TEXT PRIMARY KEY, recommendation_id TEXT NOT NULL REFERENCES recommendations(id) ON DELETE CASCADE, decision TEXT NOT NULL, final_comment TEXT, final_labels TEXT, final_state_change TEXT, acted_at INTEGER, acted_error TEXT, created_at INTEGER NOT NULL);
`
	if _, err := rawDB.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	// Insert a legacy item + recommendation + approval.
	if _, err := rawDB.Exec(`INSERT INTO repos (id, default_branch, created_at) VALUES (?, 'main', 1)`, "kunchenguid/ezoss"); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if _, err := rawDB.Exec(`INSERT INTO items (id, repo_id, kind, number, title, state, waiting_on, created_at, updated_at) VALUES (?, ?, 'issue', 1, 'x', 'open', 'contributor', 1, 1)`, "kunchenguid/ezoss#1", "kunchenguid/ezoss"); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := rawDB.Exec(`INSERT INTO recommendations (id, item_id, agent, model, rationale, draft_comment, proposed_labels, state_change, confidence, tokens_in, tokens_out, created_at) VALUES (?, ?, 'claude', 'sonnet', ?, ?, ?, ?, ?, 0, 0, 100)`,
		"rec-1", "kunchenguid/ezoss#1", "old rationale", "old draft", `["bug"]`, sharedtypes.StateChangeClose, sharedtypes.ConfidenceHigh,
	); err != nil {
		t.Fatalf("seed legacy rec: %v", err)
	}
	if _, err := rawDB.Exec(`INSERT INTO approvals (id, recommendation_id, decision, final_comment, final_labels, final_state_change, acted_at, acted_error, created_at) VALUES (?, ?, 'approved', '', '[]', 'none', 200, '', 200)`, "appr-1", "rec-1"); err != nil {
		t.Fatalf("seed legacy approval: %v", err)
	}

	if err := rawDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Open with the new code path - this should run the migration.
	migrated, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(legacy): %v", err)
	}
	defer migrated.Close()

	rec, err := migrated.GetRecommendation("rec-1")
	if err != nil {
		t.Fatalf("GetRecommendation: %v", err)
	}
	if rec == nil {
		t.Fatal("legacy recommendation lost during migration")
	}
	if len(rec.Options) != 1 {
		t.Fatalf("len(Options) = %d, want 1 after backfill", len(rec.Options))
	}
	opt := rec.Options[0]
	if opt.Position != 0 {
		t.Fatalf("Position = %d, want 0", opt.Position)
	}
	if opt.Rationale != "old rationale" || opt.DraftComment != "old draft" {
		t.Fatalf("legacy fields not copied: %#v", opt)
	}
	if opt.StateChange != sharedtypes.StateChangeClose || opt.Confidence != sharedtypes.ConfidenceHigh {
		t.Fatalf("state/confidence = %q/%q", opt.StateChange, opt.Confidence)
	}
	if len(opt.ProposedLabels) != 1 || opt.ProposedLabels[0] != "bug" {
		t.Fatalf("ProposedLabels = %#v", opt.ProposedLabels)
	}
	if opt.WaitingOn != sharedtypes.WaitingOnContributor {
		t.Fatalf("WaitingOn = %q, want contributor (copied from item)", opt.WaitingOn)
	}

	approval, err := migrated.GetApproval("appr-1")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if approval == nil || approval.OptionID != opt.ID {
		t.Fatalf("legacy approval option_id = %q, want %q", approvalOption(approval), opt.ID)
	}

	// Idempotency: a second Open shouldn't create more options.
	if err := migrated.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	migrated2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(migrated): %v", err)
	}
	defer migrated2.Close()
	rec2, err := migrated2.GetRecommendation("rec-1")
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if len(rec2.Options) != 1 {
		t.Fatalf("re-open len(Options) = %d, want 1", len(rec2.Options))
	}
}

func approvalOption(a *Approval) string {
	if a == nil {
		return ""
	}
	return a.OptionID
}
