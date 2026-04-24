package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

const claudeMaxRetries = 3

var errNoStructuredOutput = errors.New("claude returned no structured output")

const claudeScannerMaxTokenSize = 256 * 1024 * 1024

const claudeStdoutTailCap = 64 * 1024

type tailBuffer struct {
	cap int
	buf []byte
}

func newTailBuffer(cap int) *tailBuffer { return &tailBuffer{cap: cap} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if t.cap <= 0 {
		return n, nil
	}
	if n >= t.cap {
		t.buf = append(t.buf[:0], p[n-t.cap:]...)
		return n, nil
	}
	if len(t.buf)+n > t.cap {
		drop := len(t.buf) + n - t.cap
		t.buf = t.buf[drop:]
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) Bytes() []byte { return t.buf }

type claudeAgent struct {
	bin string
}

func (a *claudeAgent) Name() string { return "claude" }

func (a *claudeAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	var lastErr error
	for attempt := 1; attempt <= claudeMaxRetries; attempt++ {
		if attempt > 1 && opts.OnChunk != nil {
			opts.OnChunk(fmt.Sprintf("retrying (attempt %d/%d) - previous attempt returned no structured output", attempt, claudeMaxRetries))
		}

		result, err := a.runOnce(ctx, opts)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, errNoStructuredOutput) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (a *claudeAgent) runOnce(ctx context.Context, opts RunOpts) (*Result, error) {
	args := a.buildArgs(opts.Prompt, opts.JSONSchema)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = opts.CWD
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}

	var stderrBuf []byte
	var stderrWG sync.WaitGroup
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude start: %w", err)
	}

	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderrBuf, _ = io.ReadAll(stderrR)
	}()

	stdoutTail := newTailBuffer(claudeStdoutTailCap)
	teedStdout := io.TeeReader(stdout, stdoutTail)

	var usage TokenUsage
	var result *claudeResult
	if err := parseClaudeEvents(ctx, teedStdout, opts.OnChunk, &usage, &result); err != nil {
		stderrWG.Wait()
		_ = cmd.Wait()
		return nil, fmt.Errorf("claude parse events: %w: stderr=%s stdout_tail=%s", err, string(stderrBuf), string(stdoutTail.Bytes()))
	}

	stderrWG.Wait()
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("claude exited: %w: stderr=%s stdout_tail=%s", err, string(stderrBuf), string(stdoutTail.Bytes()))
	}

	if result == nil {
		return nil, fmt.Errorf("claude returned no result event")
	}

	res, err := finalizeClaudeResult(result, opts.JSONSchema, usage)
	if errors.Is(err, errNoStructuredOutput) && opts.OnChunk != nil {
		opts.OnChunk(fmt.Sprintf("structured output missing: subtype=%s, text_len=%d, input_tokens=%d, output_tokens=%d", result.Subtype, len(result.text), usage.InputTokens, usage.OutputTokens))
		opts.OnChunk(fmt.Sprintf("raw result event: %s", string(result.rawEvent)))
	}
	return res, err
}

func (a *claudeAgent) Close() error { return nil }

func finalizeClaudeResult(result *claudeResult, schema json.RawMessage, usage TokenUsage) (*Result, error) {
	if result.IsError || result.Subtype != "success" {
		return nil, fmt.Errorf("claude error: subtype=%s", result.Subtype)
	}
	if len(schema) > 0 && result.StructuredOutput == nil {
		return nil, errNoStructuredOutput
	}

	return &Result{Output: result.StructuredOutput, Text: result.text, Usage: usage}, nil
}

func (a *claudeAgent) buildArgs(prompt string, schema json.RawMessage) []string {
	args := []string{"-p", prompt, "--verbose", "--output-format", "stream-json"}
	if len(schema) > 0 {
		args = append(args, "--json-schema", string(schema))
	}
	args = append(args, "--dangerously-skip-permissions")
	return args
}

type claudeEvent struct {
	Type             string          `json:"type"`
	Message          json.RawMessage `json:"message,omitempty"`
	Subtype          string          `json:"subtype,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            *claudeUsage    `json:"usage,omitempty"`
}

type claudeResult struct {
	Subtype          string
	IsError          bool
	StructuredOutput json.RawMessage
	text             string
	rawEvent         json.RawMessage
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Usage   claudeUsage     `json:"usage"`
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseClaudeEvents reads claude's stream-json output and accumulates token
// usage with two safeguards: (1) per-message dedup by message.id, because a
// single API response is split across multiple "assistant" events (one per
// content block) that all carry the same usage snapshot; (2) the trailing
// "result" event's usage, when present, replaces the streamed sum because it
// is the authoritative aggregate emitted after streaming completes.
func parseClaudeEvents(ctx context.Context, r io.Reader, onChunk func(string), usage *TokenUsage, result **claudeResult) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), claudeScannerMaxTokenSize)
	var textBuf string
	usageByMsg := make(map[string]TokenUsage)
	var noIDUsage TokenUsage
	var resultUsage *TokenUsage

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event claudeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "assistant":
			var msg claudeMessage
			if err := json.Unmarshal(event.Message, &msg); err != nil {
				continue
			}
			eventUsage := TokenUsage{
				InputTokens:         msg.Usage.InputTokens,
				OutputTokens:        msg.Usage.OutputTokens,
				CacheReadTokens:     msg.Usage.CacheReadInputTokens,
				CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
			}
			if msg.ID != "" {
				usageByMsg[msg.ID] = eventUsage
			} else {
				noIDUsage.Add(eventUsage)
			}
			for _, c := range msg.Content {
				if c.Type == "text" && c.Text != "" {
					textBuf += c.Text
					if onChunk != nil {
						onChunk(c.Text)
					}
				}
			}

		case "result":
			if event.Usage != nil {
				resultUsage = &TokenUsage{
					InputTokens:         event.Usage.InputTokens,
					OutputTokens:        event.Usage.OutputTokens,
					CacheReadTokens:     event.Usage.CacheReadInputTokens,
					CacheCreationTokens: event.Usage.CacheCreationInputTokens,
				}
			}
			if result != nil {
				raw := make(json.RawMessage, len(line))
				copy(raw, line)
				*result = &claudeResult{
					Subtype:          event.Subtype,
					IsError:          event.IsError,
					StructuredOutput: event.StructuredOutput,
					text:             textBuf,
					rawEvent:         raw,
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if resultUsage != nil {
		*usage = *resultUsage
	} else {
		*usage = noIDUsage
		for _, u := range usageByMsg {
			usage.Add(u)
		}
	}
	return nil
}
