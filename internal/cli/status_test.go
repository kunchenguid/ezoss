package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestRootCommandIncludesStatusSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"status"})
	if err != nil {
		t.Fatalf("Find(status) error = %v", err)
	}
	if got == nil || got.Name() != "status" {
		t.Fatalf("Find(status) = %v, want status command", got)
	}
}

func TestStatusCommandPrintsConfiguredReposWithoutDB(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "pending=0 repos=2 daemon=stopped\n" {
		t.Fatalf("output = %q, want %q", got, "pending=0 repos=2 daemon=stopped\n")
	}
}

func TestStatusCommandPrintsPendingRecommendationCount(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "kunchenguid/ezoss#42",
		RepoID:    "kunchenguid/ezoss",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		Author:    "octocat",
		State:     sharedtypes.ItemStateOpen,
		GHTriaged: false,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence: sharedtypes.ConfidenceMedium,
			
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "pending=1 repos=2 daemon=stopped\n" {
		t.Fatalf("output = %q, want %q", got, "pending=1 repos=2 daemon=stopped\n")
	}
}

func TestStatusCommandPrintsUnconfiguredPendingRecommendationCount(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "kunchenguid/ezoss", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "kunchenguid/ezoss#42",
		RepoID:    "kunchenguid/ezoss",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		Author:    "octocat",
		State:     sharedtypes.ItemStateOpen,
		GHTriaged: false,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "kunchenguid/ezoss#42",
		Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence: sharedtypes.ConfidenceMedium,
			
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "pending=1 repos=0 daemon=stopped unconfigured=1\n" {
		t.Fatalf("output = %q, want %q", got, "pending=1 repos=0 daemon=stopped unconfigured=1\n")
	}
}

func TestStatusCommandPrintsConfiguredAndUnconfiguredPendingRecommendationCounts(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	for _, item := range []db.Item{
		{
			ID:        "kunchenguid/ezoss#42",
			RepoID:    "kunchenguid/ezoss",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    42,
			Title:     "Bug: triage queue stalls",
			Author:    "octocat",
			State:     sharedtypes.ItemStateOpen,
			GHTriaged: false,
		},
		{
			ID:        "owner/repo#7",
			RepoID:    "owner/repo",
			Kind:      sharedtypes.ItemKindPR,
			Number:    7,
			Title:     "feat: add streaming to mock agent",
			Author:    "octocat",
			State:     sharedtypes.ItemStateOpen,
			GHTriaged: false,
		},
	} {
		if err := database.UpsertRepo(db.Repo{ID: item.RepoID, DefaultBranch: "main"}); err != nil {
			t.Fatalf("UpsertRepo(%s) error = %v", item.RepoID, err)
		}
		if err := database.UpsertItem(item); err != nil {
			t.Fatalf("UpsertItem(%s) error = %v", item.ID, err)
		}
	}

	for _, recommendation := range []db.NewRecommendation{
		{
			ItemID: "kunchenguid/ezoss#42",
			Agent:  sharedtypes.AgentClaude,
			Options: []db.NewRecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Confidence:  sharedtypes.ConfidenceMedium,
			}},
		},
		{
			ItemID: "owner/repo#7",
			Agent:  sharedtypes.AgentClaude,
			Options: []db.NewRecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Confidence:  sharedtypes.ConfidenceLow,
			}},
		},
	} {
		if _, err := database.InsertRecommendation(recommendation); err != nil {
			t.Fatalf("InsertRecommendation(%s) error = %v", recommendation.ItemID, err)
		}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "pending=2 configured=1 repos=1 daemon=stopped unconfigured=1\n" {
		t.Fatalf("output = %q, want %q", got, "pending=2 configured=1 repos=1 daemon=stopped unconfigured=1\n")
	}
}

func TestStatusCommandExplainsDatabaseLockErrors(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalOpenDB := openDB
	t.Cleanup(func() {
		newPaths = originalNewPaths
		openDB = originalOpenDB
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	openDB = func(string) (*db.DB, error) {
		return nil, errors.New("migrate db: database is locked (261)")
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, "ezoss.db"), []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"status", "--short"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want database lock guidance")
	}
	msg := err.Error()
	for _, want := range []string{"database is locked", "another ezoss process may be using the database", "try again in a moment"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
	if strings.Contains(msg, "(261)") {
		t.Fatalf("error = %q, should hide raw sqlite error code", msg)
	}
}

func TestStatusCommandRetriesTransientDatabaseLock(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalOpenDB := openDB
	t.Cleanup(func() {
		newPaths = originalNewPaths
		openDB = originalOpenDB
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, "ezoss.db"), []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "retry.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	var attempts int32
	openDB = func(string) (*db.DB, error) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			return nil, errors.New("migrate db: database is locked (261)")
		}
		return database, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("open attempts = %d, want 2", got)
	}
	if got := buf.String(); got != "pending=0 repos=1 daemon=stopped\n" {
		t.Fatalf("output = %q, want %q", got, "pending=0 repos=1 daemon=stopped\n")
	}
}

func TestStatusCommandIncludesDaemonState(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalReadDaemonStatus := readDaemonStatus
	t.Cleanup(func() {
		newPaths = originalNewPaths
		readDaemonStatus = originalReadDaemonStatus
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	readDaemonStatus = func(string) (daemon.Status, error) {
		return daemon.Status{State: daemon.StateRunning, PID: 4321}, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "pending=0 repos=1 daemon=running\n" {
		t.Fatalf("output = %q, want %q", got, "pending=0 repos=1 daemon=running\n")
	}
}

func TestStatusCommandTracksTelemetryEvent(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(func() {
		newPaths = originalNewPaths
		resetTelemetry()
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status", "--short"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "status" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "status")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "status" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "status")
	}
}

func TestRenderRichStatusDaemonStoppedShowsHint(t *testing.T) {
	d := statusData{
		daemonState: daemon.StateStopped,
		repos:       []string{"acme/widgets"},
	}
	got := renderRichStatus(d, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"daemon: stopped",
		"repos: 1 configured",
		"0 pending recommendations",
		"sync:   not running",
		"ezoss daemon start",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render = %q, missing %q", got, want)
		}
	}
}

func TestRenderRichStatusUnconfiguredCountInSummary(t *testing.T) {
	d := statusData{
		daemonState:       daemon.StateStopped,
		repos:             []string{"acme/widgets"},
		pending:           3,
		configuredPending: 2,
		unconfigured:      1,
	}
	got := renderRichStatus(d, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(got, "3 pending recommendations (1 from unconfigured repo)") {
		t.Fatalf("render = %q, missing unconfigured suffix", got)
	}
}

func TestRenderRichStatusPerRepoLines(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	syncedAt := now.Add(-2 * time.Minute)
	startedAt := syncedAt.Add(-450 * time.Millisecond)
	syncingStart := now.Add(-3 * time.Second)

	d := statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   12345,
		repos:       []string{"acme/done", "acme/syncing", "acme/never", "acme/errored"},
		sync: &ipc.SyncStatusResult{
			Interval:       5 * time.Minute,
			CycleCount:     4,
			LastCycleStart: now.Add(-3 * time.Minute),
			LastCycleEnd:   now.Add(-2 * time.Minute),
			NextCycleAt:    now.Add(8 * time.Minute),
			CurrentRepo:    "acme/syncing",
			CurrentIndex:   2,
			Total:          4,
			Repos: []ipc.RepoSyncStatus{
				{Repo: "acme/done", LastSyncStart: startedAt, LastSyncEnd: syncedAt},
				{Repo: "acme/syncing", LastSyncStart: syncingStart, Syncing: true},
				{Repo: "acme/errored", LastSyncStart: startedAt, LastSyncEnd: syncedAt, LastError: "rate limited"},
			},
		},
	}
	got := renderRichStatus(d, now)
	for _, want := range []string{
		"daemon: running (pid 12345)",
		"Sync: cycle 4 finished 2m ago",
		"next in 8m",
		"✓ acme/done",
		"synced 2m ago (450ms)",
		"→ acme/syncing",
		"syncing... (3s in)",
		"·",                    // pending marker for never-synced
		"acme/never",           // never-synced row
		"pending first sync",
		"✗ acme/errored",
		"error: rate limited",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestRenderRichStatusShowsSyncPhaseHeader(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	syncingStart := now.Add(-3 * time.Second)

	d := statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   12345,
		repos:       []string{"acme/widgets", "acme/other"},
		sync: &ipc.SyncStatusResult{
			Interval:     5 * time.Minute,
			Phase:        "sync",
			CurrentRepo:  "acme/widgets",
			CurrentIndex: 1,
			Total:        2,
			Repos: []ipc.RepoSyncStatus{
				{Repo: "acme/widgets", LastSyncStart: syncingStart, Syncing: true},
			},
		},
	}
	got := renderRichStatus(d, now)
	if !strings.Contains(got, "Sync: data stage") {
		t.Fatalf("missing data-stage header in render: %s", got)
	}
	if !strings.Contains(got, "1 / 2") {
		t.Fatalf("missing repo progress (1/2) in render: %s", got)
	}
	if !strings.Contains(got, "syncing... (3s in)") {
		t.Fatalf("missing per-repo syncing line: %s", got)
	}
}

func TestRenderRichStatusShowsAgentsPhaseHeader(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	d := statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   12345,
		repos:       []string{"acme/widgets"},
		sync: &ipc.SyncStatusResult{
			Interval:    5 * time.Minute,
			Phase:       "agents",
			AgentsTotal: 12,
			AgentsDone:  5,
			CurrentItem: "acme/widgets#42",
			Repos: []ipc.RepoSyncStatus{
				{Repo: "acme/widgets", LastSyncStart: now.Add(-30 * time.Second), LastSyncEnd: now.Add(-25 * time.Second)},
			},
		},
	}
	got := renderRichStatus(d, now)
	if !strings.Contains(got, "Sync: agents stage") {
		t.Fatalf("missing agents-stage header in render: %s", got)
	}
	if !strings.Contains(got, "5 / 12") {
		t.Fatalf("missing agents progress (5/12) in render: %s", got)
	}
	if !strings.Contains(got, "acme/widgets#42") {
		t.Fatalf("missing current item in render: %s", got)
	}
}

func TestRenderRichStatusFlagsIPCFailure(t *testing.T) {
	d := statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   1,
		repos:       []string{"acme/widgets"},
		syncErr:     errors.New("dial broken"),
	}
	got := renderRichStatus(d, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(got, "sync:   unavailable (dial broken)") {
		t.Fatalf("render missing IPC failure note: %s", got)
	}
}

func TestStatusCommandQueriesIPCWhenDaemonRunning(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalReadDaemonStatus := readDaemonStatus
	originalDialDaemonIPC := dialDaemonIPC
	t.Cleanup(func() {
		newPaths = originalNewPaths
		readDaemonStatus = originalReadDaemonStatus
		dialDaemonIPC = originalDialDaemonIPC
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	readDaemonStatus = func(string) (daemon.Status, error) {
		return daemon.Status{State: daemon.StateRunning, PID: 4321}, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"acme/widgets"},
	}); err != nil {
		t.Fatalf("SaveGlobal error = %v", err)
	}

	now := time.Now().UTC()
	dialDaemonIPC = func(_ string) (daemonIPCClient, error) {
		return stubDaemonIPCClient{call: func(method string, _ interface{}, result interface{}) error {
			if method != ipc.MethodSyncStatus {
				t.Fatalf("Call() method = %q, want %q", method, ipc.MethodSyncStatus)
			}
			out, ok := result.(*ipc.SyncStatusResult)
			if !ok {
				t.Fatalf("Call() result type = %T, want *ipc.SyncStatusResult", result)
			}
			*out = ipc.SyncStatusResult{
				Interval:       time.Minute,
				CycleCount:     1,
				LastCycleStart: now.Add(-30 * time.Second),
				LastCycleEnd:   now.Add(-25 * time.Second),
				NextCycleAt:    now.Add(35 * time.Second),
				Total:          1,
				Repos: []ipc.RepoSyncStatus{
					{Repo: "acme/widgets", LastSyncStart: now.Add(-30 * time.Second), LastSyncEnd: now.Add(-25 * time.Second)},
				},
			}
			return nil
		}}, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "daemon: running (pid 4321)") {
		t.Fatalf("output missing daemon line: %q", out)
	}
	if !strings.Contains(out, "Sync: cycle 1 finished") {
		t.Fatalf("output missing sync section: %q", out)
	}
	if !strings.Contains(out, "✓ acme/widgets") {
		t.Fatalf("output missing per-repo row: %q", out)
	}
}
