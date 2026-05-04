package shellenv

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestResolveUsesLoginShellAndCapturesEnv(t *testing.T) {
	resetForTests()
	t.Setenv("SHELL", "/bin/bash")

	oldOutput := shellCommandOutput
	defer func() {
		shellCommandOutput = oldOutput
		resetForTests()
	}()

	var gotShell string
	var gotArgs []string
	shellCommandOutput = func(shell string, args ...string) ([]byte, error) {
		gotShell = shell
		gotArgs = append([]string(nil), args...)
		return []byte("PATH=/resolved/bin\x00HOME=/Users/test\x00SPECIAL=1\x00"), nil
	}

	env, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if gotShell != "/bin/bash" {
		t.Fatalf("shell = %q, want %q", gotShell, "/bin/bash")
	}
	if !reflect.DeepEqual(gotArgs, []string{"-l", "-i", "-c", "env -0"}) {
		t.Fatalf("shell args = %v", gotArgs)
	}
	for _, want := range []string{"PATH=/resolved/bin", "HOME=/Users/test", "SPECIAL=1"} {
		if !containsEnvEntry(env, want) {
			t.Fatalf("expected resolved env to contain %q, got %v", want, env)
		}
	}
}

func TestApplyToProcessSetsResolvedEnvEntries(t *testing.T) {
	resetForTests()
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("KEEP_ME", "1")

	oldOutput := shellCommandOutput
	defer func() {
		shellCommandOutput = oldOutput
		resetForTests()
	}()

	shellCommandOutput = func(shell string, args ...string) ([]byte, error) {
		return []byte("PATH=/resolved/bin\x00HOME=/Users/test\x00SPECIAL=1\x00"), nil
	}

	if err := ApplyToProcess(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("PATH"); got != "/resolved/bin" {
		t.Fatalf("PATH = %q", got)
	}
	if got := os.Getenv("HOME"); got != "/Users/test" {
		t.Fatalf("HOME = %q", got)
	}
	if got := os.Getenv("SPECIAL"); got != "1" {
		t.Fatalf("SPECIAL = %q", got)
	}
	if got := os.Getenv("KEEP_ME"); got != "1" {
		t.Fatalf("KEEP_ME = %q", got)
	}
}

func TestParseEnvOutputIgnoresShellNoiseBeforeEnv(t *testing.T) {
	env := parseEnvOutput([]byte("banner text\nPATH=/resolved/bin\x00HOME=/Users/test\x00SPECIAL=1\x00"))

	want := []string{"PATH=/resolved/bin", "HOME=/Users/test", "SPECIAL=1"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestDefaultShellCommandOutputTimesOut(t *testing.T) {
	oldTimeout := shellCommandTimeout
	defer func() {
		shellCommandTimeout = oldTimeout
	}()

	shellCommandTimeout = 20 * time.Millisecond
	t.Setenv("EZOSS_SHELLENV_TIMEOUT_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = defaultShellCommandOutput(exe, "-test.run=^TestDefaultShellCommandOutputTimeoutHelper$", "--")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("command ran too long: %v", elapsed)
	}
}

func TestDefaultShellCommandOutputTimeoutHelper(t *testing.T) {
	if os.Getenv("EZOSS_SHELLENV_TIMEOUT_HELPER") != "1" {
		return
	}

	time.Sleep(time.Second)
	os.Exit(0)
}

func containsEnvEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
