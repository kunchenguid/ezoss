package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"

	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

type Agent interface {
	Name() string
	Run(ctx context.Context, opts RunOpts) (*Result, error)
	Close() error
}

type RunOpts struct {
	Prompt     string
	CWD        string
	JSONSchema json.RawMessage
	OnChunk    func(text string)
}

type Result struct {
	Output json.RawMessage
	Text   string
	Usage  TokenUsage
}

type TokenUsage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

var defaultBinary = map[sharedtypes.AgentName]string{
	sharedtypes.AgentClaude:   "claude",
	sharedtypes.AgentCodex:    "codex",
	sharedtypes.AgentRovoDev:  "acli",
	sharedtypes.AgentOpenCode: "opencode",
}

var autoProbeOrder = []sharedtypes.AgentName{
	sharedtypes.AgentClaude,
	sharedtypes.AgentCodex,
	sharedtypes.AgentOpenCode,
	sharedtypes.AgentRovoDev,
}

func finalizeTextResult(agentName, text string, schema json.RawMessage, usage TokenUsage) (*Result, error) {
	if text == "" {
		return nil, fmt.Errorf("%s returned no text output", agentName)
	}
	if len(schema) == 0 {
		return &Result{Text: text, Usage: usage}, nil
	}

	var output json.RawMessage
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		return nil, fmt.Errorf("%s output parse: %w", agentName, err)
	}

	return &Result{Output: output, Text: text, Usage: usage}, nil
}

func (u TokenUsage) Total() int {
	return u.InputTokens + u.OutputTokens
}

// TotalInputTokens returns the full prompt size including cached and
// cache-creation tokens. For Claude this is the meaningful "input" number
// for users (matches /usage); plain InputTokens is just the uncached portion.
func (u TokenUsage) TotalInputTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

func (u *TokenUsage) Add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheCreationTokens += other.CacheCreationTokens
}

func Binary(name sharedtypes.AgentName) string {
	if bin, ok := defaultBinary[name]; ok {
		return bin
	}
	return string(name)
}

func Resolve(name sharedtypes.AgentName, lookPath func(string) (string, error)) (sharedtypes.AgentName, string, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if name != sharedtypes.AgentAuto {
		bin, err := lookPath(Binary(name))
		if err != nil {
			return "", "", err
		}
		return name, bin, nil
	}

	probed := make([]string, 0, len(autoProbeOrder))
	for _, candidate := range autoProbeOrder {
		binName := Binary(candidate)
		probed = append(probed, binName)
		bin, err := lookPath(binName)
		if err == nil {
			return candidate, bin, nil
		}
		if errorsIsNotFound(err) {
			continue
		}
		return "", "", fmt.Errorf("resolve %s agent from %q: %w", candidate, binName, err)
	}

	return "", "", fmt.Errorf("no supported agent found in PATH (looked for: %s); install one or set 'agent' in ~/.ezoss/config.yaml", strings.Join(probed, ", "))
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

func New(name sharedtypes.AgentName, bin string) (Agent, error) {
	switch name {
	case sharedtypes.AgentClaude:
		return &claudeAgent{bin: bin}, nil
	case sharedtypes.AgentCodex:
		return &codexAgent{bin: bin}, nil
	case sharedtypes.AgentRovoDev:
		return &rovodevAgent{bin: bin}, nil
	case sharedtypes.AgentOpenCode:
		return &opencodeAgent{bin: bin}, nil
	default:
		return nil, fmt.Errorf("unknown agent %q; valid options: auto, claude, codex, rovodev, opencode (set 'agent' in ~/.ezoss/config.yaml)", name)
	}
}

func NewNoop() Agent { return &noopAgent{} }

type noopAgent struct{}

func (n *noopAgent) Name() string                                      { return "noop" }
func (n *noopAgent) Run(_ context.Context, _ RunOpts) (*Result, error) { return &Result{}, nil }
func (n *noopAgent) Close() error                                      { return nil }
