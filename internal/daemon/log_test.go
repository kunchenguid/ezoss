package daemon

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLoggerOmitsTimestamp(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)
	logger.Info("daemon started", "version", "v1.2.3")

	out := buf.String()
	if strings.Contains(out, "time=") {
		t.Fatalf("logger output should not contain time= attribute (timestamps come from the log pipe): %q", out)
	}
	if !strings.Contains(out, "version=v1.2.3") {
		t.Fatalf("logger output should contain key=value pair: %q", out)
	}
	if !strings.Contains(out, `msg="daemon started"`) {
		t.Fatalf("logger output should contain msg: %q", out)
	}
	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("logger output should contain level: %q", out)
	}
}

func TestNewLoggerLevelEnv(t *testing.T) {
	t.Setenv("EZOSS_LOG_LEVEL", "warn")
	var buf bytes.Buffer
	logger := NewLogger(&buf)
	logger.Info("info-line")
	logger.Warn("warn-line")
	out := buf.String()
	if strings.Contains(out, "info-line") {
		t.Fatalf("info-line should be filtered when level=warn: %q", out)
	}
	if !strings.Contains(out, "warn-line") {
		t.Fatalf("warn-line should be present: %q", out)
	}
}

func TestNewLoggerNilWriterFallsBackToStderr(t *testing.T) {
	// Just verify it returns a non-nil logger and doesn't panic.
	logger := NewLogger(nil)
	if logger == nil {
		t.Fatal("NewLogger(nil) returned nil")
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "INFO"},
		{"info", "INFO"},
		{"INFO", "INFO"},
		{"debug", "DEBUG"},
		{"warn", "WARN"},
		{"warning", "WARN"},
		{"error", "ERROR"},
		{"  Debug ", "DEBUG"},
		{"bogus", "INFO"},
	}
	for _, c := range cases {
		got := parseLogLevel(c.in).String()
		if got != c.want {
			t.Errorf("parseLogLevel(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
