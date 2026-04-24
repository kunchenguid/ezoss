package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kunchenguid/ezoss/internal/doctor"
	"github.com/kunchenguid/ezoss/internal/telemetry"
)

func TestDoctorCommandPrintsChecks(t *testing.T) {
	original := runDoctor
	t.Cleanup(func() {
		runDoctor = original
	})

	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(resetTelemetry)

	runDoctor = func(context.Context) []doctor.Result {
		return []doctor.Result{
			{Name: "state directory", OK: true, Detail: "/tmp/.ezoss"},
			{Name: "gh CLI", OK: true, Detail: "/usr/bin/gh"},
			{Name: "shell environment", OK: true, Detail: "loaded 12 variables"},
		}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"doctor"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"ok state directory: /tmp/.ezoss",
		"ok gh CLI: /usr/bin/gh",
		"ok shell environment: loaded 12 variables",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q does not contain %q", output, want)
		}
	}

	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "doctor" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "doctor")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "doctor" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "doctor")
	}
}

func TestDoctorCommandFailsWhenAnyCheckFails(t *testing.T) {
	original := runDoctor
	t.Cleanup(func() {
		runDoctor = original
	})

	runDoctor = func(context.Context) []doctor.Result {
		return []doctor.Result{{Name: "gh CLI", OK: false, Detail: "not found in PATH"}}
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

	if output := buf.String(); !strings.Contains(output, "fail gh CLI: not found in PATH") {
		t.Fatalf("output %q does not contain failing check", output)
	}
}

func TestDoctorCommandPrintsWarningsWithoutFailing(t *testing.T) {
	original := runDoctor
	t.Cleanup(func() {
		runDoctor = original
	})

	runDoctor = func(context.Context) []doctor.Result {
		return []doctor.Result{{Name: "daemon", Warning: true, Detail: "stopped; run `ezoss daemon start` to resume polling"}}
	}

	buf := &bytes.Buffer{}
	cmd := NewRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"doctor"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if output := buf.String(); !strings.Contains(output, "warn daemon: stopped; run `ezoss daemon start` to resume polling") {
		t.Fatalf("output %q does not contain warning", output)
	}
}
