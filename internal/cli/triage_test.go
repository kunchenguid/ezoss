package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	"github.com/kunchenguid/ezoss/internal/triage"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestRootCommandIncludesTriageSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"triage"})
	if err != nil {
		t.Fatalf("Find(triage) error = %v", err)
	}
	if got == nil || got.Name() != "triage" {
		t.Fatalf("Find(triage) = %v, want triage command", got)
	}
}

func TestTriageCommandRejectsInvalidTarget(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "not-a-target", "--mock"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "parse target") {
		t.Fatalf("Execute() error = %v, want parse target error", err)
	}
	if !strings.Contains(err.Error(), `"not-a-target"`) {
		t.Fatalf("Execute() error = %v, want input value echoed back", err)
	}
	if !strings.Contains(err.Error(), "owner/name#number") {
		t.Fatalf("Execute() error = %v, want expected-format hint", err)
	}
}

func TestParseItemTargetRejectsRepoWithoutSlash(t *testing.T) {
	_, _, err := parseItemTarget("kunchenguid#42")
	if err == nil {
		t.Fatal("parseItemTarget(kunchenguid#42) error = nil, want error")
	}
	if !strings.Contains(err.Error(), `"kunchenguid#42"`) {
		t.Fatalf("parseItemTarget error = %v, want input value echoed back", err)
	}
	if !strings.Contains(err.Error(), "owner/name#number") {
		t.Fatalf("parseItemTarget error = %v, want format hint", err)
	}
}

func TestParseItemTargetRejectsRepoWithEmptyOwnerOrName(t *testing.T) {
	cases := []string{"/name#42", "owner/#42", "/#42"}
	for _, input := range cases {
		if _, _, err := parseItemTarget(input); err == nil {
			t.Fatalf("parseItemTarget(%q) error = nil, want error", input)
		}
	}
}

func TestParseItemTargetAcceptsValidOwnerName(t *testing.T) {
	repo, number, err := parseItemTarget("kunchenguid/ezoss#42")
	if err != nil {
		t.Fatalf("parseItemTarget() error = %v, want nil", err)
	}
	if repo != "kunchenguid/ezoss" {
		t.Fatalf("repo = %q, want kunchenguid/ezoss", repo)
	}
	if number != 42 {
		t.Fatalf("number = %d, want 42", number)
	}
}

func TestTriageCommandWithMockPersistsRecommendation(t *testing.T) {
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

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "kunchenguid/ezoss#42", "--mock"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"triaged kunchenguid/ezoss#42\n",
		"action:     comment (low confidence)",
		"URL:        https://github.com/kunchenguid/ezoss/issues/42",
		"Run `ezoss` to review details and approve/edit.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, missing %q", got, want)
		}
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	item, err := database.GetItem("kunchenguid/ezoss#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil {
		t.Fatal("GetItem() = nil, want item")
	}
	if item.Kind != sharedtypes.ItemKindIssue {
		t.Fatalf("item.Kind = %q, want %q", item.Kind, sharedtypes.ItemKindIssue)
	}
	if item.WaitingOn != sharedtypes.WaitingOnContributor {
		t.Fatalf("item.WaitingOn = %q, want %q", item.WaitingOn, sharedtypes.WaitingOnContributor)
	}

	recommendations, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(recommendations) != 1 {
		t.Fatalf("len(ListActiveRecommendations()) = %d, want 1", len(recommendations))
	}
	if recommendations[0].ItemID != "kunchenguid/ezoss#42" {
		t.Fatalf("recommendation.ItemID = %q, want %q", recommendations[0].ItemID, "kunchenguid/ezoss#42")
	}
	if len(recommendations[0].Options) == 0 {
		t.Fatal("recommendation has no options")
	}
	primary := recommendations[0].Options[0]
	if primary.StateChange != sharedtypes.StateChangeNone {
		t.Fatalf("recommendation.Options[0].StateChange = %q, want %q", primary.StateChange, sharedtypes.StateChangeNone)
	}
	if primary.DraftComment == "" {
		t.Fatal("recommendation.Options[0].DraftComment = empty, want mock draft comment")
	}
	if len(telemetrySink.events) != 1 {
		t.Fatalf("len(telemetry events) = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if got := telemetrySink.events[0].Fields["command"]; got != "triage" {
		t.Fatalf("telemetry field command = %v, want %q", got, "triage")
	}
	if got := telemetrySink.events[0].Fields["entrypoint"]; got != "triage" {
		t.Fatalf("telemetry field entrypoint = %v, want %q", got, "triage")
	}
}

func TestTriageCommandWithMockReportsAvailableFixturesOnMiss(t *testing.T) {
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
	cmd.SetArgs([]string{"triage", "owner/repo#999", "--mock"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Fatalf("Execute() error = %v, want not found", err)
	}
	if !strings.Contains(msg, "mock fixtures:") {
		t.Fatalf("Execute() error = %v, want mock fixtures hint", err)
	}
	for _, want := range []string{"owner/repo#42", "owner/repo#7", "owner/repo#88"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Execute() error = %v, missing fixture %q", err, want)
		}
	}
}

func TestTriageCommandUsesConfiguredAgentWithoutMock(t *testing.T) {
	tempRoot := t.TempDir()
	workingDir := t.TempDir()
	resolvedWorkingDir, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	originalNewAgent := newAgent
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{item: ghclient.Item{
			Repo:      "kunchenguid/ezoss",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    77,
			Title:     "poll loop crashes",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/kunchenguid/ezoss/issues/77",
			UpdatedAt: mustTime(t, "2026-04-19T10:00:00Z"),
		}}
	}

	var gotName sharedtypes.AgentName
	var gotOpts agent.RunOpts
	newAgent = func(name sharedtypes.AgentName, _ string) (triageAgent, error) {
		gotName = name
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:    sharedtypes.StateChangeNone,
					Rationale:      "Needs logs before debugging.",
					WaitingOn:      sharedtypes.WaitingOnContributor,
					DraftComment:   "Please share the daemon log around the crash.",
					Confidence:     sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}, onRun: func(opts agent.RunOpts) {
			gotOpts = opts
		}}, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "kunchenguid/ezoss#77"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotName != sharedtypes.AgentCodex {
		t.Fatalf("newAgent() name = %q, want %q", gotName, sharedtypes.AgentCodex)
	}
	if gotOpts.CWD != resolvedWorkingDir {
		t.Fatalf("RunOpts.CWD = %q, want %q", gotOpts.CWD, resolvedWorkingDir)
	}
	got := buf.String()
	for _, want := range []string{
		"triaged kunchenguid/ezoss#77\n",
		"action:     comment (medium confidence)",
		"URL:        https://github.com/kunchenguid/ezoss/issues/77",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, missing %q", got, want)
		}
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

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

func TestTriageCommandPrefersRepoAgentOverrideWithoutMock(t *testing.T) {
	tempRoot := t.TempDir()
	workingDir := t.TempDir()
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	originalNewAgent := newAgent
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, ".ezoss.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.ezoss.yaml) error = %v", err)
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{item: ghclient.Item{
			Repo:      "kunchenguid/ezoss",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    77,
			Title:     "poll loop crashes",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/kunchenguid/ezoss/issues/77",
			UpdatedAt: mustTime(t, "2026-04-19T10:00:00Z"),
		}}
	}

	var gotName sharedtypes.AgentName
	newAgent = func(name sharedtypes.AgentName, _ string) (triageAgent, error) {
		gotName = name
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:    sharedtypes.StateChangeNone,
					Rationale:      "Needs logs before debugging.",
					WaitingOn:      sharedtypes.WaitingOnContributor,
					DraftComment:   "Please share the daemon log around the crash.",
					Confidence:     sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}}, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "kunchenguid/ezoss#77"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotName != sharedtypes.AgentClaude {
		t.Fatalf("newAgent() name = %q, want %q from repo override", gotName, sharedtypes.AgentClaude)
	}
}

func TestTriageCommandFindsRepoAgentOverrideFromSubdirectoryWithoutMock(t *testing.T) {
	tempRoot := t.TempDir()
	repoRoot := t.TempDir()
	workingDir := filepath.Join(repoRoot, "internal", "cli")
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	originalNewAgent := newAgent
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".ezoss.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.ezoss.yaml) error = %v", err)
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{item: ghclient.Item{
			Repo:      "kunchenguid/ezoss",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    77,
			Title:     "poll loop crashes",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/kunchenguid/ezoss/issues/77",
			UpdatedAt: mustTime(t, "2026-04-19T10:00:00Z"),
		}}
	}

	var gotName sharedtypes.AgentName
	newAgent = func(name sharedtypes.AgentName, _ string) (triageAgent, error) {
		gotName = name
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:    sharedtypes.StateChangeNone,
					Rationale:      "Needs logs before debugging.",
					WaitingOn:      sharedtypes.WaitingOnContributor,
					DraftComment:   "Please share the daemon log around the crash.",
					Confidence:     sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}}, nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "kunchenguid/ezoss#77"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotName != sharedtypes.AgentClaude {
		t.Fatalf("newAgent() name = %q, want %q from repo-root override found above cwd", gotName, sharedtypes.AgentClaude)
	}
}

func TestLoadLiveTriageConfigIgnoresRepoOverrideWhenMultipleReposConfigured(t *testing.T) {
	tempRoot := t.TempDir()
	workingDir := t.TempDir()
	originalCurrentWorkingDir := currentWorkingDir
	t.Cleanup(func() {
		currentWorkingDir = originalCurrentWorkingDir
	})
	currentWorkingDir = func() (string, error) {
		return workingDir, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
		Repos: []string{"acme/widgets", "other/repo"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, ".ezoss.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.ezoss.yaml) error = %v", err)
	}

	cfg, err := loadLiveTriageConfig(tempRoot, "other/repo")
	if err != nil {
		t.Fatalf("loadLiveTriageConfig() error = %v", err)
	}
	if cfg.Agent != sharedtypes.AgentCodex {
		t.Fatalf("cfg.Agent = %q, want global agent %q when multiple repos are configured", cfg.Agent, sharedtypes.AgentCodex)
	}
}

func TestTriageCommandReturnsLiveGitHubLoadErrorWithoutMock(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{err: exec.ErrNotFound}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "kunchenguid/ezoss#77"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "load item: gh item view kunchenguid/ezoss#77") {
		t.Fatalf("Execute() error = %v, want gh item view context", err)
	}
	if strings.Contains(err.Error(), "mock fixtures") {
		t.Fatalf("Execute() error = %v, should not mention mock fixtures", err)
	}
}

func TestTriageCommandResolvesAutoAgentWithoutMock(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	originalNewAgent := newAgent
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{item: ghclient.Item{
			Repo:      "kunchenguid/ezoss",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    77,
			Title:     "poll loop crashes",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/kunchenguid/ezoss/issues/77",
			UpdatedAt: mustTime(t, "2026-04-19T10:00:00Z"),
		}}
	}

	var gotName sharedtypes.AgentName
	newAgent = func(name sharedtypes.AgentName, bin string) (triageAgent, error) {
		gotName = name
		if bin != "/usr/local/bin/codex" {
			t.Fatalf("newAgent() bin = %q, want %q", bin, "/usr/local/bin/codex")
		}
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:    sharedtypes.StateChangeNone,
					Rationale:      "Needs logs before debugging.",
					WaitingOn:      sharedtypes.WaitingOnContributor,
					DraftComment:   "Please share the daemon log around the crash.",
					Confidence:     sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}}, nil
	}

	originalLookPath := lookPath
	t.Cleanup(func() {
		lookPath = originalLookPath
	})
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

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"triage", "kunchenguid/ezoss#77"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotName != sharedtypes.AgentCodex {
		t.Fatalf("newAgent() name = %q, want %q", gotName, sharedtypes.AgentCodex)
	}
	if got := buf.String(); !strings.Contains(got, "triaged kunchenguid/ezoss#77\n") {
		t.Fatalf("output = %q, want triaged prefix", got)
	}
}

type stubItemFetcher struct {
	item ghclient.Item
	err  error
}

func (s stubItemFetcher) GetItem(_ context.Context, _ string, _ sharedtypes.ItemKind, _ int) (ghclient.Item, error) {
	if s.err != nil {
		return ghclient.Item{}, s.err
	}
	return s.item, nil
}

// liveTriageRunner.Triage must roll cache tokens into TokensIn so the TUI
// reflects the full prompt size, not the freshly-billed slice (which for
// Claude is often single digits even for huge prompts).
func TestLiveTriageRunner_TokensInIncludesCacheTokens(t *testing.T) {
	originalNewAgent := newAgent
	t.Cleanup(func() { newAgent = originalNewAgent })

	newAgent = func(_ sharedtypes.AgentName, _ string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange: sharedtypes.StateChangeNone,
					Rationale:   "ok",
					WaitingOn:   sharedtypes.WaitingOnContributor,
					Confidence:  sharedtypes.ConfidenceMedium,
				}},
			}),
			Usage: agent.TokenUsage{
				InputTokens:         7,
				OutputTokens:        170,
				CacheReadTokens:     43597,
				CacheCreationTokens: 11422,
			},
		}}, nil
	}

	runner := &liveTriageRunner{name: sharedtypes.AgentClaude, bin: "claude", cwd: t.TempDir()}
	got, err := runner.Triage(context.Background(), daemon.TriageRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("Triage() error = %v", err)
	}

	if want := 7 + 43597 + 11422; got.TokensIn != want {
		t.Errorf("TokensIn = %d, want %d (input + cache_read + cache_creation)", got.TokensIn, want)
	}
	if got.TokensOut != 170 {
		t.Errorf("TokensOut = %d, want 170", got.TokensOut)
	}
}

type stubTriageAgent struct {
	result *agent.Result
	err    error
	onRun  func(agent.RunOpts)
}

func (s stubTriageAgent) Name() string { return "codex" }

func (s stubTriageAgent) Run(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
	if s.onRun != nil {
		s.onRun(opts)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func (s stubTriageAgent) Close() error { return nil }

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	return parsed
}
