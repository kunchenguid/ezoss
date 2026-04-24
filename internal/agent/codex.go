package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

type codexAgent struct {
	bin string
}

func (a *codexAgent) Name() string { return "codex" }

func (a *codexAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	args := a.buildArgs(opts.Prompt)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = opts.CWD
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}

	var stderrBuf []byte
	var stderrWG sync.WaitGroup
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex start: %w", err)
	}

	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderrBuf, _ = io.ReadAll(stderrR)
	}()

	var usage TokenUsage
	var lastMessage string
	if err := parseCodexEvents(ctx, stdout, opts.OnChunk, &usage, &lastMessage); err != nil {
		stderrWG.Wait()
		_ = cmd.Wait()
		return nil, fmt.Errorf("codex parse events: %w", err)
	}

	stderrWG.Wait()
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("codex exited: %w: %s", err, string(stderrBuf))
	}

	return finalizeTextResult("codex", lastMessage, opts.JSONSchema, usage)
}

func (a *codexAgent) Close() error { return nil }

func (a *codexAgent) buildArgs(prompt string) []string {
	return []string{"exec", prompt, "--json", "--dangerously-bypass-approvals-and-sandbox", "--color", "never"}
}

type codexEvent struct {
	Type  string      `json:"type"`
	Item  *codexItem  `json:"item,omitempty"`
	Usage *codexUsage `json:"usage,omitempty"`
}

type codexItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

func parseCodexEvents(ctx context.Context, r io.Reader, onChunk func(string), usage *TokenUsage, lastMessage *string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024*1024)

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

		var event codexEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		switch event.Type {
		case "item.completed":
			if event.Item != nil && event.Item.Type == "agent_message" {
				*lastMessage = event.Item.Text
				if onChunk != nil {
					onChunk(event.Item.Text)
				}
			}

		case "turn.completed":
			if event.Usage != nil {
				usage.Add(TokenUsage{
					InputTokens:     event.Usage.InputTokens,
					OutputTokens:    event.Usage.OutputTokens,
					CacheReadTokens: event.Usage.CachedInputTokens,
				})
			}
		}
	}

	return scanner.Err()
}
