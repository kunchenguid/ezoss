package fix

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareWorktreeUsesDedicatedFixWorktreeFromInvestigationCheckout(t *testing.T) {
	root := t.TempDir()
	var gitCalls [][]string
	var ghCalls [][]string

	runGit := func(_ context.Context, dir string, _ []string, args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, append([]string{dir}, args...))
		switch {
		case reflect.DeepEqual(args, []string{"remote", "set-head", "origin", "-a"}):
			return nil, nil
		case reflect.DeepEqual(args, []string{"rev-parse", "--abbrev-ref", "origin/HEAD"}):
			return []byte("origin/main\n"), nil
		case len(args) == 6 && args[0] == "worktree" && args[1] == "add":
			worktree := args[4]
			if err := os.MkdirAll(worktree, 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: test\n"), 0o644); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	runGh := func(_ context.Context, dir string, args ...string) ([]byte, error) {
		ghCalls = append(ghCalls, append([]string{dir}, args...))
		checkout := args[3]
		if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
			return nil, err
		}
		return nil, nil
	}

	got, err := PrepareWorktree(context.Background(), WorktreeOptions{
		Root:   root,
		RepoID: "acme/widgets",
		Number: 42,
		RunGit: runGit,
		RunGH:  runGh,
	})
	if err != nil {
		t.Fatalf("PrepareWorktree() error = %v", err)
	}

	if got.BasePath != filepath.Join(root, "investigations", "acme__widgets") {
		t.Fatalf("BasePath = %q, want investigation checkout under root/investigations", got.BasePath)
	}
	if !strings.HasPrefix(got.WorktreePath, filepath.Join(root, "fixes", "acme__widgets", "42-")) {
		t.Fatalf("WorktreePath = %q, want dedicated fix path", got.WorktreePath)
	}
	if strings.Contains(got.WorktreePath, "investigations") {
		t.Fatalf("WorktreePath = %q, must not use investigations checkout", got.WorktreePath)
	}
	if !strings.HasPrefix(got.Branch, "ezoss/fix-42-") {
		t.Fatalf("Branch = %q, want issue branch prefix", got.Branch)
	}
	if len(ghCalls) != 1 {
		t.Fatalf("gh calls = %#v, want one investigation checkout clone", ghCalls)
	}
	if !containsGitCall(gitCalls, got.BasePath, "worktree", "add", "-b", got.Branch, got.WorktreePath, "origin/main") {
		t.Fatalf("git calls = %#v, want worktree add from investigation checkout", gitCalls)
	}
}

func TestPrepareWorktreeUsesUniqueBranchesForRepeatedItemFixes(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "investigations", "acme__widgets")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	branches := map[string]bool{}
	runGit := func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
		switch {
		case reflect.DeepEqual(args, []string{"rev-parse", "--abbrev-ref", "origin/HEAD"}):
			return []byte("origin/main\n"), nil
		case len(args) == 6 && args[0] == "worktree" && args[1] == "add":
			branch := args[3]
			if branches[branch] {
				return nil, errors.New("branch already exists")
			}
			branches[branch] = true
			return nil, os.MkdirAll(args[4], 0o755)
		}
		return nil, nil
	}

	first, err := PrepareWorktree(context.Background(), WorktreeOptions{Root: root, RepoID: "acme/widgets", Number: 42, RunGit: runGit})
	if err != nil {
		t.Fatalf("first PrepareWorktree() error = %v", err)
	}
	second, err := PrepareWorktree(context.Background(), WorktreeOptions{Root: root, RepoID: "acme/widgets", Number: 42, RunGit: runGit})
	if err != nil {
		t.Fatalf("second PrepareWorktree() error = %v", err)
	}

	if first.Branch == second.Branch {
		t.Fatalf("branches = %q and %q, want unique branches", first.Branch, second.Branch)
	}
	if !strings.HasPrefix(first.Branch, "ezoss/fix-42-") || !strings.HasPrefix(second.Branch, "ezoss/fix-42-") {
		t.Fatalf("branches = %q and %q, want item-specific fix prefixes", first.Branch, second.Branch)
	}
}

func TestPrepareWorktreeReusesExistingInvestigationCheckoutWithoutCloning(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "investigations", "acme__widgets")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	var ghCalls int
	_, err := PrepareWorktree(context.Background(), WorktreeOptions{
		Root:   root,
		RepoID: "acme/widgets",
		Number: 42,
		RunGit: func(_ context.Context, _ string, _ []string, args ...string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"rev-parse", "--abbrev-ref", "origin/HEAD"}) {
				return []byte("origin/main\n"), nil
			}
			if len(args) == 6 && args[0] == "worktree" && args[1] == "add" {
				return nil, os.MkdirAll(args[4], 0o755)
			}
			return nil, nil
		},
		RunGH: func(context.Context, string, ...string) ([]byte, error) {
			ghCalls++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("PrepareWorktree() error = %v", err)
	}
	if ghCalls != 0 {
		t.Fatalf("gh calls = %d, want existing investigation checkout reused", ghCalls)
	}
}

func TestPrepareWorktreeFallsBackToInvestigationCheckoutHEAD(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "investigations", "acme__widgets")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	var gitCalls [][]string
	got, err := PrepareWorktree(context.Background(), WorktreeOptions{
		Root:   root,
		RepoID: "acme/widgets",
		Number: 42,
		RunGit: func(_ context.Context, dir string, _ []string, args ...string) ([]byte, error) {
			gitCalls = append(gitCalls, append([]string{dir}, args...))
			switch {
			case reflect.DeepEqual(args, []string{"rev-parse", "--abbrev-ref", "origin/HEAD"}):
				return nil, errors.New("origin/HEAD missing")
			case reflect.DeepEqual(args, []string{"symbolic-ref", "--short", "HEAD"}):
				return []byte("main\n"), nil
			case len(args) == 6 && args[0] == "worktree" && args[1] == "add":
				return nil, os.MkdirAll(args[4], 0o755)
			}
			return nil, nil
		},
		RunGH: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("RunGH called for existing investigation checkout")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("PrepareWorktree() error = %v", err)
	}
	if got.BaseRef != "main" {
		t.Fatalf("BaseRef = %q, want investigation checkout HEAD branch", got.BaseRef)
	}
	if !containsGitCall(gitCalls, got.BasePath, "worktree", "add", "-b", got.Branch, got.WorktreePath, "main") {
		t.Fatalf("git calls = %#v, want worktree add from investigation checkout HEAD", gitCalls)
	}
}

func containsGitCall(calls [][]string, dir string, args ...string) bool {
	want := append([]string{dir}, args...)
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}
