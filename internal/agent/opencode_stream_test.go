package agent

import (
	"strings"
	"testing"
)

func TestOpencodeTokensToUsage(t *testing.T) {
	tokens := &opencodeTokens{
		Input:  100,
		Output: 50,
		Cache:  &opencodeCache{Read: 30, Write: 10},
	}
	u := opencodeTokensToUsage(tokens)
	if u.InputTokens != 100 || u.OutputTokens != 50 || u.CacheReadTokens != 30 || u.CacheCreationTokens != 10 {
		t.Fatalf("unexpected usage: %+v", u)
	}
}

func TestParseOpencodeSSE_PartUpdated_Text(t *testing.T) {
	input := `data: {"payload":{"type":"message.part.updated","properties":{"sessionID":"s1","part":{"id":"p1","messageID":"asst-msg","type":"text","text":"final text","metadata":{"openai":{"phase":"final_answer"}}}}}}

data: {"payload":{"type":"message.updated","properties":{"sessionID":"s1","info":{"id":"asst-msg","role":"assistant"}}}}

data: {"payload":{"type":"session.idle"}}

`
	state := &opencodeStreamState{
		sessionID:  "s1",
		textParts:  make(map[string]*opencodeTextPart),
		usageByMsg: make(map[string]TokenUsage),
	}

	err := parseOpencodeSSE(strings.NewReader(input), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.lastText != "final text" {
		t.Fatalf("expected lastText final text, got %q", state.lastText)
	}
	if state.lastFinalText != "final text" {
		t.Fatalf("expected lastFinalText final text, got %q", state.lastFinalText)
	}
}
