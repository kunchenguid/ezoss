package agent

import (
	"strings"
	"testing"
)

func TestParseRovodevSSE_PartStartThenDelta(t *testing.T) {
	input := `event: part_start
data: {"index":0,"part":{"content":"{\"success\":true","part_kind":"text"},"event_kind":"part_start"}

event: part_delta
data: {"index":0,"delta":{"content_delta":",\"summary\":\"done\"}","part_delta_kind":"text"},"event_kind":"part_delta"}

`
	var usage TokenUsage
	var latestText string
	var chunks []string

	err := parseRovodevSSE(strings.NewReader(input), func(text string) {
		chunks = append(chunks, text)
	}, &usage, &latestText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantText := `{"success":true,"summary":"done"}`
	if latestText != wantText {
		t.Errorf("expected latest text %q, got %q", wantText, latestText)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
}

func TestBuildRovodevSystemPrompt_IncludesJSONInstructions(t *testing.T) {
	prompt := buildRovodevSystemPrompt([]byte(`{"type":"object"}`))
	for _, want := range []string{
		"reply with only valid JSON",
		"Do not wrap the JSON in markdown fences.",
		`{"type":"object"}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}
