package pr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestResolveAutoPrefersNoMistakesWhenAvailable(t *testing.T) {
	got, err := Resolve(ModeAuto, fakeLookPath(map[string]bool{
		"no-mistakes": true,
		"gh":          true,
	}))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if got.Mode != ModeNoMistakes {
		t.Fatalf("Mode = %q, want %q", got.Mode, ModeNoMistakes)
	}
	if got.Binary != "/bin/no-mistakes" {
		t.Fatalf("Binary = %q, want no-mistakes path", got.Binary)
	}
}

func TestResolveAutoFallsBackToGH(t *testing.T) {
	got, err := Resolve(ModeAuto, fakeLookPath(map[string]bool{
		"gh": true,
	}))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if got.Mode != ModeGH {
		t.Fatalf("Mode = %q, want %q", got.Mode, ModeGH)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Mode != ModeNoMistakes {
		t.Fatalf("Skipped = %#v, want no-mistakes skip", got.Skipped)
	}
}

func TestResolveExplicitNoMistakesFailsWhenUnavailable(t *testing.T) {
	_, err := Resolve(ModeNoMistakes, fakeLookPath(map[string]bool{}))
	if err == nil {
		t.Fatal("Resolve() error = nil, want missing no-mistakes error")
	}
}

func TestResolveExplicitNoMistakesRequiresGHForDetection(t *testing.T) {
	_, err := Resolve(ModeNoMistakes, fakeLookPath(map[string]bool{
		"no-mistakes": true,
	}))
	if err == nil {
		t.Fatal("Resolve() error = nil, want missing gh error")
	}
}

func TestResolveAutoSkipsNoMistakesWhenGHUnavailable(t *testing.T) {
	_, err := Resolve(ModeAuto, fakeLookPath(map[string]bool{
		"no-mistakes": true,
	}))
	if err == nil {
		t.Fatal("Resolve() error = nil, want missing PR creator error")
	}
}

func fakeLookPath(available map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if available[name] {
			return "/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestResolvePropagatesUnexpectedProbeErrors(t *testing.T) {
	want := errors.New("permission denied")
	_, err := Resolve(ModeAuto, func(name string) (string, error) {
		if name == "no-mistakes" {
			return "", want
		}
		return "", exec.ErrNotFound
	})
	if !errors.Is(err, want) {
		t.Fatalf("Resolve() error = %v, want %v", err, want)
	}
}

func TestRunCommandDisablesInteractivePrompts(t *testing.T) {
	if os.Getenv("EZOSS_PR_RUN_COMMAND_ENV_HELPER") == "1" {
		if os.Getenv("GIT_TERMINAL_PROMPT") != "0" || os.Getenv("GIT_ASKPASS") != "true" || os.Getenv("GCM_INTERACTIVE") != "never" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	t.Setenv("EZOSS_PR_RUN_COMMAND_ENV_HELPER", "1")

	if _, err := runCommand(context.Background(), "", os.Args[0], "-test.run=TestRunCommandDisablesInteractivePrompts"); err != nil {
		t.Fatalf("runCommand() error = %v", err)
	}
}

func TestCreateWithGHUsesOriginPushThenGHPRCreate(t *testing.T) {
	var calls [][]string
	created, err := Create(context.Background(), ModeGH, CreateOptions{
		RepoID:       "acme/widgets",
		WorktreePath: "/tmp/worktree",
		Base:         "main",
		Head:         "ezoss/issue-42-fix",
		Title:        "Fix parser crash",
		Body:         "Fixes #42",
		Draft:        true,
	}, func(_ context.Context, dir string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{dir, name}, args...))
		if name == "gh" {
			return []byte("https://github.com/acme/widgets/pull/99\n"), nil
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.URL != "https://github.com/acme/widgets/pull/99" {
		t.Fatalf("URL = %q, want PR URL", created.URL)
	}
	want := [][]string{
		{"/tmp/worktree", "git", "push", "-u", "origin", "HEAD:ezoss/issue-42-fix"},
		{"/tmp/worktree", "gh", "pr", "create", "--base", "main", "--head", "ezoss/issue-42-fix", "--title", "Fix parser crash", "--body", "Fixes #42", "--repo", "acme/widgets", "--draft"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCreateWithNoMistakesUsesExistingRemotePushesBranchAndDetectsPR(t *testing.T) {
	var calls [][]string
	created, err := Create(context.Background(), ModeNoMistakes, CreateOptions{
		WorktreePath:   "/tmp/worktree",
		Base:           "main",
		Head:           "ezoss/fix-42",
		Title:          "Fix parser crash",
		Body:           "Fixes #42",
		RepoID:         "acme/widgets",
		DetectAttempts: 1,
	}, func(_ context.Context, dir string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{dir, name}, args...))
		if name == "git" && reflect.DeepEqual(args, []string{"remote", "get-url", "no-mistakes"}) {
			return []byte("/tmp/gate.git\n"), nil
		}
		if name == "gh" {
			return []byte("https://github.com/acme/widgets/pull/99\n"), nil
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.URL != "https://github.com/acme/widgets/pull/99" {
		t.Fatalf("URL = %q, want detected PR URL", created.URL)
	}
	want := [][]string{
		{"/tmp/worktree", "git", "remote", "get-url", "no-mistakes"},
		{"/tmp/worktree", "git", "push", "no-mistakes", "HEAD:ezoss/fix-42"},
		{"/tmp/worktree", "gh", "pr", "list", "--head", "ezoss/fix-42", "--state", "all", "--json", "url", "--jq", ".[0].url", "--repo", "acme/widgets"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCreateWithNoMistakesInitializesWhenRemoteMissing(t *testing.T) {
	var calls [][]string
	_, err := Create(context.Background(), ModeNoMistakes, CreateOptions{
		WorktreePath:   "/tmp/worktree",
		Base:           "main",
		Head:           "ezoss/fix-42",
		Title:          "Fix parser crash",
		Body:           "Fixes #42",
		DetectAttempts: 1,
	}, func(_ context.Context, dir string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{dir, name}, args...))
		if name == "git" && reflect.DeepEqual(args, []string{"remote", "get-url", "no-mistakes"}) {
			return nil, exec.ErrNotFound
		}
		return []byte("https://github.com/acme/widgets/pull/99\n"), nil
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	want := [][]string{
		{"/tmp/worktree", "git", "remote", "get-url", "no-mistakes"},
		{"/tmp/worktree", "no-mistakes", "init"},
		{"/tmp/worktree", "git", "push", "no-mistakes", "HEAD:ezoss/fix-42"},
		{"/tmp/worktree", "gh", "pr", "list", "--head", "ezoss/fix-42", "--state", "all", "--json", "url", "--jq", ".[0].url"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCreateWithNoMistakesPollsUntilPRExists(t *testing.T) {
	var calls [][]string
	ghCalls := 0
	created, err := Create(context.Background(), ModeNoMistakes, CreateOptions{
		WorktreePath:   "/tmp/worktree",
		Head:           "ezoss/fix-42",
		Title:          "Fix parser crash",
		Body:           "Fixes #42",
		DetectAttempts: 2,
		DetectInterval: 0,
	}, func(_ context.Context, dir string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{dir, name}, args...))
		if name == "gh" {
			ghCalls++
			if ghCalls == 1 {
				return []byte("\n"), nil
			}
			return []byte("https://github.com/acme/widgets/pull/99\n"), nil
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.URL != "https://github.com/acme/widgets/pull/99" {
		t.Fatalf("URL = %q, want PR URL from second detection attempt", created.URL)
	}
	if ghCalls != 2 {
		t.Fatalf("gh detection calls = %d, want 2; calls=%#v", ghCalls, calls)
	}
}

func TestCreateWithNoMistakesMarksDetectionErrors(t *testing.T) {
	want := errors.New("gh unavailable")
	_, err := Create(context.Background(), ModeNoMistakes, CreateOptions{
		WorktreePath:   "/tmp/worktree",
		Head:           "ezoss/fix-42",
		Title:          "Fix parser crash",
		Body:           "Fixes #42",
		DetectAttempts: 1,
	}, func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "gh" {
			return nil, want
		}
		return nil, nil
	})
	if !IsNoMistakesDetectionError(err) {
		t.Fatalf("IsNoMistakesDetectionError(%v) = false, want true", err)
	}
	if !errors.Is(err, want) {
		t.Fatalf("Create() error = %v, want wrapped %v", err, want)
	}
}
