package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaudeAgent_BuildArgs(t *testing.T) {
	ca := &claudeAgent{bin: "/usr/bin/claude"}
	schema := json.RawMessage(`{"type":"object"}`)
	args := ca.buildArgs("do something", schema)

	expected := []string{
		"-p", "do something",
		"--verbose",
		"--output-format", "stream-json",
		"--json-schema", `{"type":"object"}`,
		"--dangerously-skip-permissions",
	}

	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, want := range expected {
		if args[i] != want {
			t.Errorf("arg[%d]: expected %q, got %q", i, want, args[i])
		}
	}
}

func TestClaudeAgent_BuildArgs_NoSchema(t *testing.T) {
	ca := &claudeAgent{bin: "claude"}
	args := ca.buildArgs("prompt", nil)

	for _, arg := range args {
		if arg == "--json-schema" {
			t.Error("should not include --json-schema when schema is nil")
		}
	}
	if args[0] != "-p" || args[1] != "prompt" {
		t.Error("missing -p flag")
	}
}

func TestParseClaudeEvents_AssistantMessage(t *testing.T) {
	events := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50},"content":[{"type":"text","text":"hello world"}]}}
`
	var chunks []string
	var usage TokenUsage

	err := parseClaudeEvents(
		context.Background(),
		strings.NewReader(events),
		func(text string) { chunks = append(chunks, text) },
		&usage,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "hello world" {
		t.Errorf("expected chunk 'hello world', got %v", chunks)
	}
	if usage.InputTokens != 100 {
		t.Errorf("expected input tokens 100, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("expected output tokens 50, got %d", usage.OutputTokens)
	}
}

func TestParseClaudeEvents_ResultEvent(t *testing.T) {
	output := map[string]any{"success": true, "summary": "done"}
	outputJSON, _ := json.Marshal(output)
	event := map[string]any{
		"type":              "result",
		"subtype":           "success",
		"structured_output": json.RawMessage(outputJSON),
		"usage": map[string]any{
			"input_tokens":  200,
			"output_tokens": 100,
		},
	}
	line, _ := json.Marshal(event)

	var usage TokenUsage
	var result *claudeResult

	err := parseClaudeEvents(
		context.Background(),
		bytes.NewReader(append(line, '\n')),
		nil,
		&usage,
		&result,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result event")
	}
	if result.Subtype != "success" {
		t.Errorf("expected subtype 'success', got %q", result.Subtype)
	}
	if result.StructuredOutput == nil {
		t.Fatal("expected structured_output")
	}
}

func TestParseClaudeEvents_LargeAssistantEvent(t *testing.T) {
	largeText := strings.Repeat("x", 128*1024)
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 5,
			},
			"content": []map[string]any{{
				"type": "text",
				"text": largeText,
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var chunks []string
	var usage TokenUsage

	err = parseClaudeEvents(
		context.Background(),
		bytes.NewReader(append(line, '\n')),
		func(text string) { chunks = append(chunks, text) },
		&usage,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != largeText {
		t.Fatalf("unexpected chunks: got %d chunks", len(chunks))
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestParseClaudeEvents_SkipsMalformedLines(t *testing.T) {
	events := "not json\n{\"type\":\"assistant\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5},\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n"

	var chunks []string
	var usage TokenUsage

	err := parseClaudeEvents(
		context.Background(),
		strings.NewReader(events),
		func(text string) { chunks = append(chunks, text) },
		&usage,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "ok" {
		t.Errorf("expected 1 chunk 'ok', got %v", chunks)
	}
}

func TestParseClaudeEvents_CacheTokens(t *testing.T) {
	events := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":30,"cache_creation_input_tokens":10},"content":[]}}
`
	var usage TokenUsage
	err := parseClaudeEvents(context.Background(), strings.NewReader(events), nil, &usage, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.CacheReadTokens != 30 {
		t.Errorf("expected cache read tokens 30, got %d", usage.CacheReadTokens)
	}
	if usage.CacheCreationTokens != 10 {
		t.Errorf("expected cache creation tokens 10, got %d", usage.CacheCreationTokens)
	}
}

// When claude streams a single API response across multiple assistant events
// (e.g. thinking + tool_use blocks share one message.id), the per-event
// usage snapshot is the SAME for each event - summing them double-counts.
// We dedupe by message.id.
func TestParseClaudeEvents_DedupesAssistantEventsByMessageID(t *testing.T) {
	events := strings.Join([]string{
		`{"type":"assistant","message":{"id":"msg_A","usage":{"input_tokens":6,"output_tokens":8,"cache_read_input_tokens":16202,"cache_creation_input_tokens":11193},"content":[{"type":"thinking","thinking":""}]}}`,
		`{"type":"assistant","message":{"id":"msg_A","usage":{"input_tokens":6,"output_tokens":8,"cache_read_input_tokens":16202,"cache_creation_input_tokens":11193},"content":[{"type":"tool_use","id":"t","name":"Bash"}]}}`,
		`{"type":"assistant","message":{"id":"msg_B","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":27395,"cache_creation_input_tokens":229},"content":[{"type":"text","text":"done"}]}}`,
	}, "\n") + "\n"

	var usage TokenUsage
	if err := parseClaudeEvents(context.Background(), strings.NewReader(events), nil, &usage, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Without dedup the totals would be (6+6+1)=13 in / (8+8+1)=17 out.
	// With dedup we sum each unique message exactly once.
	if usage.InputTokens != 7 {
		t.Errorf("InputTokens = %d, want 7 (msg_A=6 + msg_B=1)", usage.InputTokens)
	}
	if usage.OutputTokens != 9 {
		t.Errorf("OutputTokens = %d, want 9 (msg_A=8 + msg_B=1)", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 16202+27395 {
		t.Errorf("CacheReadTokens = %d, want %d", usage.CacheReadTokens, 16202+27395)
	}
	if usage.CacheCreationTokens != 11193+229 {
		t.Errorf("CacheCreationTokens = %d, want %d", usage.CacheCreationTokens, 11193+229)
	}
}

// The result event carries the authoritative aggregate from the CLI - prefer
// it over summing per-message snapshots, which can be partial mid-stream.
func TestParseClaudeEvents_ResultUsageOverridesAssistantEvents(t *testing.T) {
	events := strings.Join([]string{
		`{"type":"assistant","message":{"id":"msg_A","usage":{"input_tokens":6,"output_tokens":8,"cache_read_input_tokens":16202,"cache_creation_input_tokens":11193},"content":[{"type":"text","text":"thinking..."}]}}`,
		`{"type":"assistant","message":{"id":"msg_B","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":27395,"cache_creation_input_tokens":229},"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"usage":{"input_tokens":7,"output_tokens":170,"cache_read_input_tokens":43597,"cache_creation_input_tokens":11422}}`,
	}, "\n") + "\n"

	var usage TokenUsage
	var result *claudeResult
	if err := parseClaudeEvents(context.Background(), strings.NewReader(events), nil, &usage, &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result event")
	}

	// The final output_tokens=170 from the result event must win over
	// the streamed snapshots (which only saw 8+1=9).
	if usage.InputTokens != 7 {
		t.Errorf("InputTokens = %d, want 7", usage.InputTokens)
	}
	if usage.OutputTokens != 170 {
		t.Errorf("OutputTokens = %d, want 170 (from result event)", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 43597 {
		t.Errorf("CacheReadTokens = %d, want 43597", usage.CacheReadTokens)
	}
	if usage.CacheCreationTokens != 11422 {
		t.Errorf("CacheCreationTokens = %d, want 11422", usage.CacheCreationTokens)
	}
}

func TestParseClaudeEvents_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := `{"type":"assistant","message":{"usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"ok"}]}}
`
	var usage TokenUsage
	err := parseClaudeEvents(ctx, strings.NewReader(events), nil, &usage, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestFinalizeClaudeResult_RequiresStructuredOutputWhenSchemaPresent(t *testing.T) {
	_, err := finalizeClaudeResult(&claudeResult{Subtype: "success"}, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if !errors.Is(err, errNoStructuredOutput) {
		t.Fatalf("expected errNoStructuredOutput, got %v", err)
	}
}

func TestTailBuffer_KeepsLastCapBytesAcrossMultipleWrites(t *testing.T) {
	tb := newTailBuffer(8)
	if _, err := tb.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := tb.Write([]byte("efgh")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := tb.Write([]byte("ijkl")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := string(tb.Bytes()); got != "efghijkl" {
		t.Fatalf("Bytes() = %q, want %q", got, "efghijkl")
	}
}

func TestTailBuffer_LargeSingleWriteKeepsTail(t *testing.T) {
	tb := newTailBuffer(4)
	if _, err := tb.Write([]byte("abcdefghij")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := string(tb.Bytes()); got != "ghij" {
		t.Fatalf("Bytes() = %q, want %q", got, "ghij")
	}
}

func TestClaudeAgent_RunOnceErrorIncludesStderrAndStdoutTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake bin assumes POSIX shell")
	}

	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-claude.sh")
	script := `#!/bin/sh
echo '{"type":"assistant","message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"thinking out loud"}]}}'
echo 'auth token expired' 1>&2
exit 1
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}

	a := &claudeAgent{bin: binPath}
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "hi", CWD: dir})
	if err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "auth token expired") {
		t.Errorf("error missing stderr content: %s", msg)
	}
	if !strings.Contains(msg, "thinking out loud") {
		t.Errorf("error missing captured stdout trajectory: %s", msg)
	}
}
