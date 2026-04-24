package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

// TestVisualCheck is a visual sanity check, not a hard assertion test.
// Run with: go test ./internal/tui/ -run TestVisualCheck -v
// It only fails if the rendered height exceeds the configured terminal
// height (which would indicate the layout is still broken).
func TestVisualCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("visual check")
	}
	entries := visualEntries()

	cases := []struct {
		name string
		w, h int
	}{
		{"narrow stacked 80x24", 80, 24},
		{"medium stacked 100x30", 100, 30},
		{"wide responsive 140x35", 140, 35},
		{"wide responsive 200x60", 200, 60},
		{"compact short 90x18", 90, 18},
	}
	for _, c := range cases {
		m := NewModel(entries)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
		mm := updated.(Model)
		view := mm.View()
		t.Logf("\n====== %s ======\n%s\n[rendered_height=%d, terminal=%d]", c.name, view, lipgloss.Height(view), c.h)
		if h := lipgloss.Height(view); h > c.h {
			t.Errorf("%s: rendered height %d > terminal height %d", c.name, h, c.h)
		}
	}
}

func visualEntries() []Entry {
	type fixture struct {
		repo  string
		num   int
		kind  sharedtypes.ItemKind
		title string
		state sharedtypes.StateChange
	}
	fixtures := []fixture{
		{"kunchenguid/axi", 28, sharedtypes.ItemKindPR, "feat(bench): publish actionbook browser benchmark baseline", sharedtypes.StateChangeNone},
		{"kunchenguid/axi", 29, sharedtypes.ItemKindPR, "Add §11: Usage-driven improvement via OODA loop", sharedtypes.StateChangeNone},
		{"kunchenguid/axi", 31, sharedtypes.ItemKindIssue, "Feedback wanted: building a Google Workspace AXI CLI", sharedtypes.StateChangeNone},
		{"kunchenguid/axi", 32, sharedtypes.ItemKindIssue, "Thoughts on JSON output?", sharedtypes.StateChangeNone},
		{"kunchenguid/chrome-devtools-axi", 32, sharedtypes.ItemKindIssue, "feature request: CDP URL", sharedtypes.StateChangeClose},
		{"kunchenguid/chrome-devtools-axi", 41, sharedtypes.ItemKindIssue, "eval: IIFE wrapping fails with 'fn is not a function'", sharedtypes.StateChangeNone},
		{"kunchenguid/chrome-devtools-axi", 42, sharedtypes.ItemKindIssue, "eval: first invocation after a click is unexpectedly slow (~7s)", sharedtypes.StateChangeNone},
		{"kunchenguid/chrome-devtools-axi", 43, sharedtypes.ItemKindIssue, "Bridge gets stuck after attached target restarts; 'start' lies about readiness", sharedtypes.StateChangeNone},
		{"kunchenguid/chrome-devtools-axi", 44, sharedtypes.ItemKindIssue, "console --type: 'all' rejected; valid values not enumerated in help", sharedtypes.StateChangeNone},
		{"kunchenguid/chrome-devtools-axi", 45, sharedtypes.ItemKindIssue, "snapshot uids age out silently across re-renders; click/fill no-op without error", sharedtypes.StateChangeNone},
		{"kunchenguid/chrome-devtools-axi", 46, sharedtypes.ItemKindIssue, "Feature: live event subscription (console / DOM mutation / IPC) for streaming UX validation", sharedtypes.StateChangeNone},
		{"kunchenguid/gnhf", 76, sharedtypes.ItemKindPR, "feat(worktree): resume into a preserved worktree on re-invocation", sharedtypes.StateChangeNone},
		{"kunchenguid/gnhf", 79, sharedtypes.ItemKindIssue, "Add GitHub Copilot CLI as supported agent", sharedtypes.StateChangeNone},
		{"kunchenguid/gnhf", 83, sharedtypes.ItemKindIssue, "opencode: --model in agentArgsOverride fails (not passed via HTTP API)", sharedtypes.StateChangeNone},
	}
	out := make([]Entry, 0, len(fixtures))
	for i, f := range fixtures {
		out = append(out, Entry{
			RecommendationID: fmt.Sprintf("rec-%d", i),
			RepoID:           f.repo,
			Number:           f.num,
			Kind:             f.kind,
			Title:            f.title,
			StateChange:      f.state,
			Confidence:       sharedtypes.ConfidenceHigh,
			WaitingOn:        sharedtypes.WaitingOnContributor,
			Rationale:        "PR was opened on the deprecated repo. Maintainer already asked contributor to move it.\nNo response and no equivalent PR exists in the new repo.",
			DraftComment:     "hey friendly ping on this. happy to carry the change over with a Co-authored-by trailer.",
			AgeLabel:         fmt.Sprintf("%dm", 28+i),
			URL:              fmt.Sprintf("https://github.com/%s/issues/%d", f.repo, f.num),
			TokensIn:         21,
			TokensOut:        558,
			ProposedLabels:   []string{"needs-discussion"},
		})
	}
	return out
}
