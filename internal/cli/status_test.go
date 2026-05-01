package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

var statusANSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripStatusANSI(s string) string {
	return statusANSIPattern.ReplaceAllString(s, "")
}

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
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
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
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			StateChange: sharedtypes.StateChangeNone,
			Confidence:  sharedtypes.ConfidenceMedium,
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

	if got, want := buf.String(), "pending=1 maintainer=0 unconfigured=1 repos=0 daemon=stopped\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
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

	if got, want := buf.String(), "pending=2 maintainer=1 unconfigured=1 repos=1 daemon=stopped\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
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

func TestStatusCommandLaunchesRealtimeTUIWhenInteractive(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalRunStatusTUI := runStatusTUI
	originalIsInteractiveTerminal := isInteractiveTerminal
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runStatusTUI = originalRunStatusTUI
		isInteractiveTerminal = originalIsInteractiveTerminal
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	isInteractiveTerminal = func() bool { return true }

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"acme/widgets"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	var gotInterval time.Duration
	runStatusTUI = func(_ context.Context, _ io.Writer, _ io.Writer, opts statusTUIOptions) error {
		gotInterval = opts.RefreshInterval
		data, err := opts.Collect()
		if err != nil {
			return err
		}
		if len(data.repos) != 1 || data.repos[0] != "acme/widgets" {
			t.Fatalf("collected repos = %#v, want acme/widgets", data.repos)
		}
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotInterval != 100*time.Millisecond {
		t.Fatalf("refresh interval = %s, want 100ms", gotInterval)
	}
	if got := buf.String(); got != "" {
		t.Fatalf("output = %q, want TUI to own rendering", got)
	}
}

func TestStatusTUIViewUsesMainTUIBoxStyle(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := newStatusTUIModel(statusTUIOptions{Now: func() time.Time { return now }})
	m.data = statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   12345,
		repos:       []string{"acme/widgets"},
		sync: &ipc.SyncStatusResult{
			Repos: []ipc.RepoSyncStatus{{Repo: "acme/widgets", LastSyncEnd: now.Add(-time.Minute)}},
		},
	}
	m.hasData = true
	m.width = 80

	view := stripStatusANSI(m.View())
	for _, want := range []string{
		"╭─ Status ",
		"│ daemon: running (pid 12345)",
		"│   ✓ acme/widgets  synced",
		"╰── q quit  ? help ",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{
		"ezoss status",
		"refresh 10fps",
	} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("View() should not include standalone header %q in:\n%s", unwanted, view)
		}
	}
}

func TestStatusTUITickOnlySchedulesRenderRefresh(t *testing.T) {
	m := newStatusTUIModel(statusTUIOptions{
		RefreshInterval: time.Nanosecond,
		Collect: func() (statusData, error) {
			t.Fatal("tick should not collect status data")
			return statusData{}, nil
		},
	})

	_, cmd := m.Update(statusTUITickMsg{})
	if cmd == nil {
		t.Fatal("Update(statusTUITickMsg) cmd = nil, want render refresh tick")
	}
	if _, ok := cmd().(statusTUITickMsg); !ok {
		t.Fatalf("Update(statusTUITickMsg) cmd returned %T, want statusTUITickMsg", cmd())
	}
}

func TestStatusTUIViewConstrainedToTerminalHeight(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repos := []string{
		"acme/repo-01",
		"acme/repo-02",
		"acme/repo-03",
		"acme/repo-04",
		"acme/repo-05",
		"acme/repo-06",
		"acme/repo-07",
		"acme/repo-08",
	}
	m := newStatusTUIModel(statusTUIOptions{Now: func() time.Time { return now }})
	m.data = statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   12345,
		repos:       repos,
		sync:        &ipc.SyncStatusResult{Repos: []ipc.RepoSyncStatus{}},
	}
	m.hasData = true
	m.width = 80
	m.height = 8

	view := stripStatusANSI(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Fatalf("View() rendered %d lines, want at most %d:\n%s", len(lines), m.height, view)
	}
	if !strings.Contains(view, "more lines") {
		t.Fatalf("View() missing overflow indicator:\n%s", view)
	}
}

func TestStatusTUIViewHonorsNarrowTerminalWidth(t *testing.T) {
	m := newStatusTUIModel(statusTUIOptions{})
	m.hasData = true
	m.data = statusData{daemonState: daemon.StateStopped}
	m.width = 40

	view := stripStatusANSI(m.View())
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("View() line width = %d, want at most %d:\n%s", got, m.width, view)
		}
	}
}

func TestStatusTUIViewClipsTitlesAndFootersToNarrowTerminalWidth(t *testing.T) {
	m := newStatusTUIModel(statusTUIOptions{})
	m.hasData = true
	m.data = statusData{daemonState: daemon.StateStopped}
	m.width = 10
	m.showHelp = true

	view := stripStatusANSI(m.View())
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("View() line width = %d, want at most %d:\n%s", got, m.width, view)
		}
	}
}

func TestStatusTUIViewFitsTinyTerminalHeight(t *testing.T) {
	for _, tc := range []struct {
		name     string
		height   int
		showHelp bool
	}{
		{name: "without help", height: 2},
		{name: "with help", height: 7, showHelp: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newStatusTUIModel(statusTUIOptions{})
			m.hasData = true
			m.data = statusData{daemonState: daemon.StateStopped}
			m.width = 40
			m.height = tc.height
			m.showHelp = tc.showHelp

			view := stripStatusANSI(m.View())
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > m.height {
				t.Fatalf("View() rendered %d lines, want at most %d:\n%s", len(lines), m.height, view)
			}
		})
	}
}

func TestStatusTUIViewHelpConstrainedToTerminalHeight(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	repos := []string{
		"acme/repo-01",
		"acme/repo-02",
		"acme/repo-03",
		"acme/repo-04",
		"acme/repo-05",
	}
	m := newStatusTUIModel(statusTUIOptions{Now: func() time.Time { return now }})
	m.data = statusData{
		daemonState: daemon.StateRunning,
		daemonPID:   12345,
		repos:       repos,
		sync:        &ipc.SyncStatusResult{Repos: []ipc.RepoSyncStatus{}},
	}
	m.hasData = true
	m.width = 80
	m.height = 8
	m.showHelp = true

	view := stripStatusANSI(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Fatalf("View() rendered %d lines, want at most %d:\n%s", len(lines), m.height, view)
	}
	if !strings.Contains(view, "Keyboard shortcuts") {
		t.Fatalf("View() missing help:\n%s", view)
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
		"maintainer:",
		"1 repo",
		"0 pending",
		"sync:   not running",
		"ezoss daemon start",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render = %q, missing %q", got, want)
		}
	}
}

func TestRenderShortStatusContribCountAndModeOff(t *testing.T) {
	// Enabled, with contrib pending: line carries contrib=N but no
	// contrib_mode marker (default-on, nothing to call out).
	on := renderShortStatus(statusData{
		daemonState:    daemon.StateRunning,
		repos:          []string{"acme/widgets"},
		pending:        2,
		contribPending: 1,
		contribEnabled: true,
	})
	if !strings.Contains(on, "contrib=1") {
		t.Fatalf("contrib enabled output missing contrib=1: %q", on)
	}
	if strings.Contains(on, "contrib_mode") {
		t.Fatalf("contrib enabled output should not advertise contrib_mode: %q", on)
	}
	// Disabled: marker appears so scripts can detect opt-out.
	off := renderShortStatus(statusData{
		daemonState:    daemon.StateRunning,
		repos:          []string{"acme/widgets"},
		contribEnabled: false,
	})
	if !strings.Contains(off, "contrib_mode=off") {
		t.Fatalf("contrib disabled output missing contrib_mode=off: %q", off)
	}
}

func TestRenderRichStatusPerSourceRows(t *testing.T) {
	enabled := renderRichStatus(statusData{
		daemonState:       daemon.StateRunning,
		daemonPID:         42,
		repos:             []string{"acme/widgets"},
		pending:           3,
		configuredPending: 1,
		contribPending:    2,
		contribRepos:      1,
		contribEnabled:    true,
	}, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	// Each source gets its own self-contained row. The user shouldn't
	// have to subtract across rows to find the maintainer count.
	for _, want := range []string{
		"maintainer:",
		"1 repo  •  1 pending recommendation",
		"contributor:",
		"1 repo  •  2 pending recommendations",
	} {
		if !strings.Contains(enabled, want) {
			t.Fatalf("enabled rich status missing %q:\n%s", want, enabled)
		}
	}
	if strings.Contains(enabled, "contrib: enabled") {
		t.Fatalf("standalone 'contrib: enabled' line should be gone:\n%s", enabled)
	}

	disabled := renderRichStatus(statusData{
		daemonState:    daemon.StateRunning,
		daemonPID:      42,
		repos:          []string{"acme/widgets"},
		contribEnabled: false,
	}, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(disabled, "contributor:  disabled") {
		t.Fatalf("disabled rich status missing 'contributor:  disabled':\n%s", disabled)
	}
}

func TestRenderRichStatusUnconfiguredOnItsOwnRow(t *testing.T) {
	d := statusData{
		daemonState:       daemon.StateStopped,
		repos:             []string{"acme/widgets"},
		pending:           3,
		configuredPending: 2,
		unconfigured:      1,
		contribEnabled:    true,
	}
	got := renderRichStatus(d, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(got, "unconfigured: 1 pending recommendation ") {
		t.Fatalf("render missing unconfigured row:\n%s", got)
	}
	if strings.Contains(got, "from unconfigured repo") {
		t.Fatalf("legacy '(N from unconfigured repo)' suffix should be gone:\n%s", got)
	}
	// Unconfigured row must not appear when count is zero (default
	// case); the default config without removed repos shouldn't
	// surface noise.
	dz := d
	dz.unconfigured = 0
	if strings.Contains(renderRichStatus(dz, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)), "unconfigured:") {
		t.Fatal("unconfigured row should not appear when count is 0")
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
		"synced",
		"→ acme/syncing",
		"syncing... (3s in)",
		"·",          // pending marker for never-synced
		"acme/never", // never-synced row
		"pending first sync",
		"✗ acme/errored",
		"error: rate limited",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render missing %q\n--- got ---\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"synced 2m ago",
		"(450ms)",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("render should not include %q\n--- got ---\n%s", unwanted, got)
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
