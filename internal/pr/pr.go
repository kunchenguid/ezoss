package pr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"
)

type Mode string

const (
	ModeAuto       Mode = "auto"
	ModeNoMistakes Mode = "no-mistakes"
	ModeGH         Mode = "gh"
	ModeDisabled   Mode = "disabled"
)

type Resolution struct {
	Requested Mode
	Mode      Mode
	Binary    string
	Skipped   []Skip
}

type Skip struct {
	Mode   Mode
	Reason string
}

func Resolve(mode Mode, lookPath func(string) (string, error)) (Resolution, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	switch normalizeMode(mode) {
	case ModeDisabled:
		return Resolution{Requested: ModeDisabled, Mode: ModeDisabled}, nil
	case ModeNoMistakes:
		bin, err := lookPath("no-mistakes")
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve no-mistakes PR creator: %w", err)
		}
		if _, err := lookPath("gh"); err != nil {
			return Resolution{}, fmt.Errorf("resolve no-mistakes PR creator gh dependency: %w", err)
		}
		return Resolution{Requested: ModeNoMistakes, Mode: ModeNoMistakes, Binary: bin}, nil
	case ModeGH:
		bin, err := lookPath("gh")
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve gh PR creator: %w", err)
		}
		return Resolution{Requested: ModeGH, Mode: ModeGH, Binary: bin}, nil
	case ModeAuto:
		return resolveAuto(lookPath)
	default:
		return Resolution{}, fmt.Errorf("invalid PR create mode %q", mode)
	}
}

func resolveAuto(lookPath func(string) (string, error)) (Resolution, error) {
	var skipped []Skip
	for _, candidate := range []struct {
		mode Mode
		bin  string
	}{
		{ModeNoMistakes, "no-mistakes"},
		{ModeGH, "gh"},
	} {
		bin, err := lookPath(candidate.bin)
		if err == nil {
			if candidate.mode == ModeNoMistakes {
				if _, err := lookPath("gh"); err != nil {
					if !isNotFound(err) {
						return Resolution{}, fmt.Errorf("resolve no-mistakes PR creator gh dependency: %w", err)
					}
					skipped = append(skipped, Skip{Mode: candidate.mode, Reason: "gh executable not found"})
					continue
				}
			}
			return Resolution{Requested: ModeAuto, Mode: candidate.mode, Binary: bin, Skipped: skipped}, nil
		}
		if !isNotFound(err) {
			return Resolution{}, fmt.Errorf("resolve %s PR creator: %w", candidate.mode, err)
		}
		skipped = append(skipped, Skip{Mode: candidate.mode, Reason: "executable not found"})
	}
	return Resolution{}, fmt.Errorf("no PR creator found in PATH (looked for: no-mistakes, gh)")
}

func normalizeMode(mode Mode) Mode {
	trimmed := Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	if trimmed == "" {
		return ModeAuto
	}
	return trimmed
}

func isNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

type CreateOptions struct {
	RepoID         string
	WorktreePath   string
	Base           string
	Head           string
	Title          string
	Body           string
	Draft          bool
	DetectAttempts int
	DetectInterval time.Duration
}

type Created struct {
	URL string
}

type noMistakesDetectionError struct {
	err error
}

func (e *noMistakesDetectionError) Error() string {
	return e.err.Error()
}

func (e *noMistakesDetectionError) Unwrap() error {
	return e.err
}

func IsNoMistakesDetectionError(err error) bool {
	var target *noMistakesDetectionError
	return errors.As(err, &target)
}

type CommandRunner func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)

func Create(ctx context.Context, mode Mode, opts CreateOptions, run CommandRunner) (Created, error) {
	if run == nil {
		run = runCommand
	}
	if opts.WorktreePath == "" {
		return Created{}, fmt.Errorf("worktree path is required")
	}
	if opts.Head == "" {
		return Created{}, fmt.Errorf("head branch is required")
	}
	if opts.Title == "" {
		return Created{}, fmt.Errorf("PR title is required")
	}
	if opts.Base == "" {
		opts.Base = "main"
	}

	if mode == ModeNoMistakes {
		return createWithNoMistakes(ctx, opts, run)
	} else if mode == ModeGH {
		if _, err := run(ctx, opts.WorktreePath, "git", "push", "-u", "origin", "HEAD:"+opts.Head); err != nil {
			return Created{}, err
		}
	} else if mode == ModeDisabled {
		return Created{}, nil
	} else {
		return Created{}, fmt.Errorf("PR create mode must be resolved before Create, got %q", mode)
	}

	args := []string{"pr", "create", "--base", opts.Base, "--head", opts.Head, "--title", opts.Title, "--body", opts.Body}
	if opts.RepoID != "" {
		args = append(args, "--repo", opts.RepoID)
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	out, err := run(ctx, opts.WorktreePath, "gh", args...)
	if err != nil {
		return Created{}, err
	}
	return Created{URL: strings.TrimSpace(string(out))}, nil
}

func createWithNoMistakes(ctx context.Context, opts CreateOptions, run CommandRunner) (Created, error) {
	if _, err := run(ctx, opts.WorktreePath, "git", "remote", "get-url", "no-mistakes"); err != nil {
		if _, err := run(ctx, opts.WorktreePath, "no-mistakes", "init"); err != nil {
			return Created{}, err
		}
	}
	if _, err := run(ctx, opts.WorktreePath, "git", "push", "no-mistakes", "HEAD:"+opts.Head); err != nil {
		return Created{}, err
	}
	url, err := detectPullRequest(ctx, opts, run)
	if err != nil {
		return Created{}, err
	}
	return Created{URL: url}, nil
}

func detectPullRequest(ctx context.Context, opts CreateOptions, run CommandRunner) (string, error) {
	attempts := opts.DetectAttempts
	if attempts <= 0 {
		attempts = 120
		if opts.DetectInterval == 0 {
			opts.DetectInterval = 5 * time.Second
		}
	}
	interval := opts.DetectInterval
	if interval < 0 {
		interval = 0
	}

	args := []string{"pr", "list", "--head", opts.Head, "--state", "all", "--json", "url", "--jq", ".[0].url"}
	if opts.RepoID != "" {
		args = append(args, "--repo", opts.RepoID)
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		out, err := run(ctx, opts.WorktreePath, "gh", args...)
		if err == nil {
			if url := strings.TrimSpace(string(out)); url != "" && url != "null" {
				return url, nil
			}
		} else {
			lastErr = err
		}
		if attempt == attempts-1 {
			break
		}
		if interval > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr != nil {
		return "", &noMistakesDetectionError{err: fmt.Errorf("detect no-mistakes PR for %s: %w", opts.Head, lastErr)}
	}
	return "", &noMistakesDetectionError{err: fmt.Errorf("detect no-mistakes PR for %s: PR not found", opts.Head)}
}

func runCommand(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
