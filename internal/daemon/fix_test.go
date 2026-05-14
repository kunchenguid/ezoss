package daemon

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func openDaemonTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestPollOnceRunsQueuedFixJobBeforeTriage(t *testing.T) {
	database := openDaemonTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, State: sharedtypes.ItemStateOpen}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}

	runner := &stubFixRunner{result: &FixResult{Branch: "ezoss/fix-42", WorktreePath: "/tmp/w", PRURL: "https://github.com/acme/widgets/pull/99"}}
	triage := &recordingTriageRunner{}
	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Triage: triage, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(runner.ran) != 1 || runner.ran[0].ID != job.ID {
		t.Fatalf("fix runner jobs = %#v, want %s", runner.ran, job.ID)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusSucceeded || got.PRURL == "" {
		t.Fatalf("job after PollOnce = %#v, want succeeded with PR URL", got)
	}
	if triage.calls != 0 {
		t.Fatalf("triage calls = %d, want fix stage before triage to consume cycle", triage.calls)
	}
}

func TestPollOnceMarksFixJobWaitingForPR(t *testing.T) {
	database := openDaemonTestDB(t)
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	runner := &stubFixRunner{result: &FixResult{Branch: "ezoss/fix-42", WorktreePath: "/tmp/w", WaitingForPR: true}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusRunning || got.Phase != db.FixJobPhaseWaitingForPR || got.Message != "waiting for no-mistakes pipeline to finish" {
		t.Fatalf("job after PollOnce = %#v, want running/waiting_for_pr with no-mistakes wait message", got)
	}
}

func TestPollOnceMarksFixJobWaitingForManualReview(t *testing.T) {
	database := openDaemonTestDB(t)
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	runner := &stubFixRunner{result: &FixResult{Branch: "fix-race", WorktreePath: "/tmp/w", WaitingForManualReview: true}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusRunning || got.Phase != db.FixJobPhaseWaitingForPR || got.Message != "waiting for manual review" {
		t.Fatalf("job after PollOnce = %#v, want running manual review wait", got)
	}
}

func TestPollOnceDetectsWaitingFixPR(t *testing.T) {
	database := openDaemonTestDB(t)
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseWaitingForPR, Branch: "ezoss/fix-42"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	runner := &stubFixRunner{detectedPR: "https://github.com/acme/widgets/pull/99"}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusSucceeded || got.PRURL == "" {
		t.Fatalf("job after detection = %#v, want succeeded", got)
	}
}

func TestPollOnceKeepsWaitingFixJobRunningWhenPRDetectionFails(t *testing.T) {
	database := openDaemonTestDB(t)
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseWaitingForPR, Branch: "ezoss/fix-42"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	runner := &stubFixRunner{err: errors.New("gh temporarily unavailable")}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v, want detection error to be retried later", err)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusRunning || got.Phase != db.FixJobPhaseWaitingForPR || got.Error != "" {
		t.Fatalf("job after detection error = %#v, want still running/waiting_for_pr without stored error", got)
	}
}

func TestPollOnceChecksLaterWaitingFixJobsWhenEarlierPRIsMissing(t *testing.T) {
	database := openDaemonTestDB(t)
	first, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#41", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 41, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix first.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() first error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() first error = %v", err)
	}
	if err := database.UpdateFixJob(first.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseWaitingForPR, Branch: "ezoss/fix-41"}); err != nil {
		t.Fatalf("UpdateFixJob() first error = %v", err)
	}
	second, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-2", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix second.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() second error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() second error = %v", err)
	}
	if err := database.UpdateFixJob(second.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseWaitingForPR, Branch: "ezoss/fix-42"}); err != nil {
		t.Fatalf("UpdateFixJob() second error = %v", err)
	}
	runner := &stubFixRunner{detectedByJob: map[string]string{second.ID: "https://github.com/acme/widgets/pull/99"}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	got, err := database.GetFixJob(second.ID)
	if err != nil {
		t.Fatalf("GetFixJob() second error = %v", err)
	}
	if got.Status != db.FixJobStatusSucceeded || got.PRURL == "" {
		t.Fatalf("second job after detection = %#v, want succeeded", got)
	}
}

func TestPollOnceRunsQueuedFixJobAfterWaitingPRDetectionError(t *testing.T) {
	database := openDaemonTestDB(t)
	waiting, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#41", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 41, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix first.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() waiting error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() waiting error = %v", err)
	}
	if err := database.UpdateFixJob(waiting.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseWaitingForPR, Branch: "ezoss/fix-41"}); err != nil {
		t.Fatalf("UpdateFixJob() waiting error = %v", err)
	}
	queued, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-2", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix second.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() queued error = %v", err)
	}
	runner := &stubFixRunner{errByDetectJob: map[string]error{waiting.ID: errors.New("gh temporarily unavailable")}, result: &FixResult{Branch: "ezoss/fix-42", WorktreePath: "/tmp/w", PRURL: "https://github.com/acme/widgets/pull/99"}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(runner.ran) != 1 || runner.ran[0].ID != queued.ID {
		t.Fatalf("fix runner jobs = %#v, want queued job %s", runner.ran, queued.ID)
	}
	gotWaiting, err := database.GetFixJob(waiting.ID)
	if err != nil {
		t.Fatalf("GetFixJob() waiting error = %v", err)
	}
	if gotWaiting.Status != db.FixJobStatusRunning || gotWaiting.Phase != db.FixJobPhaseWaitingForPR {
		t.Fatalf("waiting job = %#v, want still waiting", gotWaiting)
	}
}

func TestPollOnceReclaimsStaleRunningFixJob(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.sqlite")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rawDB, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	stale, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#41", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 41, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix first.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() stale error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() stale error = %v", err)
	}
	if err := database.UpdateFixJob(stale.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseRunningAgent}); err != nil {
		t.Fatalf("UpdateFixJob() stale error = %v", err)
	}
	if _, err := rawDB.Exec(`UPDATE fix_jobs SET updated_at = ? WHERE id = ?`, time.Now().Add(-2*time.Hour).Unix(), stale.ID); err != nil {
		t.Fatalf("force old updated_at: %v", err)
	}
	queued, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-2", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix second.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() queued error = %v", err)
	}
	runner := &stubFixRunner{result: &FixResult{Branch: "ezoss/fix-42", WorktreePath: "/tmp/w", PRURL: "https://github.com/acme/widgets/pull/99"}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner, PerFixJobTimeout: time.Hour}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	gotStale, err := database.GetFixJob(stale.ID)
	if err != nil {
		t.Fatalf("GetFixJob() stale error = %v", err)
	}
	if gotStale.Status != db.FixJobStatusFailed || gotStale.Phase != db.FixJobPhaseFailed {
		t.Fatalf("stale job = %#v, want failed", gotStale)
	}
	if len(runner.ran) != 1 || runner.ran[0].ID != queued.ID {
		t.Fatalf("fix runner jobs = %#v, want queued job %s", runner.ran, queued.ID)
	}
}

func TestPollOnceRunsFixJobWithTimeout(t *testing.T) {
	database := openDaemonTestDB(t)
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	runner := &deadlineCheckingFixRunner{result: &FixResult{Branch: "ezoss/fix-42", WorktreePath: "/tmp/w", PRURL: "https://github.com/acme/widgets/pull/99"}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner, PerFixJobTimeout: time.Minute}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if !runner.sawDeadline {
		t.Fatalf("RunFix() context had no deadline")
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusSucceeded {
		t.Fatalf("job status = %q, want succeeded", got.Status)
	}
}

func TestPollOnceLeavesCancelledJobAfterDetectRace(t *testing.T) {
	database := openDaemonTestDB(t)
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, FixPrompt: "Fix it.", PRCreate: "no-mistakes"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if _, err := database.ClaimNextQueuedFixJob(); err != nil {
		t.Fatalf("ClaimNextQueuedFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusRunning, Phase: db.FixJobPhaseWaitingForPR, Branch: "ezoss/fix-42"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	runner := &cancellingDetectFixRunner{database: database, jobID: job.ID, detectedPR: "https://github.com/acme/widgets/pull/99"}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: emptyTriageLister{}, Fix: runner}, []string{"acme/widgets"}); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	got, err := database.GetFixJob(job.ID)
	if err != nil {
		t.Fatalf("GetFixJob() error = %v", err)
	}
	if got.Status != db.FixJobStatusCancelled {
		t.Fatalf("job status = %q, want cancelled (detected URL must not clobber supersede)", got.Status)
	}
	if got.PRURL != "" {
		t.Fatalf("job PR URL = %q, want empty (URL must be discarded after cancellation)", got.PRURL)
	}
}

func TestPollOnceRefreshesSucceededFixJobSourceAndPRState(t *testing.T) {
	database := openDaemonTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", State: sharedtypes.ItemStateOpen, GHTriaged: true}); err != nil {
		t.Fatalf("UpsertItem(source) error = %v", err)
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, Title: "panic", FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: "https://github.com/acme/widgets/pull/99"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	github := &stubFixJobItemGetter{items: map[string]ghclient.Item{
		"acme/widgets issue 42": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", Author: "alice", State: sharedtypes.ItemStateClosed, UpdatedAt: time.Unix(1713000000, 0)},
		"acme/widgets pr 99":    {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, Title: "fix panic", Author: "kun", State: sharedtypes.ItemStateMerged, UpdatedAt: time.Unix(1713000001, 0)},
	}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	source, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem(source) error = %v", err)
	}
	if source == nil || source.State != sharedtypes.ItemStateClosed {
		t.Fatalf("source item = %#v, want closed", source)
	}
	pr, err := database.GetItem("acme/widgets#99")
	if err != nil {
		t.Fatalf("GetItem(PR) error = %v", err)
	}
	if pr == nil || pr.State != sharedtypes.ItemStateMerged {
		t.Fatalf("fix PR item = %#v, want merged", pr)
	}
}

func TestPollOnceMarksOpenFixPRLocallyTriagedWhenRefreshingSucceededFixJob(t *testing.T) {
	database := openDaemonTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", State: sharedtypes.ItemStateOpen, GHTriaged: true}); err != nil {
		t.Fatalf("UpsertItem(source) error = %v", err)
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, Title: "panic", FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: "https://github.com/acme/widgets/pull/99"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	github := &stubFixJobItemGetter{items: map[string]ghclient.Item{
		"acme/widgets issue 42": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", Author: "alice", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000000, 0)},
		"acme/widgets pr 99":    {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, Title: "fix panic", Author: "kun", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000001, 0)},
	}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	pr, err := database.GetItem("acme/widgets#99")
	if err != nil {
		t.Fatalf("GetItem(PR) error = %v", err)
	}
	if pr == nil || !pr.GHTriaged {
		t.Fatalf("fix PR item = %#v, want locally triaged", pr)
	}
}

func TestPollOnceDoesNotRefreshSucceededFixJobEveryCycle(t *testing.T) {
	database := openDaemonTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", State: sharedtypes.ItemStateOpen, GHTriaged: true}); err != nil {
		t.Fatalf("UpsertItem(source) error = %v", err)
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, Title: "panic", FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: "https://github.com/acme/widgets/pull/99"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	github := &stubFixJobItemGetter{items: map[string]ghclient.Item{
		"acme/widgets issue 42": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", Author: "alice", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000000, 0)},
		"acme/widgets pr 99":    {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, Title: "fix panic", Author: "kun", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000001, 0)},
	}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("first PollOnce() error = %v", err)
	}
	firstCalls := github.calls
	if firstCalls != 2 {
		t.Fatalf("GetItem calls after first poll = %d, want 2", firstCalls)
	}
	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("second PollOnce() error = %v", err)
	}
	if github.calls != firstCalls {
		t.Fatalf("GetItem calls after second poll = %d, want %d", github.calls, firstCalls)
	}
}

func TestPollOnceContinuesRefreshingSucceededFixJobsAfterGetItemFailure(t *testing.T) {
	database := openDaemonTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	for _, number := range []int{41, 42} {
		if err := database.UpsertItem(db.Item{ID: "acme/widgets#" + strconv.Itoa(number), RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: number, Title: "panic", State: sharedtypes.ItemStateOpen, GHTriaged: true}); err != nil {
			t.Fatalf("UpsertItem(%d) error = %v", number, err)
		}
		job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#" + strconv.Itoa(number), RecommendationID: "rec-" + strconv.Itoa(number), RepoID: "acme/widgets", ItemNumber: number, ItemKind: sharedtypes.ItemKindIssue, Title: "panic", FixPrompt: "Fix it.", PRCreate: "gh"})
		if err != nil {
			t.Fatalf("CreateFixJob(%d) error = %v", number, err)
		}
		if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: "https://github.com/acme/widgets/pull/" + strconv.Itoa(number+50)}); err != nil {
			t.Fatalf("UpdateFixJob(%d) error = %v", number, err)
		}
	}
	github := &stubFixJobItemGetter{
		items: map[string]ghclient.Item{
			"acme/widgets issue 42": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "panic", Author: "alice", State: sharedtypes.ItemStateClosed, UpdatedAt: time.Unix(1713000000, 0)},
			"acme/widgets pr 92":    {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 92, Title: "fix panic", Author: "kun", State: sharedtypes.ItemStateMerged, UpdatedAt: time.Unix(1713000001, 0)},
		},
		errs: map[string]error{
			"acme/widgets issue 41": errors.New("not found"),
		},
	}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	source, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem(source) error = %v", err)
	}
	if source == nil || source.State != sharedtypes.ItemStateClosed {
		t.Fatalf("source item = %#v, want closed despite earlier refresh failure", source)
	}
	pr, err := database.GetItem("acme/widgets#92")
	if err != nil {
		t.Fatalf("GetItem(PR) error = %v", err)
	}
	if pr == nil || pr.State != sharedtypes.ItemStateMerged {
		t.Fatalf("fix PR item = %#v, want merged despite earlier refresh failure", pr)
	}
}

func TestPollOncePreservesContributorRepoSourceWhenRefreshingSucceededFixJob(t *testing.T) {
	database := openDaemonTestDB(t)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", Source: db.RepoSourceContrib}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindPR, Role: sharedtypes.RoleContributor, Number: 42, Title: "panic", State: sharedtypes.ItemStateOpen, GHTriaged: true}); err != nil {
		t.Fatalf("UpsertItem(source) error = %v", err)
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindPR, Title: "panic", FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: "https://github.com/acme/widgets/pull/99"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	github := &stubFixJobItemGetter{items: map[string]ghclient.Item{
		"acme/widgets pr 42": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 42, Title: "panic", Author: "alice", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000000, 0)},
		"acme/widgets pr 99": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, Title: "fix panic", Author: "kun", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000001, 0)},
	}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	repo, err := database.GetRepo("acme/widgets")
	if err != nil {
		t.Fatalf("GetRepo() error = %v", err)
	}
	if repo == nil || repo.Source != db.RepoSourceContrib {
		t.Fatalf("repo = %#v, want contributor source preserved", repo)
	}
}

func TestPollOncePreservesContributorSweepMetadataWhenRefreshingSucceededFixJob(t *testing.T) {
	database := openDaemonTestDB(t)
	lastSeen := time.Unix(1712990000, 0).UTC()
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", Source: db.RepoSourceContrib}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindPR, Role: sharedtypes.RoleContributor, Number: 42, Title: "panic", State: sharedtypes.ItemStateOpen, GHTriaged: true, LastSeenUpdatedAt: &lastSeen, LastSeenCommentID: 123}); err != nil {
		t.Fatalf("UpsertItem(source) error = %v", err)
	}
	job, err := database.CreateFixJob(db.NewFixJob{ItemID: "acme/widgets#42", RecommendationID: "rec-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindPR, Title: "panic", FixPrompt: "Fix it.", PRCreate: "gh"})
	if err != nil {
		t.Fatalf("CreateFixJob() error = %v", err)
	}
	if err := database.UpdateFixJob(job.ID, db.FixJobUpdate{Status: db.FixJobStatusSucceeded, Phase: db.FixJobPhasePROpened, PRURL: "https://github.com/acme/widgets/pull/99"}); err != nil {
		t.Fatalf("UpdateFixJob() error = %v", err)
	}
	github := &stubFixJobItemGetter{items: map[string]ghclient.Item{
		"acme/widgets pr 42": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 42, Title: "panic", Author: "alice", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000000, 0)},
		"acme/widgets pr 99": {Repo: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 99, Title: "fix panic", Author: "kun", State: sharedtypes.ItemStateOpen, UpdatedAt: time.Unix(1713000001, 0)},
	}}

	if err := PollOnce(context.Background(), Poller{DB: database, GitHub: github, Fix: &stubFixRunner{}}, nil); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || item.LastSeenUpdatedAt == nil || !item.LastSeenUpdatedAt.Equal(lastSeen) || item.LastSeenCommentID != 123 {
		t.Fatalf("item = %#v, want contributor sweep metadata preserved", item)
	}
}

// cancellingDetectFixRunner simulates a supersede that lands while DetectPR
// is mid-network-call: the job is marked cancelled before DetectPR returns.
type cancellingDetectFixRunner struct {
	database   *db.DB
	jobID      string
	detectedPR string
}

func (c *cancellingDetectFixRunner) RunFix(context.Context, db.FixJob, func(db.FixJobUpdate) error) (*FixResult, error) {
	return nil, errors.New("RunFix should not be called")
}

func (c *cancellingDetectFixRunner) DetectPR(context.Context, db.FixJob) (string, error) {
	if err := c.database.UpdateFixJob(c.jobID, db.FixJobUpdate{Status: db.FixJobStatusCancelled, Message: "superseded by newer fix request"}); err != nil {
		return "", err
	}
	return c.detectedPR, nil
}

type stubFixRunner struct {
	result         *FixResult
	err            error
	detectedPR     string
	detectedByJob  map[string]string
	errByDetectJob map[string]error
	ran            []db.FixJob
}

func (s *stubFixRunner) RunFix(_ context.Context, job db.FixJob, progress func(db.FixJobUpdate) error) (*FixResult, error) {
	s.ran = append(s.ran, job)
	if progress != nil {
		_ = progress(db.FixJobUpdate{Phase: db.FixJobPhaseRunningAgent, Message: "running agent"})
	}
	return s.result, s.err
}

func (s *stubFixRunner) DetectPR(_ context.Context, job db.FixJob) (string, error) {
	if s.errByDetectJob != nil && s.errByDetectJob[job.ID] != nil {
		return "", s.errByDetectJob[job.ID]
	}
	if s.err != nil {
		return "", s.err
	}
	if s.detectedByJob != nil {
		return s.detectedByJob[job.ID], nil
	}
	return s.detectedPR, nil
}

type deadlineCheckingFixRunner struct {
	result      *FixResult
	sawDeadline bool
}

func (s *deadlineCheckingFixRunner) RunFix(ctx context.Context, _ db.FixJob, _ func(db.FixJobUpdate) error) (*FixResult, error) {
	_, s.sawDeadline = ctx.Deadline()
	if !s.sawDeadline {
		return nil, errors.New("missing deadline")
	}
	return s.result, nil
}

func (s *deadlineCheckingFixRunner) DetectPR(context.Context, db.FixJob) (string, error) {
	return "", nil
}

type recordingTriageRunner struct{ calls int }

func (r *recordingTriageRunner) Triage(context.Context, TriageRequest) (*TriageResult, error) {
	r.calls++
	return nil, errors.New("triage should not run")
}

type emptyTriageLister struct{}

func (emptyTriageLister) ListNeedingTriage(context.Context, string) ([]ghclient.Item, error) {
	return nil, nil
}

func (emptyTriageLister) ListTriaged(context.Context, string, time.Time) ([]ghclient.Item, error) {
	return nil, nil
}

type stubFixJobItemGetter struct {
	items map[string]ghclient.Item
	errs  map[string]error
	calls int
}

func (s stubFixJobItemGetter) ListNeedingTriage(context.Context, string) ([]ghclient.Item, error) {
	return nil, nil
}

func (s stubFixJobItemGetter) ListTriaged(context.Context, string, time.Time) ([]ghclient.Item, error) {
	return nil, nil
}

func (s *stubFixJobItemGetter) GetItem(_ context.Context, repo string, kind sharedtypes.ItemKind, number int) (ghclient.Item, error) {
	s.calls++
	key := repo + " " + string(kind) + " " + strconv.Itoa(number)
	if s.errs != nil && s.errs[key] != nil {
		return ghclient.Item{}, s.errs[key]
	}
	return s.items[key], nil
}
