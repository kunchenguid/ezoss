package cli

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/kunchenguid/ezoss/internal/telemetry"
	"github.com/kunchenguid/ezoss/internal/update"
)

func TestRootCommandIncludesUpdateSubcommand(t *testing.T) {
	cmd := NewRootCmd()

	got, _, err := cmd.Find([]string{"update"})
	if err != nil {
		t.Fatalf("Find(update) error = %v", err)
	}
	if got == nil || got.Name() != "update" {
		t.Fatalf("Find(update) = %v, want update command", got)
	}
}

func TestUpdateCommandRunsUpdateCheck(t *testing.T) {
	original := runUpdate
	t.Cleanup(func() {
		runUpdate = original
	})

	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(resetTelemetry)

	runUpdate = func(ctx context.Context, stdout, stderr io.Writer, _ update.Options) error {
		_, _ = stdout.Write([]byte("update available: v1.2.3 -> v1.2.4\n"))
		return nil
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"update"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := buf.String(); got != "update available: v1.2.3 -> v1.2.4\n" {
		t.Fatalf("output = %q, want %q", got, "update available: v1.2.3 -> v1.2.4\n")
	}

	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "update" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "update")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "update" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "update")
	}
}

func TestUpdateCommandPassesBetaFlag(t *testing.T) {
	original := runUpdate
	t.Cleanup(func() {
		runUpdate = original
	})

	var capturedOpts update.Options
	runUpdate = func(_ context.Context, _, _ io.Writer, opts update.Options) error {
		capturedOpts = opts
		return nil
	}

	cmd := NewRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"update", "--beta"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !capturedOpts.Beta {
		t.Fatal("expected runUpdate to receive Beta=true when --beta flag is set")
	}
}

func TestUpdateCommandDefaultsBetaToFalse(t *testing.T) {
	original := runUpdate
	t.Cleanup(func() {
		runUpdate = original
	})

	var capturedOpts update.Options
	runUpdate = func(_ context.Context, _, _ io.Writer, opts update.Options) error {
		capturedOpts = opts
		return nil
	}

	cmd := NewRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"update"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if capturedOpts.Beta {
		t.Fatal("expected runUpdate to receive Beta=false by default")
	}
}
