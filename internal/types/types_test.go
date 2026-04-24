package types

import (
	"database/sql/driver"
	"encoding/json"
	"testing"
)

func TestAgentNameJSONRoundTrip(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(AgentCodex)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(raw) != `"codex"` {
		t.Fatalf("Marshal() = %s, want %q", string(raw), `"codex"`)
	}

	var got AgentName
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got != AgentCodex {
		t.Fatalf("Unmarshal() = %q, want %q", got, AgentCodex)
	}
	if !got.IsSupported() {
		t.Fatalf("IsSupported() = false, want true")
	}
}

func TestAgentNameUnsupported(t *testing.T) {
	t.Parallel()

	var got AgentName = "made-up"
	if got.IsSupported() {
		t.Fatalf("IsSupported() = true, want false")
	}
}

func TestEnumScanAndValue(t *testing.T) {
	t.Parallel()

	var kind ItemKind
	if err := kind.Scan([]byte("pr")); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if kind != ItemKindPR {
		t.Fatalf("Scan() = %q, want %q", kind, ItemKindPR)
	}

	var state ItemState
	if err := state.Scan("merged"); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if state != ItemStateMerged {
		t.Fatalf("Scan() = %q, want %q", state, ItemStateMerged)
	}

	var waiting WaitingOn
	if err := waiting.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) error = %v", err)
	}
	if waiting != "" {
		t.Fatalf("Scan(nil) = %q, want empty", waiting)
	}

	value, err := StateChangeClose.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	if value != driver.Value("close") {
		t.Fatalf("Value() = %v, want %q", value, "close")
	}
}

func TestEnumScanRejectsUnsupportedSourceType(t *testing.T) {
	t.Parallel()

	var confidence Confidence
	err := confidence.Scan(123)
	if err == nil {
		t.Fatal("Scan() error = nil, want error")
	}
}
