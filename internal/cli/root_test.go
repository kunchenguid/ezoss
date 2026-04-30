package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/kunchenguid/ezoss/internal/buildinfo"
	"github.com/kunchenguid/ezoss/internal/doctor"
	"github.com/kunchenguid/ezoss/internal/update"
)

func TestGitNoPromptEnvDisablesInteractiveCredentialFallbacks(t *testing.T) {
	env := gitNoPromptEnv()
	for _, want := range []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
		"GCM_INTERACTIVE=never",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("gitNoPromptEnv() missing %q\nfull env: %v", want, env)
		}
	}
}

func TestRootCommandPrintsVersion(t *testing.T) {
	originalVersion := buildinfo.Version
	originalCommit := buildinfo.Commit
	originalDate := buildinfo.Date
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
		buildinfo.Commit = originalCommit
		buildinfo.Date = originalDate
	})

	buildinfo.Version = "v1.2.3"
	buildinfo.Commit = "abc123"
	buildinfo.Date = "2026-04-18"

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "ezoss v1.2.3 (abc123) 2026-04-18\n" {
		t.Fatalf("output = %q, want %q", got, "ezoss v1.2.3 (abc123) 2026-04-18\n")
	}
}

func TestRootCommandIncludesInitSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"init"})
	if err != nil {
		t.Fatalf("Find(init) error = %v", err)
	}
	if got == nil || got.Name() != "init" {
		t.Fatalf("Find(init) = %v, want init command", got)
	}
}

func TestRootCommandIncludesListSubcommandFromRootTests(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"list"})
	if err != nil {
		t.Fatalf("Find(list) error = %v", err)
	}
	if got == nil || got.Name() != "list" {
		t.Fatalf("Find(list) = %v, want list command", got)
	}
}

func TestRootCommandIncludesFixSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"fix"})
	if err != nil {
		t.Fatalf("Find(fix) error = %v", err)
	}
	if got == nil || got.Name() != "fix" {
		t.Fatalf("Find(fix) = %v, want fix command", got)
	}
}

func TestRootCommandAppliesShellEnvironmentBeforeRunningSubcommands(t *testing.T) {
	originalApplyShellEnv := applyShellEnv
	originalRunUpdate := runUpdate
	originalCloseTelemetry := closeTelemetry
	t.Cleanup(func() {
		applyShellEnv = originalApplyShellEnv
		runUpdate = originalRunUpdate
		closeTelemetry = originalCloseTelemetry
	})

	calledApply := false
	applyShellEnv = func() error {
		calledApply = true
		return nil
	}
	runUpdate = func(context.Context, io.Writer, io.Writer, update.Options) error {
		return nil
	}
	closeTelemetry = func() {}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"update"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !calledApply {
		t.Fatal("applyShellEnv() was not called")
	}
}

func TestRootCommandClosesTelemetryAfterSubcommandRuns(t *testing.T) {
	originalApplyShellEnv := applyShellEnv
	originalRunUpdate := runUpdate
	originalCloseTelemetry := closeTelemetry
	t.Cleanup(func() {
		applyShellEnv = originalApplyShellEnv
		runUpdate = originalRunUpdate
		closeTelemetry = originalCloseTelemetry
	})

	applyShellEnv = func() error {
		return nil
	}
	runUpdate = func(context.Context, io.Writer, io.Writer, update.Options) error {
		return nil
	}
	closed := false
	closeTelemetry = func() {
		closed = true
	}

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"update"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !closed {
		t.Fatal("closeTelemetry() was not called")
	}
}

func TestRootCommandClosesTelemetryAfterSubcommandError(t *testing.T) {
	originalApplyShellEnv := applyShellEnv
	originalRunUpdate := runUpdate
	originalCloseTelemetry := closeTelemetry
	t.Cleanup(func() {
		applyShellEnv = originalApplyShellEnv
		runUpdate = originalRunUpdate
		closeTelemetry = originalCloseTelemetry
	})

	applyShellEnv = func() error {
		return nil
	}
	runUpdate = func(context.Context, io.Writer, io.Writer, update.Options) error {
		return errors.New("boom")
	}
	closed := false
	closeTelemetry = func() {
		closed = true
	}

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"update"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if got := err.Error(); got != "boom" {
		t.Fatalf("error = %q, want %q", got, "boom")
	}
	if !closed {
		t.Fatal("closeTelemetry() was not called")
	}
}

func TestDoctorCommandRunsEvenWhenShellEnvironmentSetupFails(t *testing.T) {
	originalApplyShellEnv := applyShellEnv
	originalRunDoctor := runDoctor
	t.Cleanup(func() {
		applyShellEnv = originalApplyShellEnv
		runDoctor = originalRunDoctor
	})

	applyShellEnv = func() error {
		return errors.New("apply shell environment: permission denied")
	}
	calledDoctor := false
	runDoctor = func(context.Context) []doctor.Result {
		calledDoctor = true
		return []doctor.Result{{Name: "shell environment", OK: false, Detail: "shell command timed out"}}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"doctor"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if got := err.Error(); got != "doctor found failing checks" {
		t.Fatalf("error = %q, want %q", got, "doctor found failing checks")
	}
	if !calledDoctor {
		t.Fatal("runDoctor() was not called")
	}
	if output := buf.String(); !bytes.Contains([]byte(output), []byte("fail shell environment: shell command timed out")) {
		t.Fatalf("output %q does not contain doctor check result", output)
	}
}

func TestRootVersionRunsEvenWhenShellEnvironmentSetupFails(t *testing.T) {
	originalApplyShellEnv := applyShellEnv
	t.Cleanup(func() {
		applyShellEnv = originalApplyShellEnv
	})

	applyShellEnv = func() error {
		return errors.New("apply shell environment: permission denied")
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got == "" {
		t.Fatal("version output is empty")
	}
}

func TestRootHelpRunsEvenWhenShellEnvironmentSetupFails(t *testing.T) {
	originalApplyShellEnv := applyShellEnv
	t.Cleanup(func() {
		applyShellEnv = originalApplyShellEnv
	})

	applyShellEnv = func() error {
		return errors.New("apply shell environment: permission denied")
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); !bytes.Contains([]byte(got), []byte("Maintainer-side issue and PR triage orchestrator")) {
		t.Fatalf("help output %q does not contain command description", got)
	}
}
