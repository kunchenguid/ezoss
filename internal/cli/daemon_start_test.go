package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
)

func TestRootCommandIncludesDaemonStartSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"daemon", "start"})
	if err != nil {
		t.Fatalf("Find(daemon start) error = %v", err)
	}
	if got == nil || got.Name() != "start" {
		t.Fatalf("Find(daemon start) = %v, want daemon start command", got)
	}
}

func TestDaemonStartCommandPrintsStarted(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStartDaemon := startDaemon
	t.Cleanup(func() {
		newPaths = originalNewPaths
		startDaemon = originalStartDaemon
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Repos: []string{"kunchenguid/ezoss"},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	calledWith := ""
	startDaemon = func(pidFile string, useMock bool) error {
		calledWith = pidFile
		if useMock {
			t.Fatal("expected mock mode to be disabled")
		}
		return nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"daemon", "start"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if calledWith != paths.WithRoot(tempRoot).PIDPath() {
		t.Fatalf("startDaemon called with %q, want %q", calledWith, paths.WithRoot(tempRoot).PIDPath())
	}
	if got := stdout.String(); got != "started\n" {
		t.Fatalf("stdout = %q, want %q", got, "started\n")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestDaemonStartCommandWarnsWhenNoReposConfigured(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStartDaemon := startDaemon
	t.Cleanup(func() {
		newPaths = originalNewPaths
		startDaemon = originalStartDaemon
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	startDaemon = func(string, bool) error {
		return nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"daemon", "start"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := stdout.String(); got != "started\n" {
		t.Fatalf("stdout = %q, want %q", got, "started\n")
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "no repos configured") {
		t.Fatalf("stderr = %q, want warning about missing repos", errOut)
	}
	if !strings.Contains(errOut, "ezoss init --repo") {
		t.Fatalf("stderr = %q, want hint command", errOut)
	}
}

func TestDaemonStartCommandSkipsWarningUnderMock(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStartDaemon := startDaemon
	t.Cleanup(func() {
		newPaths = originalNewPaths
		startDaemon = originalStartDaemon
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	startDaemon = func(string, bool) error {
		return nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"daemon", "start", "--mock"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := stdout.String(); got != "started\n" {
		t.Fatalf("stdout = %q, want %q", got, "started\n")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty under --mock", got)
	}
}

func TestDaemonStartCommandPassesMockFlag(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStartDaemon := startDaemon
	t.Cleanup(func() {
		newPaths = originalNewPaths
		startDaemon = originalStartDaemon
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	calledWithMock := false
	startDaemon = func(pidFile string, useMock bool) error {
		calledWithMock = useMock
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "start", "--mock"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !calledWithMock {
		t.Fatal("expected startDaemon to be called with mock mode enabled")
	}
}

func TestDaemonStartCommandTracksTelemetry(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStartDaemon := startDaemon
	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(func() {
		newPaths = originalNewPaths
		startDaemon = originalStartDaemon
		resetTelemetry()
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	startDaemon = func(string, bool) error {
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "start"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "daemon_start" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "daemon_start")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "daemon.start" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "daemon.start")
	}
}
