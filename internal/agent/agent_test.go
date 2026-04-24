package agent

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

func TestNew_KnownAgents(t *testing.T) {
	tests := []struct {
		name     string
		agent    sharedtypes.AgentName
		bin      string
		wantName string
	}{
		{name: "claude", agent: sharedtypes.AgentClaude, bin: "claude", wantName: "claude"},
		{name: "codex", agent: sharedtypes.AgentCodex, bin: "codex", wantName: "codex"},
		{name: "rovodev", agent: sharedtypes.AgentRovoDev, bin: "acli", wantName: "rovodev"},
		{name: "opencode", agent: sharedtypes.AgentOpenCode, bin: "opencode", wantName: "opencode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(tt.agent, tt.bin)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if a.Name() != tt.wantName {
				t.Errorf("expected name %q, got %q", tt.wantName, a.Name())
			}
		})
	}
}

func TestNew_Unknown(t *testing.T) {
	_, err := New("nonexistent", "foo")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("expected 'unknown agent' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), string(sharedtypes.AgentAuto)) {
		t.Errorf("expected auto agent option in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "config.yaml") {
		t.Errorf("expected config guidance in error, got: %v", err)
	}
}

func TestResolveAutoPrefersFirstAvailableAgent(t *testing.T) {
	resolved, bin, err := Resolve(sharedtypes.AgentAuto, func(name string) (string, error) {
		switch name {
		case "claude":
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		case "codex":
			return "/usr/local/bin/codex", nil
		default:
			t.Fatalf("unexpected lookup for %q", name)
			return "", nil
		}
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved != sharedtypes.AgentCodex {
		t.Fatalf("resolved agent = %q, want %q", resolved, sharedtypes.AgentCodex)
	}
	if bin != "/usr/local/bin/codex" {
		t.Fatalf("resolved bin = %q, want %q", bin, "/usr/local/bin/codex")
	}
}

func TestResolveReturnsConfiguredAgentBinary(t *testing.T) {
	resolved, bin, err := Resolve(sharedtypes.AgentRovoDev, func(name string) (string, error) {
		if name != "acli" {
			t.Fatalf("lookPath() name = %q, want %q", name, "acli")
		}
		return "/usr/local/bin/acli", nil
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved != sharedtypes.AgentRovoDev {
		t.Fatalf("resolved agent = %q, want %q", resolved, sharedtypes.AgentRovoDev)
	}
	if bin != "/usr/local/bin/acli" {
		t.Fatalf("resolved bin = %q, want %q", bin, "/usr/local/bin/acli")
	}
}

func TestResolveAutoErrorsWhenNoAgentIsAvailable(t *testing.T) {
	_, _, err := Resolve(sharedtypes.AgentAuto, func(name string) (string, error) {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	})
	if err == nil {
		t.Fatal("Resolve() error = nil, want error")
	}
	for _, want := range []string{"claude", "codex", "opencode", "acli"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve() error = %v, want mention of %q", err, want)
		}
	}
}

func TestResolveReturnsLookupErrorsForExplicitAgent(t *testing.T) {
	wantErr := errors.New("permission denied")
	_, _, err := Resolve(sharedtypes.AgentClaude, func(name string) (string, error) {
		if name != "claude" {
			t.Fatalf("lookPath() name = %q, want %q", name, "claude")
		}
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Resolve() error = %v, want %v", err, wantErr)
	}
}

func TestTokenUsage_Total(t *testing.T) {
	u := TokenUsage{
		InputTokens:         100,
		OutputTokens:        50,
		CacheReadTokens:     20,
		CacheCreationTokens: 10,
	}
	if u.Total() != 150 {
		t.Errorf("expected total 150, got %d", u.Total())
	}
}

func TestTokenUsage_TotalInputTokens(t *testing.T) {
	// TokensIn shown to users should reflect the full prompt size, not
	// just the freshly-billed (uncached) portion. For Claude, fresh
	// input_tokens is often single digits while cache_read holds the bulk.
	u := TokenUsage{
		InputTokens:         7,
		OutputTokens:        170,
		CacheReadTokens:     43597,
		CacheCreationTokens: 11422,
	}
	if got, want := u.TotalInputTokens(), 7+43597+11422; got != want {
		t.Errorf("TotalInputTokens() = %d, want %d", got, want)
	}
}

func TestTokenUsage_Add(t *testing.T) {
	a := TokenUsage{InputTokens: 100, OutputTokens: 50}
	b := TokenUsage{InputTokens: 200, OutputTokens: 75, CacheReadTokens: 30}
	a.Add(b)
	if a.InputTokens != 300 {
		t.Errorf("expected InputTokens 300, got %d", a.InputTokens)
	}
	if a.OutputTokens != 125 {
		t.Errorf("expected OutputTokens 125, got %d", a.OutputTokens)
	}
	if a.CacheReadTokens != 30 {
		t.Errorf("expected CacheReadTokens 30, got %d", a.CacheReadTokens)
	}
}

func TestFinalizeTextResult_NoSchemaAllowsTextOnly(t *testing.T) {
	result, err := finalizeTextResult("codex", "fixed it", nil, TokenUsage{InputTokens: 1, OutputTokens: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "fixed it" {
		t.Errorf("unexpected text: %q", result.Text)
	}
	if result.Output != nil {
		t.Fatalf("expected nil structured output, got %s", string(result.Output))
	}
	if result.Usage.InputTokens != 1 || result.Usage.OutputTokens != 2 {
		t.Errorf("unexpected usage: %+v", result.Usage)
	}
}

func TestFinalizeTextResult_WithSchemaParsesJSON(t *testing.T) {
	result, err := finalizeTextResult("codex", `{"done":true}`, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if output["done"] != true {
		t.Errorf("expected done=true, got %v", output["done"])
	}
}
