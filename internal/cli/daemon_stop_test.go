package cli

import (
	"bytes"
	"testing"

	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
)

func TestRootCommandIncludesDaemonStopSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"daemon", "stop"})
	if err != nil {
		t.Fatalf("Find(daemon stop) error = %v", err)
	}
	if got == nil || got.Name() != "stop" {
		t.Fatalf("Find(daemon stop) = %v, want daemon stop command", got)
	}
}

func TestDaemonStopCommandPrintsStopped(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStopDaemon := stopDaemon
	t.Cleanup(func() {
		newPaths = originalNewPaths
		stopDaemon = originalStopDaemon
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	calledWith := ""
	stopDaemon = func(pidFile string) error {
		calledWith = pidFile
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "stop"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if calledWith != paths.WithRoot(tempRoot).PIDPath() {
		t.Fatalf("stopDaemon called with %q, want %q", calledWith, paths.WithRoot(tempRoot).PIDPath())
	}
	if got := buf.String(); got != "stopped\n" {
		t.Fatalf("output = %q, want %q", got, "stopped\n")
	}
}

func TestDaemonStopCommandTracksTelemetry(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalStopDaemon := stopDaemon
	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(func() {
		newPaths = originalNewPaths
		stopDaemon = originalStopDaemon
		resetTelemetry()
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	stopDaemon = func(string) error {
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"daemon", "stop"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "daemon_stop" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "daemon_stop")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "daemon.stop" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "daemon.stop")
	}
}
