package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	"github.com/kunchenguid/ezoss/internal/wizard"
	"github.com/spf13/cobra"
)

func TestInitCommandWritesConfiguredGlobalConfig(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"init",
		"--agent", "codex",
		"--merge-method", "squash",
		"--poll-interval", "10m",
		"--stale-threshold", "336h",
		"--repo", "kunchenguid/ezoss",
		"--repo", "kunchenguid/no-mistakes",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	configPath := filepath.Join(tempRoot, "config.yaml")
	got, err := config.LoadGlobal(configPath)
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if got.Agent != config.AgentCodex {
		t.Fatalf("Agent = %q, want %q", got.Agent, config.AgentCodex)
	}
	if got.PollInterval.String() != "10m0s" {
		t.Fatalf("PollInterval = %v, want 10m0s", got.PollInterval)
	}
	if got.MergeMethod != "squash" {
		t.Fatalf("MergeMethod = %q, want squash", got.MergeMethod)
	}
	if got.StaleThreshold.String() != "336h0m0s" {
		t.Fatalf("StaleThreshold = %v, want 336h0m0s", got.StaleThreshold)
	}
	if strings.Join(got.Repos, ",") != "kunchenguid/ezoss,kunchenguid/no-mistakes" {
		t.Fatalf("Repos = %v, want configured repos", got.Repos)
	}

	output := buf.String()
	for _, want := range []string{"initialized", configPath, "2 repos"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q does not contain %q", output, want)
		}
	}

	if _, err := os.Stat(filepath.Join(tempRoot, "logs")); err != nil {
		t.Fatalf("logs dir stat error = %v", err)
	}
	if got.SyncLabels != (config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true}) {
		t.Fatalf("SyncLabels = %+v, want defaults", got.SyncLabels)
	}

	if output == "" {
		t.Fatal("expected init output")
	}
}

func TestInitCommandAcceptsDayDurationForStaleThreshold(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--stale-threshold", "30d"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if got.StaleThreshold != 30*24*time.Hour {
		t.Fatalf("StaleThreshold = %v, want %v", got.StaleThreshold, 30*24*time.Hour)
	}
}

func TestInitCommandEmitsTelemetry(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(resetTelemetry)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	event := telemetrySink.events[0]
	if event.Name != "command" {
		t.Fatalf("telemetry event name = %q, want command", event.Name)
	}
	if got := event.Fields["command"]; got != "init" {
		t.Fatalf("telemetry field command = %v, want init", got)
	}
	if got := event.Fields["entrypoint"]; got != "init" {
		t.Fatalf("telemetry field entrypoint = %v, want init", got)
	}
}

func TestInitCommandAppendsReposWithoutReplacingExisting(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() { newPaths = originalNewPaths })
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	first := NewRootCmd()
	first.SetArgs([]string{"init", "--repo", "existing/first", "--repo", "existing/second"})
	if err := first.Execute(); err != nil {
		t.Fatalf("first init error = %v", err)
	}

	buf := &bytes.Buffer{}
	second := NewRootCmd()
	second.SetOut(buf)
	second.SetErr(buf)
	second.SetArgs([]string{"init", "--repo", "new/third"})
	if err := second.Execute(); err != nil {
		t.Fatalf("second init error = %v", err)
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	want := []string{"existing/first", "existing/second", "new/third"}
	if !reflect.DeepEqual(cfg.Repos, want) {
		t.Fatalf("Repos = %v, want %v (init --repo should append, not replace)", cfg.Repos, want)
	}

	output := buf.String()
	for _, repo := range want {
		if !strings.Contains(output, repo) {
			t.Fatalf("summary output %q does not contain configured repo %q", output, repo)
		}
	}
}

func TestInitCommandDedupesRepoValues(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() { newPaths = originalNewPaths })
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	first := NewRootCmd()
	first.SetArgs([]string{"init", "--repo", "kunchenguid/ezoss"})
	if err := first.Execute(); err != nil {
		t.Fatalf("first init error = %v", err)
	}
	second := NewRootCmd()
	second.SetArgs([]string{"init", "--repo", "kunchenguid/ezoss", "--repo", "kunchenguid/ezoss"})
	if err := second.Execute(); err != nil {
		t.Fatalf("second init error = %v", err)
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Repos, []string{"kunchenguid/ezoss"}) {
		t.Fatalf("Repos = %v, want single kunchenguid/ezoss after dedup", cfg.Repos)
	}
}

func TestInitCommandSummaryListsConfiguredRepos(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() { newPaths = originalNewPaths })
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"init",
		"--repo", "kunchenguid/ezoss",
		"--repo", "kunchenguid/no-mistakes",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	for _, repo := range []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"} {
		if !strings.Contains(output, repo) {
			t.Fatalf("summary %q does not list configured repo %q", output, repo)
		}
	}
}

func TestInitCommandRejectsMalformedMergeMethodFlag(t *testing.T) {
	cases := []string{"fast-forward", "squash-and-merge", "FASTFORWARD"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			tempRoot := t.TempDir()
			originalNewPaths := newPaths
			t.Cleanup(func() { newPaths = originalNewPaths })
			newPaths = func() (*paths.Paths, error) {
				return paths.WithRoot(tempRoot), nil
			}

			buf := &bytes.Buffer{}
			cmd := NewRootCmd()
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs([]string{"init", "--merge-method", input})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("Execute(--merge-method %q) error = nil, want error", input)
			}
			msg := err.Error()
			if !strings.Contains(msg, "parse --merge-method") {
				t.Fatalf("error = %v, want parse --merge-method prefix (not save config)", err)
			}
			if !strings.Contains(msg, input) {
				t.Fatalf("error = %v, want input %q echoed back", err, input)
			}
			if !strings.Contains(msg, "merge, squash, or rebase") {
				t.Fatalf("error = %v, want merge/squash/rebase hint", err)
			}
			if strings.Contains(msg, "save config") {
				t.Fatalf("error = %v, should not be reported as save config failure", err)
			}

			configPath := filepath.Join(tempRoot, "config.yaml")
			if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
				cfg, loadErr := config.LoadGlobal(configPath)
				if loadErr != nil {
					t.Fatalf("LoadGlobal() error = %v", loadErr)
				}
				if cfg.MergeMethod != "merge" {
					t.Fatalf("MergeMethod = %q, want default merge (bad input should not be persisted)", cfg.MergeMethod)
				}
			}
		})
	}
}

type stubRepoLister struct {
	calls        []ghclient.RepoVisibility
	repos        []string
	err          error
	starredCalls int
	starredRepos []string
	starredErr   error
}

func (s *stubRepoLister) ListOwnedRepos(_ context.Context, visibility ghclient.RepoVisibility) ([]string, error) {
	s.calls = append(s.calls, visibility)
	return s.repos, s.err
}

func (s *stubRepoLister) ListStarredRepos(_ context.Context) ([]string, error) {
	s.starredCalls++
	return s.starredRepos, s.starredErr
}

func withInitTestEnv(t *testing.T) string {
	t.Helper()
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() { newPaths = originalNewPaths })
	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(tempRoot), nil }

	originalWizardEnabled := initWizardEnabled
	t.Cleanup(func() { initWizardEnabled = originalWizardEnabled })
	initWizardEnabled = func(*cobra.Command) bool { return false }

	return tempRoot
}

func TestInitCommandAllOwnedFlagListsAndStoresRepos(t *testing.T) {
	tempRoot := withInitTestEnv(t)

	stub := &stubRepoLister{repos: []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}}
	originalLister := newRepoLister
	t.Cleanup(func() { newRepoLister = originalLister })
	newRepoLister = func() repoLister { return stub }

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--all-owned"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(stub.calls) != 1 || stub.calls[0] != ghclient.RepoVisibilityAll {
		t.Fatalf("ListOwnedRepos calls = %v, want one all-visibility call", stub.calls)
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal error = %v", err)
	}
	want := []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}
	if !reflect.DeepEqual(cfg.Repos, want) {
		t.Fatalf("Repos = %v, want %v", cfg.Repos, want)
	}
}

func TestInitCommandAllPublicOwnedFlagPassesPublicVisibility(t *testing.T) {
	withInitTestEnv(t)

	stub := &stubRepoLister{repos: []string{"kunchenguid/ezoss"}}
	originalLister := newRepoLister
	t.Cleanup(func() { newRepoLister = originalLister })
	newRepoLister = func() repoLister { return stub }

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--all-public-owned"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(stub.calls) != 1 || stub.calls[0] != ghclient.RepoVisibilityPublic {
		t.Fatalf("ListOwnedRepos calls = %v, want one public-visibility call", stub.calls)
	}
}

func TestInitCommandRejectsBothAllFlagsTogether(t *testing.T) {
	withInitTestEnv(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--all-owned", "--all-public-owned"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both flags supplied")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want mutually exclusive hint", err)
	}
}

func TestInitCommandAllPublicOwnedAndStarredFlagIntersectsTheTwoLists(t *testing.T) {
	tempRoot := withInitTestEnv(t)

	stub := &stubRepoLister{
		repos:        []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes", "kunchenguid/internal"},
		starredRepos: []string{"acme/widgets", "kunchenguid/ezoss", "kunchenguid/no-mistakes"},
	}
	originalLister := newRepoLister
	t.Cleanup(func() { newRepoLister = originalLister })
	newRepoLister = func() repoLister { return stub }

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--all-public-owned-and-starred"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(stub.calls) != 1 || stub.calls[0] != ghclient.RepoVisibilityPublic {
		t.Fatalf("ListOwnedRepos calls = %v, want one public-visibility call", stub.calls)
	}
	if stub.starredCalls != 1 {
		t.Fatalf("ListStarredRepos calls = %d, want 1", stub.starredCalls)
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal error = %v", err)
	}
	want := []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}
	if !reflect.DeepEqual(cfg.Repos, want) {
		t.Fatalf("Repos = %v, want %v (intersection of owned ∩ starred, owned ordering)", cfg.Repos, want)
	}
}

func TestInitCommandAllPublicOwnedAndStarredSurfacesStarredError(t *testing.T) {
	withInitTestEnv(t)

	stub := &stubRepoLister{
		repos:      []string{"kunchenguid/ezoss"},
		starredErr: errors.New("starred fetch broke"),
	}
	originalLister := newRepoLister
	t.Cleanup(func() { newRepoLister = originalLister })
	newRepoLister = func() repoLister { return stub }

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--all-public-owned-and-starred"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from starred lister to propagate")
	}
	if !strings.Contains(err.Error(), "list starred repos") {
		t.Fatalf("error = %v, want list starred repos prefix", err)
	}
}

func TestInitCommandAllOwnedSurfacesListerErrors(t *testing.T) {
	withInitTestEnv(t)

	stub := &stubRepoLister{err: errors.New("not authenticated")}
	originalLister := newRepoLister
	t.Cleanup(func() { newRepoLister = originalLister })
	newRepoLister = func() repoLister { return stub }

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init", "--all-owned"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from lister to propagate")
	}
	if !strings.Contains(err.Error(), "list owned repos") {
		t.Fatalf("error = %v, want list owned repos prefix", err)
	}
}

func TestInitCommandSkipsWizardWhenNonInteractive(t *testing.T) {
	tempRoot := withInitTestEnv(t)

	wizardCalled := false
	originalRun := runInitWizard
	t.Cleanup(func() { runInitWizard = originalRun })
	runInitWizard = func(_ wizard.Config) (wizard.Result, error) {
		wizardCalled = true
		return wizard.Result{}, nil
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if wizardCalled {
		t.Fatal("wizard should not run when stdout is not a TTY")
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal error = %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Fatalf("Repos = %v, want empty (no flags + no TTY = no-op)", cfg.Repos)
	}
}

func TestInitCommandRunsWizardWhenInteractiveAndNoFlags(t *testing.T) {
	tempRoot := withInitTestEnv(t)

	originalEnabled := initWizardEnabled
	t.Cleanup(func() { initWizardEnabled = originalEnabled })
	initWizardEnabled = func(*cobra.Command) bool { return true }

	originalDetect := detectCurrentRepo
	t.Cleanup(func() { detectCurrentRepo = originalDetect })
	detectCurrentRepo = func(_ context.Context, _ string) (string, error) {
		return "kunchenguid/ezoss", nil
	}

	gotDetected := ""
	originalRun := runInitWizard
	t.Cleanup(func() { runInitWizard = originalRun })
	runInitWizard = func(cfg wizard.Config) (wizard.Result, error) {
		gotDetected = cfg.DetectedRepo
		return wizard.Result{
			Mode:  wizard.ModeOneAtATime,
			Repos: []string{"kunchenguid/ezoss"},
		}, nil
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotDetected != "kunchenguid/ezoss" {
		t.Fatalf("wizard cfg.DetectedRepo = %q, want kunchenguid/ezoss", gotDetected)
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Repos, []string{"kunchenguid/ezoss"}) {
		t.Fatalf("Repos = %v, want [kunchenguid/ezoss]", cfg.Repos)
	}
}

func TestInitCommandWizardAbortLeavesConfigUnchanged(t *testing.T) {
	tempRoot := withInitTestEnv(t)

	// Seed an existing repo so we can confirm the abort doesn't drop it.
	first := NewRootCmd()
	first.SetArgs([]string{"init", "--repo", "existing/repo"})
	if err := first.Execute(); err != nil {
		t.Fatalf("seed init error = %v", err)
	}

	originalEnabled := initWizardEnabled
	t.Cleanup(func() { initWizardEnabled = originalEnabled })
	initWizardEnabled = func(*cobra.Command) bool { return true }

	originalDetect := detectCurrentRepo
	t.Cleanup(func() { detectCurrentRepo = originalDetect })
	detectCurrentRepo = func(_ context.Context, _ string) (string, error) { return "", nil }

	originalRun := runInitWizard
	t.Cleanup(func() { runInitWizard = originalRun })
	runInitWizard = func(_ wizard.Config) (wizard.Result, error) {
		return wizard.Result{Aborted: true}, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !strings.Contains(buf.String(), "init cancelled") {
		t.Fatalf("output %q does not mention cancellation", buf.String())
	}

	cfg, err := config.LoadGlobal(filepath.Join(tempRoot, "config.yaml"))
	if err != nil {
		t.Fatalf("LoadGlobal error = %v", err)
	}
	if !reflect.DeepEqual(cfg.Repos, []string{"existing/repo"}) {
		t.Fatalf("Repos = %v, want [existing/repo] (abort should not lose seeded repo)", cfg.Repos)
	}
}

func TestInitCommandRejectsMalformedRepoFlag(t *testing.T) {
	cases := []string{"kunchenguid", "kunchenguid/", "/ezoss", "owner/name/extra"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			tempRoot := t.TempDir()
			originalNewPaths := newPaths
			t.Cleanup(func() { newPaths = originalNewPaths })
			newPaths = func() (*paths.Paths, error) {
				return paths.WithRoot(tempRoot), nil
			}

			buf := &bytes.Buffer{}
			cmd := NewRootCmd()
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs([]string{"init", "--repo", input})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("Execute(--repo %q) error = nil, want error", input)
			}
			msg := err.Error()
			if !strings.Contains(msg, input) {
				t.Fatalf("error = %v, want input %q echoed back", err, input)
			}
			if !strings.Contains(msg, "owner/name") {
				t.Fatalf("error = %v, want owner/name format hint", err)
			}

			configPath := filepath.Join(tempRoot, "config.yaml")
			if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
				cfg, loadErr := config.LoadGlobal(configPath)
				if loadErr != nil {
					t.Fatalf("LoadGlobal() error = %v", loadErr)
				}
				if len(cfg.Repos) != 0 {
					t.Fatalf("Repos = %v, want empty (bad input should not be persisted)", cfg.Repos)
				}
			}
		})
	}
}
