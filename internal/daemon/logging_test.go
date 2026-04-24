package daemon

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPipeTimestampsPrefixesEachLine(t *testing.T) {
	t.Parallel()

	clock := func() time.Time {
		return time.Date(2026, 4, 25, 10, 33, 7, 123_000_000, time.UTC)
	}

	in := strings.NewReader("first line\nsecond line\n")
	var out bytes.Buffer

	if err := pipeTimestamps(in, &out, clock); err != nil {
		t.Fatalf("pipeTimestamps() error = %v", err)
	}

	got := out.String()
	want := "2026-04-25T10:33:07.123Z first line\n2026-04-25T10:33:07.123Z second line\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPipeTimestampsHandlesUnterminatedFinalLine(t *testing.T) {
	t.Parallel()

	clock := func() time.Time {
		return time.Date(2026, 4, 25, 10, 33, 7, 0, time.UTC)
	}

	in := strings.NewReader("only line without newline")
	var out bytes.Buffer

	if err := pipeTimestamps(in, &out, clock); err != nil {
		t.Fatalf("pipeTimestamps() error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "only line without newline") {
		t.Fatalf("output missing line content: %q", got)
	}
	matched, _ := regexp.MatchString(`^2026-04-25T10:33:07\.000Z only line without newline\n?$`, got)
	if !matched {
		t.Fatalf("output not timestamped as expected: %q", got)
	}
}
