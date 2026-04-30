package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/ezoss/internal/agent"
	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/db"
	fixflow "github.com/kunchenguid/ezoss/internal/fix"
	"github.com/kunchenguid/ezoss/internal/paths"
	prcreator "github.com/kunchenguid/ezoss/internal/pr"
	"github.com/kunchenguid/ezoss/internal/tui"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestParseFixTarget(t *testing.T) {
	repo, number, err := parseFixTarget("acme/widgets#42")
	if err != nil {
		t.Fatalf("parseFixTarget() error = %v", err)
	}
	if repo != "acme/widgets" || number != 42 {
		t.Fatalf("parseFixTarget() = %q, %d, want acme/widgets, 42", repo, number)
	}
}

func TestFixCommandPreparesIsolatedWorktreeAndResolvesConfiguredPRCreator(t *testing.T) {
	root := t.TempDir()
	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalApplyShellEnv := applyShellEnv
	originalCloseTelemetry := closeTelemetry
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		applyShellEnv = originalApplyShellEnv
		closeTelemetry = originalCloseTelemetry
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(path string) (*config.GlobalConfig, error) {
		if path != filepath.Join(root, "config.yaml") {
			t.Fatalf("config path = %q, want root config", path)
		}
		return &config.GlobalConfig{Fixes: config.FixesConfig{PRCreate: config.PRCreateNoMistakes}}, nil
	}
	prepareFixWorktree = func(ctx context.Context, opts fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		if opts.Root != root || opts.RepoID != "acme/widgets" || opts.Number != 42 {
			t.Fatalf("worktree opts = %+v", opts)
		}
		return fixflow.Worktree{
			BasePath:     filepath.Join(root, "investigations", "acme__widgets"),
			WorktreePath: filepath.Join(root, "fixes", "acme__widgets", "42-run"),
			Branch:       "ezoss/fix-42",
			BaseRef:      "origin/main",
		}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		if mode != prcreator.ModeNoMistakes {
			t.Fatalf("PR mode = %q, want no-mistakes", mode)
		}
		return prcreator.Resolution{Mode: prcreator.ModeNoMistakes, Binary: "/bin/no-mistakes"}, nil
	}
	applyShellEnv = func() error { return nil }
	closeTelemetry = func() {}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"fix", "acme/widgets#42", "--prepare-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"repo: acme/widgets",
		"number: 42",
		"worktree: " + filepath.Join(root, "fixes", "acme__widgets", "42-run"),
		"pr_create: no-mistakes",
		"pr_create_binary: /bin/no-mistakes",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q in:\n%s", want, output)
		}
	}
}

func TestFixCommandFlagOverridesConfiguredPRCreator(t *testing.T) {
	root := t.TempDir()
	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalApplyShellEnv := applyShellEnv
	originalCloseTelemetry := closeTelemetry
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		applyShellEnv = originalApplyShellEnv
		closeTelemetry = originalCloseTelemetry
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{Fixes: config.FixesConfig{PRCreate: config.PRCreateNoMistakes}}, nil
	}
	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{WorktreePath: filepath.Join(root, "fixes", "x"), Branch: "ezoss/fix-42", BaseRef: "origin/main"}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		if mode != prcreator.ModeGH {
			t.Fatalf("PR mode = %q, want gh flag override", mode)
		}
		return prcreator.Resolution{Mode: prcreator.ModeGH, Binary: "/bin/gh"}, nil
	}
	applyShellEnv = func() error { return nil }
	closeTelemetry = func() {}

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"fix", "acme/widgets#42", "--pr-create", "gh", "--prepare-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestRunFixEntryWithNoMistakesCommitsBeforePushingToGate(t *testing.T) {
	root := t.TempDir()
	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalRunFixGitCommand := runFixGitCommand
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		newAgent = originalNewAgent
		lookPath = originalLookPath
		runFixGitCommand = originalRunFixGitCommand
		createFixPR = originalCreateFixPR
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{Agent: config.AgentCodex, Fixes: config.FixesConfig{PRCreate: config.PRCreateNoMistakes}}, nil
	}
	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{
			BasePath:     filepath.Join(root, "investigations", "acme__widgets"),
			WorktreePath: filepath.Join(root, "fixes", "acme__widgets", "42-run"),
			Branch:       "ezoss/fix-42",
			BaseRef:      "origin/main",
		}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		if mode != prcreator.ModeNoMistakes {
			t.Fatalf("PR mode = %q, want no-mistakes", mode)
		}
		return prcreator.Resolution{Mode: prcreator.ModeNoMistakes, Binary: "/bin/no-mistakes"}, nil
	}
	lookPath = func(name string) (string, error) {
		if name != "codex" {
			t.Fatalf("lookPath name = %q, want codex", name)
		}
		return "/bin/codex", nil
	}
	var gotRun agent.RunOpts
	newAgent = func(name sharedtypes.AgentName, bin string) (triageAgent, error) {
		if name != sharedtypes.AgentCodex || bin != "/bin/codex" {
			t.Fatalf("newAgent(%q, %q), want codex", name, bin)
		}
		return stubTriageAgent{result: &agent.Result{Text: "fixed", Output: json.RawMessage(`"fixed"`)}, onRun: func(opts agent.RunOpts) {
			gotRun = opts
		}}, nil
	}
	var gitCalls [][]string
	runFixGitCommand = func(_ context.Context, dir string, _ []string, args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, append([]string{dir}, args...))
		if reflect.DeepEqual(args, []string{"status", "--porcelain"}) {
			return []byte(" M internal/parser.go\n"), nil
		}
		if reflect.DeepEqual(args, []string{"rev-list", "--count", "origin/main..HEAD"}) {
			return []byte("1\n"), nil
		}
		return nil, nil
	}
	var prOpts prcreator.CreateOptions
	createFixPR = func(_ context.Context, mode prcreator.Mode, opts prcreator.CreateOptions, _ prcreator.CommandRunner) (prcreator.Created, error) {
		if mode != prcreator.ModeNoMistakes {
			t.Fatalf("create mode = %q, want no-mistakes", mode)
		}
		prOpts = opts
		return prcreator.Created{URL: "https://github.com/acme/widgets/pull/99"}, nil
	}

	result, err := runFixEntry(context.Background(), tui.Entry{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		StateChange:      sharedtypes.StateChangeFixRequired,
		FixPrompt:        "Fix the parser panic and add a regression test.",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runFixEntry() error = %v", err)
	}
	if result.PRURL != "https://github.com/acme/widgets/pull/99" {
		t.Fatalf("PRURL = %q, want created PR", result.PRURL)
	}
	if gotRun.CWD != filepath.Join(root, "fixes", "acme__widgets", "42-run") {
		t.Fatalf("agent CWD = %q, want fix worktree", gotRun.CWD)
	}
	if !strings.Contains(gotRun.Prompt, "Fix the parser panic") || !strings.Contains(gotRun.Prompt, "Repository checkout for fixing") {
		t.Fatalf("agent prompt = %q", gotRun.Prompt)
	}
	wantGit := [][]string{
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "status", "--porcelain"},
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "add", "-A"},
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "commit", "-m", "fix: acme/widgets#42"},
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "rev-list", "--count", "origin/main..HEAD"},
	}
	if !reflect.DeepEqual(gitCalls, wantGit) {
		t.Fatalf("git calls = %#v, want %#v", gitCalls, wantGit)
	}
	if prOpts.Head != "ezoss/fix-42" || prOpts.Title != "Fix acme/widgets#42: panic in parser" || !strings.Contains(prOpts.Body, "Fixes #42") {
		t.Fatalf("PR opts = %+v", prOpts)
	}
}

func TestRunFixEntryWithGHCommitsBeforeCreatingPR(t *testing.T) {
	root := t.TempDir()
	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalRunFixGitCommand := runFixGitCommand
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		newAgent = originalNewAgent
		lookPath = originalLookPath
		runFixGitCommand = originalRunFixGitCommand
		createFixPR = originalCreateFixPR
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{Agent: config.AgentCodex, Fixes: config.FixesConfig{PRCreate: config.PRCreateGH}}, nil
	}
	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{WorktreePath: filepath.Join(root, "fixes", "acme__widgets", "42-run"), Branch: "ezoss/fix-42", BaseRef: "origin/main"}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		if mode != prcreator.ModeGH {
			t.Fatalf("PR mode = %q, want gh", mode)
		}
		return prcreator.Resolution{Mode: prcreator.ModeGH, Binary: "/bin/gh"}, nil
	}
	lookPath = func(string) (string, error) { return "/bin/codex", nil }
	newAgent = func(sharedtypes.AgentName, string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{Text: "fixed"}}, nil
	}
	var gitCalls [][]string
	runFixGitCommand = func(_ context.Context, dir string, _ []string, args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, append([]string{dir}, args...))
		if reflect.DeepEqual(args, []string{"status", "--porcelain"}) {
			return []byte(" M internal/parser.go\n"), nil
		}
		if reflect.DeepEqual(args, []string{"rev-list", "--count", "origin/main..HEAD"}) {
			return []byte("1\n"), nil
		}
		return nil, nil
	}
	createFixPR = func(context.Context, prcreator.Mode, prcreator.CreateOptions, prcreator.CommandRunner) (prcreator.Created, error) {
		return prcreator.Created{URL: "https://github.com/acme/widgets/pull/99"}, nil
	}

	_, err := runFixEntry(context.Background(), tui.Entry{RepoID: "acme/widgets", Number: 42, Title: "panic in parser", FixPrompt: "Fix it."}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runFixEntry() error = %v", err)
	}
	wantGit := [][]string{
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "status", "--porcelain"},
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "add", "-A"},
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "commit", "-m", "fix: acme/widgets#42"},
		{filepath.Join(root, "fixes", "acme__widgets", "42-run"), "rev-list", "--count", "origin/main..HEAD"},
	}
	if !reflect.DeepEqual(gitCalls, wantGit) {
		t.Fatalf("git calls = %#v, want %#v", gitCalls, wantGit)
	}
}

func TestRunFixEntryFailsWhenAgentProducesNoCommits(t *testing.T) {
	root := t.TempDir()
	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalRunFixGitCommand := runFixGitCommand
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		newAgent = originalNewAgent
		lookPath = originalLookPath
		runFixGitCommand = originalRunFixGitCommand
		createFixPR = originalCreateFixPR
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{Agent: config.AgentCodex, Fixes: config.FixesConfig{PRCreate: config.PRCreateGH}}, nil
	}
	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{WorktreePath: filepath.Join(root, "fixes", "acme__widgets", "42-run"), Branch: "ezoss/fix-42", BaseRef: "origin/main"}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		return prcreator.Resolution{Mode: mode, Binary: "/bin/gh"}, nil
	}
	lookPath = func(string) (string, error) { return "/bin/codex", nil }
	newAgent = func(sharedtypes.AgentName, string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{Text: "fixed"}}, nil
	}
	runFixGitCommand = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"status", "--porcelain"}) {
			return nil, nil
		}
		if reflect.DeepEqual(args, []string{"rev-list", "--count", "origin/main..HEAD"}) {
			return []byte("0\n"), nil
		}
		return nil, nil
	}
	createFixPR = func(context.Context, prcreator.Mode, prcreator.CreateOptions, prcreator.CommandRunner) (prcreator.Created, error) {
		t.Fatal("createFixPR called despite no commits ahead of base")
		return prcreator.Created{}, nil
	}

	_, err := runFixEntry(context.Background(), tui.Entry{RepoID: "acme/widgets", Number: 42, Title: "panic in parser", FixPrompt: "Fix it."}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no fix changes produced") {
		t.Fatalf("runFixEntry() error = %v, want no fix changes produced", err)
	}
}

func TestRunFixEntryUsesRepoLocalAgentConfig(t *testing.T) {
	root := t.TempDir()
	worktreePath := filepath.Join(root, "fixes", "acme__widgets", "42-run")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".ezoss.yaml"), []byte("agent: opencode\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalRunFixGitCommand := runFixGitCommand
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		newAgent = originalNewAgent
		lookPath = originalLookPath
		runFixGitCommand = originalRunFixGitCommand
		createFixPR = originalCreateFixPR
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{Agent: config.AgentCodex, Fixes: config.FixesConfig{PRCreate: config.PRCreateGH}}, nil
	}
	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{WorktreePath: worktreePath, Branch: "ezoss/fix-42", BaseRef: "origin/main"}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		return prcreator.Resolution{Mode: mode, Binary: "/bin/gh"}, nil
	}
	lookPath = func(name string) (string, error) {
		if name != "opencode" {
			t.Fatalf("lookPath name = %q, want opencode", name)
		}
		return "/bin/opencode", nil
	}
	newAgent = func(name sharedtypes.AgentName, bin string) (triageAgent, error) {
		if name != sharedtypes.AgentOpenCode || bin != "/bin/opencode" {
			t.Fatalf("newAgent(%q, %q), want opencode", name, bin)
		}
		return stubTriageAgent{result: &agent.Result{Text: "fixed"}}, nil
	}
	runFixGitCommand = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"status", "--porcelain"}) {
			return []byte(" M internal/parser.go\n"), nil
		}
		if reflect.DeepEqual(args, []string{"rev-list", "--count", "origin/main..HEAD"}) {
			return []byte("1\n"), nil
		}
		return nil, nil
	}
	createFixPR = func(context.Context, prcreator.Mode, prcreator.CreateOptions, prcreator.CommandRunner) (prcreator.Created, error) {
		return prcreator.Created{URL: "https://github.com/acme/widgets/pull/99"}, nil
	}

	if _, err := runFixEntry(context.Background(), tui.Entry{RepoID: "acme/widgets", Number: 42, Title: "panic in parser", FixPrompt: "Fix it."}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runFixEntry() error = %v", err)
	}
}

func TestCLIFixRunnerFailsWhenAgentProducesNoCommits(t *testing.T) {
	root := t.TempDir()
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalRunFixGitCommand := runFixGitCommand
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		newAgent = originalNewAgent
		lookPath = originalLookPath
		runFixGitCommand = originalRunFixGitCommand
		createFixPR = originalCreateFixPR
	})

	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{WorktreePath: filepath.Join(root, "fixes", "acme__widgets", "42-run"), Branch: "ezoss/fix-42", BaseRef: "origin/main"}, nil
	}
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		return prcreator.Resolution{Mode: mode, Binary: "/bin/gh"}, nil
	}
	lookPath = func(string) (string, error) { return "/bin/codex", nil }
	newAgent = func(sharedtypes.AgentName, string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{Text: "fixed"}}, nil
	}
	runFixGitCommand = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"status", "--porcelain"}) {
			return nil, nil
		}
		if reflect.DeepEqual(args, []string{"rev-list", "--count", "origin/main..HEAD"}) {
			return []byte("0\n"), nil
		}
		return nil, nil
	}
	createFixPR = func(context.Context, prcreator.Mode, prcreator.CreateOptions, prcreator.CommandRunner) (prcreator.Created, error) {
		t.Fatal("createFixPR called despite no commits ahead of base")
		return prcreator.Created{}, nil
	}

	_, err := (cliFixRunner{root: root, cfg: &config.GlobalConfig{Agent: config.AgentCodex}}).RunFix(context.Background(), fixJobForTest(), nil)
	if err == nil || !strings.Contains(err.Error(), "no fix changes produced") {
		t.Fatalf("RunFix() error = %v, want no fix changes produced", err)
	}
}

func TestRunFixEntryAutoFallsBackToGHWhenNoMistakesCreateFails(t *testing.T) {
	root := t.TempDir()
	originalNewPaths := newPaths
	originalLoadGlobalConfig := loadGlobalConfig
	originalPrepareFixWorktree := prepareFixWorktree
	originalResolvePRCreator := resolvePRCreator
	originalNewAgent := newAgent
	originalLookPath := lookPath
	originalRunFixGitCommand := runFixGitCommand
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		newPaths = originalNewPaths
		loadGlobalConfig = originalLoadGlobalConfig
		prepareFixWorktree = originalPrepareFixWorktree
		resolvePRCreator = originalResolvePRCreator
		newAgent = originalNewAgent
		lookPath = originalLookPath
		runFixGitCommand = originalRunFixGitCommand
		createFixPR = originalCreateFixPR
	})

	newPaths = func() (*paths.Paths, error) { return paths.WithRoot(root), nil }
	loadGlobalConfig = func(string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{Agent: config.AgentCodex, Fixes: config.FixesConfig{PRCreate: config.PRCreateAuto}}, nil
	}
	prepareFixWorktree = func(context.Context, fixflow.WorktreeOptions) (fixflow.Worktree, error) {
		return fixflow.Worktree{WorktreePath: filepath.Join(root, "fixes", "acme__widgets", "42-run"), Branch: "ezoss/fix-42", BaseRef: "main"}, nil
	}
	var resolvedModes []prcreator.Mode
	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		resolvedModes = append(resolvedModes, mode)
		switch mode {
		case prcreator.ModeAuto:
			return prcreator.Resolution{Mode: prcreator.ModeNoMistakes, Requested: prcreator.ModeAuto, Binary: "/bin/no-mistakes"}, nil
		case prcreator.ModeGH:
			return prcreator.Resolution{Mode: prcreator.ModeGH, Requested: prcreator.ModeGH, Binary: "/bin/gh"}, nil
		default:
			t.Fatalf("unexpected PR mode resolution %q", mode)
			return prcreator.Resolution{}, nil
		}
	}
	lookPath = func(string) (string, error) { return "/bin/codex", nil }
	newAgent = func(sharedtypes.AgentName, string) (triageAgent, error) {
		return stubTriageAgent{result: &agent.Result{Text: "fixed"}}, nil
	}
	runFixGitCommand = func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"status", "--porcelain"}) {
			return []byte(" M internal/parser.go\n"), nil
		}
		if reflect.DeepEqual(args, []string{"rev-list", "--count", "main..HEAD"}) {
			return []byte("1\n"), nil
		}
		return nil, nil
	}
	var createModes []prcreator.Mode
	createFixPR = func(_ context.Context, mode prcreator.Mode, _ prcreator.CreateOptions, _ prcreator.CommandRunner) (prcreator.Created, error) {
		createModes = append(createModes, mode)
		if mode == prcreator.ModeNoMistakes {
			return prcreator.Created{}, errors.New("no-mistakes init failed")
		}
		return prcreator.Created{URL: "https://github.com/acme/widgets/pull/99"}, nil
	}

	result, err := runFixEntry(context.Background(), tui.Entry{RepoID: "acme/widgets", Number: 42, Title: "panic in parser", FixPrompt: "Fix it."}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runFixEntry() error = %v", err)
	}
	if result.PRURL != "https://github.com/acme/widgets/pull/99" {
		t.Fatalf("PRURL = %q, want gh fallback PR", result.PRURL)
	}
	if !reflect.DeepEqual(resolvedModes, []prcreator.Mode{prcreator.ModeAuto, prcreator.ModeGH}) {
		t.Fatalf("resolved modes = %#v, want auto then gh fallback", resolvedModes)
	}
	if !reflect.DeepEqual(createModes, []prcreator.Mode{prcreator.ModeNoMistakes, prcreator.ModeGH}) {
		t.Fatalf("create modes = %#v, want no-mistakes then gh fallback", createModes)
	}
}

func fixJobForTest() db.FixJob {
	return db.FixJob{ID: "fix-1", ItemID: "item-1", RecommendationID: "rec-1", OptionID: "opt-1", RepoID: "acme/widgets", ItemNumber: 42, ItemKind: sharedtypes.ItemKindIssue, Title: "panic in parser", FixPrompt: "Fix it.", PRCreate: string(prcreator.ModeGH)}
}

func TestCreateFixPRWithFallbackDoesNotFallbackAfterNoMistakesPush(t *testing.T) {
	originalResolvePRCreator := resolvePRCreator
	originalCreateFixPR := createFixPR
	t.Cleanup(func() {
		resolvePRCreator = originalResolvePRCreator
		createFixPR = originalCreateFixPR
	})

	resolvePRCreator = func(mode prcreator.Mode, _ func(string) (string, error)) (prcreator.Resolution, error) {
		if mode != prcreator.ModeGH {
			t.Fatalf("resolved fallback mode = %q, want gh", mode)
		}
		return prcreator.Resolution{Mode: prcreator.ModeGH, Requested: prcreator.ModeGH, Binary: "/bin/gh"}, nil
	}
	var createModes []prcreator.Mode
	createFixPR = func(ctx context.Context, mode prcreator.Mode, opts prcreator.CreateOptions, _ prcreator.CommandRunner) (prcreator.Created, error) {
		createModes = append(createModes, mode)
		if mode == prcreator.ModeGH {
			return prcreator.Created{URL: "https://github.com/acme/widgets/pull/99"}, nil
		}
		return prcreator.Create(ctx, mode, opts, func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
			if name == "gh" {
				return nil, errors.New("gh pr list failed")
			}
			return nil, nil
		})
	}

	_, _, err := createFixPRWithFallback(context.Background(), prcreator.Resolution{Mode: prcreator.ModeNoMistakes, Requested: prcreator.ModeAuto}, prcreator.CreateOptions{WorktreePath: "/tmp/worktree", Head: "ezoss/fix-42", Title: "Fix parser crash", Body: "Fixes #42", DetectAttempts: 1})
	if !prcreator.IsNoMistakesDetectionError(err) {
		t.Fatalf("IsNoMistakesDetectionError(%v) = false, want true", err)
	}
	if !reflect.DeepEqual(createModes, []prcreator.Mode{prcreator.ModeNoMistakes}) {
		t.Fatalf("create modes = %#v, want no gh fallback after no-mistakes push", createModes)
	}
}
