package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
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
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	stubAgentLookPath(t, map[string]string{
		"codex": "/usr/local/bin/codex",
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "kunchenguid/ezoss" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "kunchenguid/ezoss")
		}
		return resolvedWorkingDir, nil
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
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "Needs logs before debugging.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Please share the daemon log around the crash.",
					Confidence:   sharedtypes.ConfidenceMedium,
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
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
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
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	stubAgentLookPath(t, map[string]string{
		"claude": "/usr/local/bin/claude",
		"codex":  "/usr/local/bin/codex",
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "kunchenguid/ezoss" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "kunchenguid/ezoss")
		}
		return workingDir, nil
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
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
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
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	stubAgentLookPath(t, map[string]string{
		"claude": "/usr/local/bin/claude",
		"codex":  "/usr/local/bin/codex",
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "kunchenguid/ezoss" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "kunchenguid/ezoss")
		}
		return workingDir, nil
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
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "kunchenguid/ezoss" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "kunchenguid/ezoss")
		}
		return t.TempDir(), nil
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
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	t.Cleanup(func() {
		newAgent = originalNewAgent
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
	})
	prepareInvestigationCheckout = func(_ context.Context, _ string, _ string) (string, error) {
		return "", nil
	}

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

func TestLiveTriageRunnerPreparesInvestigationCheckoutForRepoItem(t *testing.T) {
	originalNewAgent := newAgent
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	t.Cleanup(func() {
		newAgent = originalNewAgent
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
	})

	root := t.TempDir()
	checkout := filepath.Join(root, "investigations", "kunchenguid__no-mistakes")
	var preparedRoot, preparedRepo string
	prepareInvestigationCheckout = func(_ context.Context, gotRoot string, gotRepo string) (string, error) {
		preparedRoot = gotRoot
		preparedRepo = gotRepo
		return checkout, nil
	}

	var gotOpts agent.RunOpts
	newAgent = func(_ sharedtypes.AgentName, _ string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{Options: []triage.RecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Rationale:   "ok",
				WaitingOn:   sharedtypes.WaitingOnNone,
				Confidence:  sharedtypes.ConfidenceHigh,
			}}}),
		}, onRun: func(opts agent.RunOpts) {
			gotOpts = opts
		}}, nil
	}

	runner := &liveTriageRunner{name: sharedtypes.AgentClaude, bin: "claude", cwd: t.TempDir(), stateRoot: root}
	_, err := runner.Triage(context.Background(), daemon.TriageRequest{
		Item:   ghclient.Item{Repo: "kunchenguid/no-mistakes"},
		Prompt: "base prompt",
	})
	if err != nil {
		t.Fatalf("Triage() error = %v", err)
	}

	if preparedRoot != root || preparedRepo != "kunchenguid/no-mistakes" {
		t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", preparedRoot, preparedRepo, root, "kunchenguid/no-mistakes")
	}
	if gotOpts.CWD != checkout {
		t.Fatalf("RunOpts.CWD = %q, want checkout %q", gotOpts.CWD, checkout)
	}
	for _, want := range []string{"Repository checkout for investigation:", checkout, "Do not push", "Local edits are scratch"} {
		if !strings.Contains(gotOpts.Prompt, want) {
			t.Fatalf("RunOpts.Prompt = %q, missing %q", gotOpts.Prompt, want)
		}
	}
}

func TestLiveTriageRunnerSerializesInvestigationCheckoutUse(t *testing.T) {
	originalNewAgent := newAgent
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	t.Cleanup(func() {
		newAgent = originalNewAgent
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
	})

	root := t.TempDir()
	checkout := filepath.Join(root, "investigations", "kunchenguid__no-mistakes")
	prepared := make(chan struct{}, 2)
	prepareInvestigationCheckout = func(_ context.Context, _ string, _ string) (string, error) {
		prepared <- struct{}{}
		return checkout, nil
	}

	firstRunStarted := make(chan struct{})
	releaseFirstRun := make(chan struct{})
	var mu sync.Mutex
	runCount := 0
	newAgent = func(_ sharedtypes.AgentName, _ string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSON(t, triage.Recommendation{Options: []triage.RecommendationOption{{
				StateChange: sharedtypes.StateChangeNone,
				Rationale:   "ok",
				WaitingOn:   sharedtypes.WaitingOnNone,
				Confidence:  sharedtypes.ConfidenceHigh,
			}}}),
		}, onRun: func(agent.RunOpts) {
			mu.Lock()
			runCount++
			currentRun := runCount
			mu.Unlock()
			if currentRun == 1 {
				close(firstRunStarted)
				<-releaseFirstRun
			}
		}}, nil
	}

	runner := &liveTriageRunner{name: sharedtypes.AgentClaude, bin: "claude", cwd: t.TempDir(), stateRoot: root}
	errCh := make(chan error, 2)
	req := daemon.TriageRequest{Item: ghclient.Item{Repo: "kunchenguid/no-mistakes"}, Prompt: "base prompt"}
	go func() {
		_, err := runner.Triage(context.Background(), req)
		errCh <- err
	}()

	<-prepared
	<-firstRunStarted
	go func() {
		_, err := runner.Triage(context.Background(), req)
		errCh <- err
	}()

	select {
	case <-prepared:
		t.Fatal("second triage prepared checkout while first agent still held it")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseFirstRun)
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("Triage() error = %v", err)
		}
	}
}

func TestAcquireInvestigationCheckoutLockRecoversStaleLock(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, "investigations", ".locks", "kunchenguid__no-mistakes.lock")
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatalf("create stale lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	release, err := acquireInvestigationCheckoutLock(ctx, root, "kunchenguid/no-mistakes")
	if err != nil {
		t.Fatalf("acquireInvestigationCheckoutLock() error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
}

func TestAcquireInvestigationCheckoutLockRecoversStalePIDLock(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, "investigations", ".locks", "kunchenguid__no-mistakes.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("create lock dir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("999999\n"), 0o644); err != nil {
		t.Fatalf("create stale lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	t.Cleanup(cancel)
	release, err := acquireInvestigationCheckoutLock(ctx, root, "kunchenguid/no-mistakes")
	if err != nil {
		t.Fatalf("acquireInvestigationCheckoutLock() error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
}

func TestPreparePersistentInvestigationCheckoutCreatesAndCleansClone(t *testing.T) {
	root := t.TempDir()
	originalGitHubAuthToken := gitHubAuthToken
	t.Cleanup(func() {
		gitHubAuthToken = originalGitHubAuthToken
	})
	gitHubAuthToken = func(_ context.Context) (string, error) {
		return "", nil
	}

	var calls []gitCommandCall
	runner := func(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
		calls = append(calls, gitCommandCall{dir: dir, env: append([]string(nil), env...), args: append([]string(nil), args...)})
		if len(args) > 0 && args[0] == "clone" {
			if err := os.MkdirAll(filepath.Join(root, "investigations", "kunchenguid__no-mistakes", ".git"), 0o755); err != nil {
				return nil, err
			}
		}
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("origin/main\n"), nil
		}
		return nil, nil
	}

	checkout, err := preparePersistentInvestigationCheckout(context.Background(), root, "kunchenguid/no-mistakes", runner)
	if err != nil {
		t.Fatalf("preparePersistentInvestigationCheckout() error = %v", err)
	}

	wantCheckout := filepath.Join(root, "investigations", "kunchenguid__no-mistakes")
	if checkout != wantCheckout {
		t.Fatalf("checkout = %q, want %q", checkout, wantCheckout)
	}
	want := []gitCommandCall{
		{dir: filepath.Join(root, "investigations"), args: []string{"clone", "https://github.com/kunchenguid/no-mistakes.git", wantCheckout}},
		{dir: wantCheckout, args: []string{"fetch", "--prune", "origin"}},
		{dir: wantCheckout, args: []string{"remote", "set-head", "origin", "-a"}},
		{dir: wantCheckout, args: []string{"rev-parse", "--abbrev-ref", "origin/HEAD"}},
		{dir: wantCheckout, args: []string{"reset", "--hard"}},
		{dir: wantCheckout, args: []string{"clean", "-fdx"}},
		{dir: wantCheckout, args: []string{"checkout", "--detach", "origin/main"}},
		{dir: wantCheckout, args: []string{"reset", "--hard", "origin/main"}},
		{dir: wantCheckout, args: []string{"clean", "-fdx"}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("git calls = %#v, want %#v", calls, want)
	}
}

func TestPreparePersistentInvestigationCheckoutUsesGhTokenForGitHubGit(t *testing.T) {
	root := t.TempDir()
	originalGitHubAuthToken := gitHubAuthToken
	t.Cleanup(func() {
		gitHubAuthToken = originalGitHubAuthToken
	})
	gitHubAuthToken = func(_ context.Context) (string, error) {
		return "gho_private", nil
	}

	var calls []gitCommandCall
	runner := func(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
		calls = append(calls, gitCommandCall{dir: dir, env: append([]string(nil), env...), args: append([]string(nil), args...)})
		if len(args) > 0 && args[0] == "clone" {
			if err := os.MkdirAll(filepath.Join(root, "investigations", "kunchenguid__no-mistakes", ".git"), 0o755); err != nil {
				return nil, err
			}
		}
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("origin/main\n"), nil
		}
		return nil, nil
	}

	_, err := preparePersistentInvestigationCheckout(context.Background(), root, "kunchenguid/no-mistakes", runner)
	if err != nil {
		t.Fatalf("preparePersistentInvestigationCheckout() error = %v", err)
	}

	wantEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: bearer gho_private",
	}
	for _, call := range calls {
		if !reflect.DeepEqual(call.env, wantEnv) {
			t.Fatalf("git call env = %#v, want %#v", call.env, wantEnv)
		}
	}
}

type gitCommandCall struct {
	dir  string
	env  []string
	args []string
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

func stubAgentLookPath(t *testing.T, bins map[string]string) {
	t.Helper()

	originalLookPath := lookPath
	t.Cleanup(func() {
		lookPath = originalLookPath
	})
	lookPath = func(file string) (string, error) {
		if path, ok := bins[file]; ok {
			return path, nil
		}
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}
}

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
