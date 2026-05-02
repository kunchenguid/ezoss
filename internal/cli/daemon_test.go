package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/agent"
	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/triage"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestRootCommandDoesNotExposeDaemonStatusSubcommand(t *testing.T) {
	// `ezoss daemon status` was removed in favor of `ezoss status`, which
	// folds in daemon liveness alongside sync progress.
	cmd := NewRootCmd()
	got, _, err := cmd.Find([]string{"daemon", "status"})
	if err == nil && got != nil && got.Name() == "status" {
		t.Fatalf("Find(daemon status) = %v, want no such subcommand", got)
	}
}

func TestDaemonRunCommandMockModeSeedsRecommendations(t *testing.T) {
	tempRoot := t.TempDir()
	if err := config.SaveGlobal(tempRoot+"/config.yaml", &config.GlobalConfig{
		Repos:        []string{"acme/widgets"},
		PollInterval: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	originalNewPaths := newPaths
	originalRunDaemon := runDaemonWithOptions
	originalInstallLogPipe := installTimestampedLogPipe
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runDaemonWithOptions = originalRunDaemon
		installTimestampedLogPipe = originalInstallLogPipe
	})

	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	installTimestampedLogPipe = func(_ io.Writer) (func(), error) {
		return func() {}, nil
	}

	runDaemonWithOptions = func(pidFile string, sigCh <-chan os.Signal, opts daemon.RunOptions) error {
		if err := opts.PollOnce(context.Background(), opts.Repos); err != nil {
			return err
		}
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "run", "--mock"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	database, err := db.Open(paths.WithRoot(tempRoot).DBPath())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	count, err := database.CountActiveRecommendations()
	if err != nil {
		t.Fatalf("CountActiveRecommendations() error = %v", err)
	}
	if count == 0 {
		t.Fatal("expected mock daemon run to persist at least one recommendation")
	}
}

func TestDaemonRunCommandMockModeRejectsFixStart(t *testing.T) {
	tempRoot := t.TempDir()
	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos:        []string{"acme/widgets"},
		PollInterval: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	originalNewPaths := newPaths
	originalRunDaemon := runDaemonWithOptions
	originalInstallLogPipe := installTimestampedLogPipe
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runDaemonWithOptions = originalRunDaemon
		installTimestampedLogPipe = originalInstallLogPipe
	})

	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	installTimestampedLogPipe = func(_ io.Writer) (func(), error) {
		return func() {}, nil
	}

	runDaemonWithOptions = func(_ string, _ <-chan os.Signal, opts daemon.RunOptions) error {
		if err := opts.PollOnce(context.Background(), opts.Repos); err != nil {
			return err
		}
		recommendations, err := opts.RecommendationSnapshot()
		if err != nil {
			return err
		}
		var params ipc.FixStartParams
		for _, recommendation := range recommendations {
			for _, option := range recommendation.Options {
				if strings.TrimSpace(option.FixPrompt) != "" {
					params = ipc.FixStartParams{RecommendationID: recommendation.ID, OptionID: option.ID}
					break
				}
			}
			if params.RecommendationID != "" {
				break
			}
		}
		if params.RecommendationID == "" {
			t.Fatalf("mock recommendations = %#v, want at least one fixable option", recommendations)
		}
		if opts.FixStart == nil {
			t.Fatalf("FixStart is nil, want explicit mock-mode rejection")
		}
		_, err = opts.FixStart(context.Background(), params)
		if err == nil || !strings.Contains(err.Error(), "fix is unavailable in mock mode") {
			t.Fatalf("FixStart() error = %v, want mock-mode rejection", err)
		}
		jobs, err := opts.FixJobSnapshot()
		if err != nil {
			return err
		}
		if len(jobs) != 0 {
			t.Fatalf("fix jobs = %#v, want none", jobs)
		}
		return nil
	}

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"daemon", "run", "--mock"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDaemonRunCommandUsesConfiguredAgentWithoutMock(t *testing.T) {
	tempRoot := t.TempDir()
	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent:        sharedtypes.AgentCodex,
		Repos:        []string{"acme/widgets"},
		PollInterval: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	originalNewPaths := newPaths
	originalRunDaemon := runDaemonWithOptions
	originalNewDaemonTriageLister := newDaemonTriageLister
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	originalInstallLogPipe := installTimestampedLogPipe
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runDaemonWithOptions = originalRunDaemon
		newDaemonTriageLister = originalNewDaemonTriageLister
		newAgent = originalNewAgent
		lookPath = originalLookPath
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
		installTimestampedLogPipe = originalInstallLogPipe
	})

	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	installTimestampedLogPipe = func(_ io.Writer) (func(), error) {
		return func() {}, nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "acme/widgets" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "acme/widgets")
		}
		return t.TempDir(), nil
	}
	newDaemonTriageLister = func() daemonTriageLister {
		return stubDaemonTriageLister{items: []ghclient.Item{{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    77,
			Title:     "poll loop crashes",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/acme/widgets/issues/77",
			UpdatedAt: mustDaemonTime(t, "2026-04-19T10:00:00Z"),
		}}}
	}

	var gotName sharedtypes.AgentName
	newAgent = func(name sharedtypes.AgentName, bin string) (triageAgent, error) {
		gotName = name
		if bin != "/usr/local/bin/codex" {
			t.Fatalf("newAgent() bin = %q, want %q", bin, "/usr/local/bin/codex")
		}
		return stubTriageAgent{result: &agent.Result{
			Output: mustDaemonJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "Needs logs before debugging.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Please share the daemon log around the crash.",
					Confidence:   sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}}, nil
	}
	lookPath = func(file string) (string, error) {
		switch file {
		case "claude":
			return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
		case "codex":
			return "/usr/local/bin/codex", nil
		default:
			t.Fatalf("lookPath() file = %q, want claude or codex", file)
			return "", nil
		}
	}
	runDaemonWithOptions = func(pidFile string, sigCh <-chan os.Signal, opts daemon.RunOptions) error {
		if err := opts.PollOnce(context.Background(), opts.Repos); err != nil {
			return err
		}
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotName != sharedtypes.AgentCodex {
		t.Fatalf("newAgent() name = %q, want %q", gotName, sharedtypes.AgentCodex)
	}

	database, err := db.Open(paths.WithRoot(tempRoot).DBPath())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("len(ListActiveRecommendations()) = %d, want 1", len(recommendations))
	}
	if recommendations[0].Agent != sharedtypes.AgentCodex {
		t.Fatalf("recommendation.Agent = %q, want %q", recommendations[0].Agent, sharedtypes.AgentCodex)
	}
	if recommendations[0].Model != "codex" {
		t.Fatalf("recommendation.Model = %q, want codex", recommendations[0].Model)
	}
	if recommendations[0].TokensIn != 900 || recommendations[0].TokensOut != 120 {
		t.Fatalf("unexpected token usage = in:%d out:%d", recommendations[0].TokensIn, recommendations[0].TokensOut)
	}
}

func TestDaemonRunCommandUsesRepoOverrideForSingleConfiguredRepo(t *testing.T) {
	tempRoot := t.TempDir()
	repoDir := t.TempDir()
	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent:        sharedtypes.AgentCodex,
		Repos:        []string{"acme/widgets"},
		PollInterval: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".ezoss.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.ezoss.yaml) error = %v", err)
	}

	originalNewPaths := newPaths
	originalRunDaemon := runDaemonWithOptions
	originalNewDaemonTriageLister := newDaemonTriageLister
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalCurrentWorkingDir := currentWorkingDir
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	originalInstallLogPipe := installTimestampedLogPipe
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runDaemonWithOptions = originalRunDaemon
		newDaemonTriageLister = originalNewDaemonTriageLister
		newAgent = originalNewAgent
		lookPath = originalLookPath
		currentWorkingDir = originalCurrentWorkingDir
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
		installTimestampedLogPipe = originalInstallLogPipe
	})

	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	installTimestampedLogPipe = func(_ io.Writer) (func(), error) {
		return func() {}, nil
	}
	newDaemonTriageLister = func() daemonTriageLister {
		return stubDaemonTriageLister{items: []ghclient.Item{{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    77,
			Title:     "poll loop crashes",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/acme/widgets/issues/77",
			UpdatedAt: mustDaemonTime(t, "2026-04-19T10:00:00Z"),
		}}}
	}
	currentWorkingDir = func() (string, error) {
		return repoDir, nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "acme/widgets" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "acme/widgets")
		}
		return repoDir, nil
	}

	var gotName sharedtypes.AgentName
	newAgent = func(name sharedtypes.AgentName, bin string) (triageAgent, error) {
		gotName = name
		if bin != "/usr/local/bin/claude" {
			t.Fatalf("newAgent() bin = %q, want %q", bin, "/usr/local/bin/claude")
		}
		return stubTriageAgent{result: &agent.Result{
			Output: mustDaemonJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "Needs logs before debugging.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Please share the daemon log around the crash.",
					Confidence:   sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}}, nil
	}
	lookPath = func(file string) (string, error) {
		switch file {
		case "claude":
			return "/usr/local/bin/claude", nil
		case "codex":
			return "/usr/local/bin/codex", nil
		default:
			t.Fatalf("lookPath() file = %q, want claude or codex", file)
			return "", nil
		}
	}
	runDaemonWithOptions = func(pidFile string, sigCh <-chan os.Signal, opts daemon.RunOptions) error {
		if err := opts.PollOnce(context.Background(), opts.Repos); err != nil {
			return err
		}
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotName != sharedtypes.AgentClaude {
		t.Fatalf("newAgent() name = %q, want %q from repo override", gotName, sharedtypes.AgentClaude)
	}
}

func TestDaemonRunCommandLoadsConfigAndStartsRuntime(t *testing.T) {
	tempRoot := t.TempDir()
	if err := config.SaveGlobal(tempRoot+"/config.yaml", &config.GlobalConfig{
		Repos:        []string{"acme/widgets"},
		PollInterval: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	originalNewPaths := newPaths
	originalRunDaemon := runDaemonWithOptions
	originalInstallLogPipe := installTimestampedLogPipe
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runDaemonWithOptions = originalRunDaemon
		installTimestampedLogPipe = originalInstallLogPipe
	})
	stubAgentLookPath(t, map[string]string{
		"codex": "/usr/local/bin/codex",
	})

	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	installTimestampedLogPipe = func(_ io.Writer) (func(), error) {
		return func() {}, nil
	}

	called := false
	runDaemonWithOptions = func(pidFile string, sigCh <-chan os.Signal, opts daemon.RunOptions) error {
		called = true
		wantPidFile := filepath.Join(tempRoot, "daemon.pid")
		if pidFile != wantPidFile {
			t.Fatalf("pidFile = %q, want %q", pidFile, wantPidFile)
		}
		wantIPCPath := filepath.Join(tempRoot, "daemon.sock")
		if opts.IPCPath != wantIPCPath {
			t.Fatalf("IPCPath = %q, want %q", opts.IPCPath, wantIPCPath)
		}
		if sigCh != nil {
			t.Fatal("expected CLI to let daemon own signal channel")
		}
		if opts.PollInterval != 2*time.Minute {
			t.Fatalf("PollInterval = %v, want %v", opts.PollInterval, 2*time.Minute)
		}
		if opts.StaleThreshold != 720*time.Hour {
			t.Fatalf("StaleThreshold = %v, want %v", opts.StaleThreshold, 720*time.Hour)
		}
		if len(opts.Repos) != 1 || opts.Repos[0] != "acme/widgets" {
			t.Fatalf("Repos = %#v, want %#v", opts.Repos, []string{"acme/widgets"})
		}
		if opts.PollOnce == nil {
			t.Fatal("expected PollOnce to be wired")
		}
		if opts.NewTicker == nil {
			t.Fatal("expected NewTicker to be wired")
		}
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !called {
		t.Fatal("expected daemon runtime to be invoked")
	}
}

type stubDaemonTriageLister struct {
	items []ghclient.Item
	err   error
}

type stubDaemonIPCClient struct {
	call func(method string, params interface{}, result interface{}) error
}

func (s stubDaemonIPCClient) Call(method string, params interface{}, result interface{}) error {
	if s.call == nil {
		return nil
	}
	return s.call(method, params, result)
}

func (stubDaemonIPCClient) Close() error {
	return nil
}

func (s stubDaemonTriageLister) ListNeedingTriage(_ context.Context, _ string) ([]ghclient.Item, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]ghclient.Item(nil), s.items...), nil
}

func (s stubDaemonTriageLister) ListTriaged(_ context.Context, _ string, _ time.Time) ([]ghclient.Item, error) {
	if s.err != nil {
		return nil, s.err
	}
	return nil, nil
}

func (s stubDaemonTriageLister) SearchAuthoredOpenPRs(_ context.Context) ([]ghclient.Item, error) {
	return nil, nil
}

func (s stubDaemonTriageLister) SearchAuthoredOpenIssues(_ context.Context) ([]ghclient.Item, error) {
	return nil, nil
}

func (s stubDaemonTriageLister) ListOwnedRepos(_ context.Context, _ ghclient.RepoVisibility) ([]string, error) {
	return nil, nil
}

func mustDaemonJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func mustDaemonTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	return parsed
}
