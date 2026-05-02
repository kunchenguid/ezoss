package fix

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kunchenguid/ezoss/internal/paths"
)

type GitRunner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
type GHRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

type WorktreeOptions struct {
	Root         string
	RepoID       string
	Number       int
	BranchPrefix string
	RunGit       GitRunner
	RunGH        GHRunner
}

type Worktree struct {
	BasePath     string
	WorktreePath string
	Branch       string
	BaseRef      string
}

var runIDCounter atomic.Uint64

func PrepareWorktree(ctx context.Context, opts WorktreeOptions) (Worktree, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		p, err := paths.New()
		if err != nil {
			return Worktree{}, err
		}
		root = p.Root()
	}
	repoID := strings.TrimSpace(opts.RepoID)
	if repoID == "" || !strings.Contains(repoID, "/") {
		return Worktree{}, fmt.Errorf("invalid repo %q", opts.RepoID)
	}
	if opts.Number <= 0 {
		return Worktree{}, fmt.Errorf("invalid item number %d", opts.Number)
	}
	runGit := opts.RunGit
	if runGit == nil {
		runGit = runGitCommand
	}
	runGH := opts.RunGH
	if runGH == nil {
		runGH = runGHCommand
	}

	safeRepo := SafeRepoName(repoID)
	checkoutPath := filepath.Join(root, "investigations", safeRepo)
	if err := os.MkdirAll(filepath.Dir(checkoutPath), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("create investigations dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(checkoutPath, ".git")); err != nil {
		if !os.IsNotExist(err) {
			return Worktree{}, fmt.Errorf("inspect investigation checkout: %w", err)
		}
		if err := os.RemoveAll(checkoutPath); err != nil {
			return Worktree{}, fmt.Errorf("remove invalid investigation checkout: %w", err)
		}
		if _, err := runGH(ctx, filepath.Dir(checkoutPath), "repo", "clone", repoID, checkoutPath); err != nil {
			return Worktree{}, err
		}
	}

	if _, err := runGit(ctx, checkoutPath, nil, "fetch", "--prune", "origin"); err != nil {
		return Worktree{}, err
	}
	_, _ = runGit(ctx, checkoutPath, nil, "remote", "set-head", "origin", "-a")
	baseRef := "origin/main"
	if out, err := runGit(ctx, checkoutPath, nil, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(out))
		if ref != "" && ref != "origin/HEAD" {
			baseRef = ref
		}
	} else if out, err := runGit(ctx, checkoutPath, nil, "symbolic-ref", "--short", "HEAD"); err == nil {
		ref := strings.TrimSpace(string(out))
		if ref != "" && ref != "HEAD" {
			baseRef = ref
		}
	}

	run := runID()
	branch := branchName(opts.BranchPrefix, opts.Number, run)
	worktreePath := filepath.Join(root, "fixes", safeRepo, strconv.Itoa(opts.Number)+"-"+run)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("create fix worktree parent: %w", err)
	}
	if _, err := runGit(ctx, checkoutPath, nil, "worktree", "add", "-b", branch, worktreePath, baseRef); err != nil {
		return Worktree{}, err
	}

	return Worktree{BasePath: checkoutPath, WorktreePath: worktreePath, Branch: branch, BaseRef: baseRef}, nil
}

// ContribWorktreeOptions describes what the contributor fix runner
// needs to push to an existing PR's head branch on the fork (or
// upstream, when the user has push access there).
type ContribWorktreeOptions struct {
	Root     string
	HeadRepo string // owner/name where the PR branch lives
	HeadRef  string // branch name on HeadRepo
	CloneURL string // git URL to clone (typically <head>.git)
	Number   int    // upstream PR number, used for path naming
	RunGit   GitRunner
}

// PrepareContribWorktree clones HeadRepo (the PR head, often a fork),
// checks out HeadRef into an isolated worktree, and returns the
// worktree path. Unlike PrepareWorktree it does NOT create a new branch
// - the contributor already has a PR open against an existing branch
// and we want to push more commits to it. BaseRef is set to HeadRef so
// downstream "do we have new commits" checks compare against the
// pre-fix tip of the same branch.
func PrepareContribWorktree(ctx context.Context, opts ContribWorktreeOptions) (Worktree, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		p, err := paths.New()
		if err != nil {
			return Worktree{}, err
		}
		root = p.Root()
	}
	if strings.TrimSpace(opts.HeadRepo) == "" || !strings.Contains(opts.HeadRepo, "/") {
		return Worktree{}, fmt.Errorf("invalid head repo %q", opts.HeadRepo)
	}
	if strings.TrimSpace(opts.HeadRef) == "" {
		return Worktree{}, fmt.Errorf("head ref required")
	}
	if strings.TrimSpace(opts.CloneURL) == "" {
		return Worktree{}, fmt.Errorf("clone url required")
	}
	runGit := opts.RunGit
	if runGit == nil {
		runGit = runGitCommand
	}

	safeRepo := SafeRepoName(opts.HeadRepo)
	checkoutPath := filepath.Join(root, "investigations", safeRepo)
	if err := os.MkdirAll(filepath.Dir(checkoutPath), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("create investigations dir: %w", err)
	}
	if _, err := os.Stat(filepath.Join(checkoutPath, ".git")); err != nil {
		if !os.IsNotExist(err) {
			return Worktree{}, fmt.Errorf("inspect contrib checkout: %w", err)
		}
		if err := os.RemoveAll(checkoutPath); err != nil {
			return Worktree{}, fmt.Errorf("remove invalid contrib checkout: %w", err)
		}
		if _, err := runGit(ctx, filepath.Dir(checkoutPath), nil, "clone", opts.CloneURL, checkoutPath); err != nil {
			return Worktree{}, err
		}
	}

	if _, err := runGit(ctx, checkoutPath, nil, "fetch", "--prune", "origin"); err != nil {
		return Worktree{}, err
	}

	run := runID()
	number := opts.Number
	if number <= 0 {
		number = 0
	}
	branch := branchName("ezoss/contrib", number, run)
	worktreePath := filepath.Join(root, "fixes", safeRepo, strconv.Itoa(number)+"-"+run)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("create contrib fix worktree parent: %w", err)
	}
	if _, err := runGit(ctx, checkoutPath, nil, "worktree", "add", worktreePath, "origin/"+opts.HeadRef); err != nil {
		return Worktree{}, err
	}
	if _, err := runGit(ctx, worktreePath, nil, "checkout", "-B", branch, "origin/"+opts.HeadRef); err != nil {
		return Worktree{}, err
	}

	return Worktree{
		BasePath:     checkoutPath,
		WorktreePath: worktreePath,
		Branch:       branch,
		BaseRef:      "origin/" + opts.HeadRef,
	}, nil
}

func SafeRepoName(repoID string) string {
	return strings.NewReplacer("/", "__", "\\", "__", ":", "_").Replace(repoID)
}

func branchName(prefix string, number int, run string) string {
	if strings.TrimSpace(prefix) == "" {
		prefix = "ezoss/"
	}
	name := strings.TrimRight(prefix, "/") + "/fix-" + strconv.Itoa(number)
	if strings.TrimSpace(run) != "" {
		name += "-" + strings.TrimSpace(run)
	}
	return name
}

func runID() string {
	now := time.Now().UTC()
	return now.Format("20060102-150405") + "-" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.FormatUint(runIDCounter.Add(1), 36)
}

func runGitCommand(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), noPromptEnv()...)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func runGHCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), noPromptEnv()...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func noPromptEnv() []string {
	return []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "GCM_INTERACTIVE=never"}
}
