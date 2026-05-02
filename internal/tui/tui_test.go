package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// runActionCmd extracts an actionFinishedMsg from the cmd returned by an
// async key press (a/f/m/r). The cmd is either the bare action func or a
// tea.Batch whose first child is the action - by convention startAction
// places it first so tests can bypass the sibling spinner-tick cmd
// (tea.Tick blocks for real wall-clock time).
func runActionCmd(t *testing.T, cmd tea.Cmd) actionFinishedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	msg := cmd()
	switch v := msg.(type) {
	case actionFinishedMsg:
		return v
	case tea.BatchMsg:
		if len(v) == 0 || v[0] == nil {
			t.Fatal("empty batch")
		}
		inner := v[0]()
		if afm, ok := inner.(actionFinishedMsg); ok {
			return afm
		}
		t.Fatalf("first batch child returned %T, want actionFinishedMsg", inner)
	default:
		t.Fatalf("got %T, want actionFinishedMsg or BatchMsg", msg)
	}
	return actionFinishedMsg{}
}

func TestModelViewShowsInboxCountsAndDetails(t *testing.T) {
	m := NewModel([]Entry{
		{
			RepoID:         "acme/widgets",
			Number:         42,
			Kind:           sharedtypes.ItemKindIssue,
			Author:         "alice",
			Title:          "Bug: triage queue stalls",
			StateChange:    sharedtypes.StateChangeNone,
			ProposedLabels: []string{"bug", "needs-repro"},
			Confidence:     sharedtypes.ConfidenceMedium,
			Rationale:      "The report is missing a repro and needs one more round-trip.",
			DraftComment:   "Thanks for the report. Can you share a repro?",
			Followups:      []string{"Check whether this regressed after the queue rewrite."},
			TokensIn:       12400,
			TokensOut:      1100,
			AgeLabel:       "2h",
			WaitingOn:      sharedtypes.WaitingOnContributor,
		},
		{
			RepoID:      "acme/widgets",
			Number:      7,
			Kind:        sharedtypes.ItemKindPR,
			Title:       "feat: ship it",
			StateChange: sharedtypes.StateChangeRequestChanges,
			Confidence:  sharedtypes.ConfidenceHigh,
			AgeLabel:    "5h",
		},
	})
	m.width = 100

	view := stripANSI(m.View())
	for _, want := range []string{
		// Card title: repo · <kind-glyph> #N · age (no queue counter on
		// single-option recommendations - the rail's cursor shows position).
		"acme/widgets · ○ #42",
		// Card body
		"Bug: triage queue stalls",
		"by @alice",
		"The report is missing a repro",
		"Thanks for the report. Can you share a repro?",
		"confidence: medium",
		"currently waiting on @alice (contributor)",
		"Follow-ups:",
		"- Check whether this regressed after the queue rewrite.",
		// Action summary describes what `a approve` will do.
		"Will:",
		"comment",
		"labels: bug, needs-repro",
		// Decide bar + nav bar
		"a approve",
		"q quit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "recommendation: 2h old") {
		t.Fatalf("View() should not show recommendation age in card metadata:\n%s", view)
	}
	if strings.Contains(view, "12.4k in / 1.1k out") {
		t.Fatalf("View() should not show token usage in card metadata:\n%s", view)
	}
}

func TestModelViewShowsSwitchOptionHintWhenMultipleOptions(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID: "acme/widgets",
		Number: 42,
		Kind:   sharedtypes.ItemKindIssue,
		Title:  "Bug: triage queue stalls",
		Options: []EntryOption{
			{
				ID:           "opt-1",
				StateChange:  sharedtypes.StateChangeClose,
				Confidence:   sharedtypes.ConfidenceHigh,
				Rationale:    "Stale - close after long inactivity.",
				DraftComment: "Closing as stale.",
				WaitingOn:    sharedtypes.WaitingOnContributor,
			},
			{
				ID:           "opt-2",
				StateChange:  sharedtypes.StateChangeNone,
				Confidence:   sharedtypes.ConfidenceMedium,
				Rationale:    "One more nudge before closing.",
				DraftComment: "Friendly ping - any update?",
				WaitingOn:    sharedtypes.WaitingOnContributor,
			},
		},
	}})
	m.entries[0].SyncActive()
	m.width = 120

	view := stripANSI(m.View())
	if !strings.Contains(view, "tab switch option") {
		t.Fatalf("View() with multiple options missing 'tab switch option' hint in:\n%s", view)
	}
}

func TestModelViewShowsCompactActionHintsWithMoreFallback(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "Bug: triage queue stalls",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceMedium,
		Rationale:    "single option only",
		DraftComment: "draft",
	}})
	m.width = 100
	m.height = 24

	view := stripANSI(m.View())
	for _, want := range []string{"a approve", "e edit", "m mark triaged", "? more"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing compact action hint %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "edit draft") {
		t.Fatalf("View() should say 'edit', not 'edit draft':\n%s", view)
	}
}

func TestModelViewPrioritizesScrollHintWhenCardOverflows(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "Bug: triage queue stalls",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceMedium,
		Rationale:    strings.Repeat("This is enough rationale to overflow the recommendation card. ", 24),
		DraftComment: "draft",
	}})
	m.width = 92
	m.height = 12

	view := stripANSI(m.View())
	if !strings.Contains(view, "↓") || !strings.Contains(view, "more") {
		t.Fatalf("View() should keep the scroll hint visible when the card overflows:\n%s", view)
	}
	if !strings.Contains(view, "a approve") || !strings.Contains(view, "? more") {
		t.Fatalf("View() should still keep compact action discovery visible:\n%s", view)
	}
}

func TestModelViewKeepsCurrentWaitingOnWhenSwitchingOptions(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "Bug: triage queue stalls",
		CurrentWaitingOn: sharedtypes.WaitingOnMaintainer,
		Options: []EntryOption{
			{ID: "opt-1", StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceHigh, WaitingOn: sharedtypes.WaitingOnContributor},
			{ID: "opt-2", StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium, WaitingOn: sharedtypes.WaitingOnCI},
		},
	}})
	m.entries[0].SyncActive()
	m.width = 100

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	view := stripANSI(updated.(Model).View())
	if !strings.Contains(view, "currently waiting on maintainer") {
		t.Fatalf("View() should keep current waiting_on after switching options, got:\n%s", view)
	}
	if strings.Contains(view, "currently waiting on ci") {
		t.Fatalf("View() should not render option waiting_on as current state, got:\n%s", view)
	}
}

func TestModelViewOmitsSwitchOptionHintForSingleOption(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "Bug: triage queue stalls",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceMedium,
		Rationale:    "single option only",
		DraftComment: "draft",
	}})
	m.width = 100

	view := stripANSI(m.View())
	if strings.Contains(view, "tab switch option") {
		t.Fatalf("View() with one option should not show 'tab switch option' hint, got:\n%s", view)
	}
}

func TestModelViewShowsGitHubURLInDetails(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "Bug: triage queue stalls",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceMedium,
		Rationale:    "Need a minimal repro.",
		DraftComment: "Can you share a minimal repro?",
		URL:          "https://github.com/acme/widgets/issues/42",
	}})
	m.width = 100

	details := m.renderDetails()
	if !strings.Contains(details, "https://github.com/acme/widgets/issues/42") {
		t.Fatalf("renderDetails() missing URL in:\n%s", details)
	}
}

func TestModelViewIndentsMultilineDraftResponse(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "Bug: triage queue stalls",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceMedium,
		DraftComment: "Thanks for the report.\nPlease share a minimal repro.\nAlso include the daemon log.",
	}})
	m.width = 100

	details := m.renderDetails()
	for _, want := range []string{
		"Draft response:",
		"  Thanks for the report.",
		"  Please share a minimal repro.",
		"  Also include the daemon log.",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("renderDetails() missing %q in:\n%s", want, details)
		}
	}
}

func TestModelViewIndentsMultilineRationale(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:      "acme/widgets",
		Number:      42,
		Kind:        sharedtypes.ItemKindIssue,
		Title:       "Bug: triage queue stalls",
		StateChange: sharedtypes.StateChangeNone,
		Confidence:  sharedtypes.ConfidenceMedium,
		Rationale:   "The queue likely deadlocked after a lock retry.\nThe error path still needs a minimal repro.",
	}})
	m.width = 100

	details := m.renderDetails()
	for _, want := range []string{
		"Rationale:",
		"  The queue likely deadlocked after a lock retry.",
		"  The error path still needs a minimal repro.",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("renderDetails() missing %q in:\n%s", want, details)
		}
	}
}

func TestModelViewFormatsSingleLineFixPromptIntoReadableSections(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:      "acme/widgets",
		Number:      42,
		Kind:        sharedtypes.ItemKindIssue,
		Title:       "Bug: triage queue stalls",
		StateChange: sharedtypes.StateChangeNone,
		Confidence:  sharedtypes.ConfidenceMedium,
		FixPrompt:   "GitHub issue: https://github.com/acme/widgets/issues/42 Problem The test step pauses at awaiting approval for informational findings. Reproduction / evidence Two offending blocks: 1. internal/pipeline/steps/test.go:122-146. Acceptance criteria Do not require approval for non-actionable findings. Verification steps go test ./internal/pipeline/...",
	}})
	m.width = 100

	details := stripANSI(m.renderDetails())
	for _, want := range []string{
		"Fix prompt:",
		"  GitHub issue: https://github.com/acme/widgets/issues/42",
		"  Problem",
		"  Reproduction / evidence",
		"  Acceptance criteria",
		"  Verification steps",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("renderDetails() missing formatted fix prompt section %q in:\n%s", want, details)
		}
	}
}

func TestModelViewShowsConciseFixJobStatus(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:      "acme/widgets",
		Number:      42,
		Kind:        sharedtypes.ItemKindIssue,
		Title:       "Bug: triage queue stalls",
		FixJobID:    "fix-1",
		FixStatus:   "running",
		FixPhase:    "running_agent",
		FixMessage:  "running agent",
		Rationale:   "Need a fix.",
		Confidence:  sharedtypes.ConfidenceMedium,
		StateChange: sharedtypes.StateChangeFixRequired,
	}})
	m.width = 100

	details := stripANSI(m.renderDetails())
	if !strings.Contains(details, "Fix: running agent") {
		t.Fatalf("renderDetails() missing concise fix status in:\n%s", details)
	}
	if strings.Contains(details, "running · running_agent") || strings.Contains(details, "running_agent") {
		t.Fatalf("renderDetails() should not show redundant raw fix status/phase in:\n%s", details)
	}
}

func TestModelViewShowsNoMistakesAttachCommandWhenWaitingForPR(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:          "acme/widgets",
		Number:          42,
		Kind:            sharedtypes.ItemKindIssue,
		Title:           "Bug: triage queue stalls",
		FixJobID:        "fix-1",
		FixStatus:       "running",
		FixPhase:        "waiting_for_pr",
		FixMessage:      "waiting for PR",
		FixWorktreePath: "/tmp/ezoss fix/widgets/42-run",
		Rationale:       "Need a fix.",
		Confidence:      sharedtypes.ConfidenceMedium,
		StateChange:     sharedtypes.StateChangeFixRequired,
	}})
	m.width = 120

	details := stripANSI(m.renderDetails())
	want := "attach: cd '/tmp/ezoss fix/widgets/42-run' && no-mistakes attach"
	if !strings.Contains(details, want) {
		t.Fatalf("renderDetails() missing no-mistakes attach command %q in:\n%s", want, details)
	}
}

func TestModelHelpExplainsSkipMarksTriaged(t *testing.T) {
	m := NewModel(nil)
	help := stripANSI(m.renderHelp())
	if !strings.Contains(help, "m                  mark triaged without approving") {
		t.Fatalf("renderHelp() should explain mark-triaged semantics, got:\n%s", help)
	}
}

func TestModelViewDoesNotStartDetailsPaneWithBlankLineWhenNoApprovalError(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "Bug: triage queue stalls",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceMedium,
		Rationale:    "Need a minimal repro.",
		DraftComment: "Can you share a minimal repro?",
	}})
	m.width = 100

	details := m.renderDetails()
	if strings.HasPrefix(details, "\n") {
		t.Fatalf("renderDetails() starts with a blank line: %q", details)
	}
	stripped := stripANSI(details)
	// New layout: status strip on first line, then blank, then rationale.
	if !strings.HasPrefix(stripped, "confidence: medium\n\nRationale:\n  Need a minimal repro.") {
		t.Fatalf("renderDetails() should start with status strip then rationale, got %q", stripped)
	}
}

func TestModelNavigationMovesSelection(t *testing.T) {
	m := NewModel([]Entry{
		{RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", StateChange: sharedtypes.StateChangeNone},
		{RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindPR, Title: "two", StateChange: sharedtypes.StateChangeMerge},
	})
	m.width = 100

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	next := updated.(Model)
	if next.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", next.cursor)
	}

	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	prev := updated.(Model)
	if prev.cursor != 0 {
		t.Fatalf("cursor after k = %d, want 0", prev.cursor)
	}
}

func TestModelNavigationIgnoresArrowNPKeysForInboxCursor(t *testing.T) {
	m := NewModel([]Entry{
		{RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", StateChange: sharedtypes.StateChangeNone},
		{RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindPR, Title: "two", StateChange: sharedtypes.StateChangeMerge},
	})
	m.width = 100
	m.height = 30

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyDown},
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
	} {
		updated, _ := m.Update(key)
		if got := updated.(Model).cursor; got != 0 {
			t.Fatalf("cursor after %q = %d, want 0", key.String(), got)
		}
	}

	m.cursor = 1
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyUp},
		{Type: tea.KeyRunes, Runes: []rune{'p'}},
	} {
		updated, _ := m.Update(key)
		if got := updated.(Model).cursor; got != 1 {
			t.Fatalf("cursor after %q = %d, want 1", key.String(), got)
		}
	}
}

// TestApproveAdvancesCursorToNextEntry pins the behavior where pressing
// 'a' on a recommendation that's about to be approved (and thus removed
// from the inbox once the async action lands) immediately advances the
// selection to the next item, so the maintainer can see what's next
// while the original entry is still pending.
func TestApproveAdvancesCursorToNextEntry(t *testing.T) {
	m := NewModelWithActions([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two"},
	}, ModelActions{
		Approve: func([]Entry) error { return nil },
	})
	m.width = 100

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	next := updated.(Model)
	if next.cursor != 1 {
		t.Fatalf("cursor after approve should advance to next entry; got %d, want 1", next.cursor)
	}
	if len(next.entries) != 2 {
		t.Fatalf("entry should remain in list until actionFinishedMsg, got %d entries", len(next.entries))
	}
}

// TestMarkTriagedAdvancesCursorToNextEntry mirrors the approve advance
// behavior for 'm'.
func TestMarkTriagedAdvancesCursorToNextEntry(t *testing.T) {
	m := NewModelWithDismiss([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two"},
	}, func([]Entry) error { return nil })
	m.width = 100

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	next := updated.(Model)
	if next.cursor != 1 {
		t.Fatalf("cursor after mark triaged should advance; got %d, want 1", next.cursor)
	}
}

// TestApproveOnLastEntryKeepsCursor verifies that the advance is a no-op
// when there is no next entry to move to.
func TestApproveOnLastEntryKeepsCursor(t *testing.T) {
	m := NewModelWithActions([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two"},
	}, ModelActions{
		Approve: func([]Entry) error { return nil },
	})
	m.width = 100
	m.cursor = 1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	next := updated.(Model)
	if next.cursor != 1 {
		t.Fatalf("cursor on last entry should stay; got %d, want 1", next.cursor)
	}
}

// TestSelectionStableWhenEarlierActionResolves verifies that once the
// cursor has been advanced past a pending action, the cursor stays on
// the new selection when the earlier async action finally resolves and
// removes its entry - the cursor does not visually jump just because an
// item before it disappeared.
func TestSelectionStableWhenEarlierActionResolves(t *testing.T) {
	m := NewModelWithActions([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two"},
		{RecommendationID: "rec-3", RepoID: "acme/widgets", Number: 3, Kind: sharedtypes.ItemKindIssue, Title: "three"},
	}, ModelActions{
		Approve: func([]Entry) error { return nil },
	})
	m.width = 100

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated, _ = updated.(Model).Update(actionFinishedMsg{
		verb:             "approve",
		recommendationID: "rec-1",
	})
	next := updated.(Model)
	if next.cursor < 0 || next.cursor >= len(next.entries) {
		t.Fatalf("cursor out of range: %d (entries=%d)", next.cursor, len(next.entries))
	}
	if got := next.entries[next.cursor].RecommendationID; got != "rec-2" {
		t.Fatalf("cursor should remain on rec-2 after rec-1 was removed; got %q", got)
	}
}

func TestModelMarkTriagedDismissesCurrentEntry(t *testing.T) {
	var dismissed []string
	m := NewModelWithDismiss([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", StateChange: sharedtypes.StateChangeNone},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindPR, Title: "two", StateChange: sharedtypes.StateChangeMerge},
	}, func(entries []Entry) error {
		for _, entry := range entries {
			dismissed = append(dismissed, entry.RecommendationID)
		}
		return nil
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	finishMsg := runActionCmd(t, cmd)
	if len(dismissed) != 1 || dismissed[0] != "rec-1" {
		t.Fatalf("dismissed = %#v, want [rec-1]", dismissed)
	}
	updated, _ = updated.(Model).Update(finishMsg)
	next := updated.(Model)
	if len(next.entries) != 1 || next.entries[0].RecommendationID != "rec-2" {
		t.Fatalf("entries after mark triaged = %#v, want [rec-2]", next.entries)
	}
	view := stripANSI(next.View())
	// The card should now focus on the surviving entry.
	if !strings.Contains(view, "acme/widgets · ⇡ #2") {
		t.Fatalf("View() should show surviving entry as cursor:\n%s", view)
	}
}

func TestActionFinishedRemovesEntryFromAllEntries(t *testing.T) {
	m := NewModel([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", Role: sharedtypes.RoleContributor},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two", Role: sharedtypes.RoleMaintainer},
	})
	m.width = 100
	m.roleFilter = RoleFilterContributor
	m.entries = applyRoleFilter(m.allEntries, m.roleFilter)

	m.applyActionFinished(actionFinishedMsg{verb: "approve", recommendationID: "rec-1"})
	m.cycleRoleFilter()

	for _, entry := range m.entries {
		if entry.RecommendationID == "rec-1" {
			t.Fatalf("removed recommendation reappeared after cycling role filter: %#v", m.entries)
		}
	}
}

func TestActionFinishedRemovesHiddenEntryFromAllEntries(t *testing.T) {
	m := NewModel([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", Role: sharedtypes.RoleContributor},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two", Role: sharedtypes.RoleMaintainer},
	})
	m.width = 100
	m.roleFilter = RoleFilterMaintainer
	m.entries = applyRoleFilter(m.allEntries, m.roleFilter)

	m.applyActionFinished(actionFinishedMsg{verb: "approve", recommendationID: "rec-1"})
	m.cycleRoleFilter()

	for _, entry := range m.entries {
		if entry.RecommendationID == "rec-1" {
			t.Fatalf("hidden removed recommendation reappeared after cycling role filter: %#v", m.entries)
		}
	}
}

func TestRerunFinishedUpdatesHiddenEntryInAllEntries(t *testing.T) {
	m := NewModel([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "old", Role: sharedtypes.RoleContributor, DraftComment: "old draft"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two", Role: sharedtypes.RoleMaintainer},
	})
	m.width = 100
	m.roleFilter = RoleFilterMaintainer
	m.entries = applyRoleFilter(m.allEntries, m.roleFilter)

	m.applyActionFinished(actionFinishedMsg{
		verb:             "rerun",
		recommendationID: "rec-1",
		updatedEntries:   []Entry{{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "new", Role: sharedtypes.RoleContributor, DraftComment: "new draft"}},
	})
	m.cycleRoleFilter()

	if len(m.entries) != 1 || m.entries[0].RecommendationID != "rec-1" {
		t.Fatalf("entries after cycling filter = %#v, want rec-1", m.entries)
	}
	if m.entries[0].DraftComment != "new draft" {
		t.Fatalf("draft after hidden rerun = %q, want new draft", m.entries[0].DraftComment)
	}
}

func TestEditCurrentReplacesAllEntries(t *testing.T) {
	m := NewModelWithActions([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", Role: sharedtypes.RoleContributor, DraftComment: "draft"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two", Role: sharedtypes.RoleMaintainer},
	}, ModelActions{
		Edit: func(entry Entry) (Entry, error) {
			entry.DraftComment = "edited draft"
			return entry, nil
		},
	})
	m.width = 100
	m.roleFilter = RoleFilterContributor
	m.entries = applyRoleFilter(m.allEntries, m.roleFilter)

	m.editCurrent()
	m.cycleRoleFilter()

	idx := -1
	for i := range m.entries {
		if m.entries[i].RecommendationID == "rec-1" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("entries after cycling filter = %#v, want rec-1", m.entries)
	}
	if m.entries[idx].DraftComment != "edited draft" {
		t.Fatalf("draft after cycling filter = %q, want edited draft", m.entries[idx].DraftComment)
	}
}

func TestEditFinishedUpdatesHiddenEntry(t *testing.T) {
	m := NewModel([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", Role: sharedtypes.RoleContributor, DraftComment: "draft"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two", Role: sharedtypes.RoleMaintainer},
	})
	m.width = 100
	m.roleFilter = RoleFilterMaintainer
	m.entries = applyRoleFilter(m.allEntries, m.roleFilter)
	finish := func(error) (Entry, error) {
		return Entry{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", Role: sharedtypes.RoleContributor, DraftComment: "edited draft"}, nil
	}

	m.applyEditFinished(editFinishedMsg{recommendationID: "rec-1", finish: finish})
	m.cycleRoleFilter()

	if len(m.entries) != 1 || m.entries[0].RecommendationID != "rec-1" {
		t.Fatalf("entries after cycling filter = %#v, want rec-1", m.entries)
	}
	if m.entries[0].DraftComment != "edited draft" {
		t.Fatalf("draft after cycling filter = %q, want edited draft", m.entries[0].DraftComment)
	}
}

func TestModelSKeyDoesNotMarkTriaged(t *testing.T) {
	called := false
	m := NewModelWithDismiss([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           1,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "one",
	}}, func(entries []Entry) error {
		called = true
		return nil
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	next := updated.(Model)
	if cmd != nil || called || len(next.entries) != 1 {
		t.Fatalf("s key should not mark triaged; cmd=%v called=%v entries=%d", cmd, called, len(next.entries))
	}
}

func TestModelApproveRemovesCurrentEntry(t *testing.T) {
	var approved []string
	m := NewModelWithActions([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", StateChange: sharedtypes.StateChangeNone},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindPR, Title: "two", StateChange: sharedtypes.StateChangeMerge},
	}, ModelActions{
		Approve: func(entries []Entry) error {
			for _, entry := range entries {
				approved = append(approved, entry.RecommendationID)
			}
			return nil
		},
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	finishMsg := runActionCmd(t, cmd)
	if len(approved) != 1 || approved[0] != "rec-1" {
		t.Fatalf("approved = %#v, want [rec-1]", approved)
	}
	updated, _ = updated.(Model).Update(finishMsg)
	next := updated.(Model)
	if len(next.entries) != 1 || next.entries[0].RecommendationID != "rec-2" {
		t.Fatalf("entries after approve = %#v, want [rec-2]", next.entries)
	}
	if !strings.Contains(stripANSI(next.View()), "approved acme/widgets #1") {
		t.Fatalf("View() missing approval status in:\n%s", next.View())
	}
}

func TestModelCopyPromptCopiesCurrentEntryPrompt(t *testing.T) {
	var copied []string
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		FixPrompt:        "Fix https://github.com/acme/widgets/issues/42 by adding a regression test.",
	}}, ModelActions{
		CopyPrompt: func(entry Entry) error {
			copied = append(copied, entry.FixPrompt)
			return nil
		},
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if cmd == nil {
		t.Fatal("expected copy prompt command")
	}
	if len(copied) != 0 {
		t.Fatalf("copy prompt ran synchronously, copied = %#v", copied)
	}
	updated, _ = updated.(Model).Update(cmd())
	next := updated.(Model)
	if len(copied) != 1 || copied[0] != "Fix https://github.com/acme/widgets/issues/42 by adding a regression test." {
		t.Fatalf("copied = %#v", copied)
	}
	if len(next.entries) != 1 {
		t.Fatalf("copying prompt should keep entry in inbox, got %d entries", len(next.entries))
	}
	if !strings.Contains(stripANSI(next.View()), "copied prompt for acme/widgets #42") {
		t.Fatalf("View() missing copy status in:\n%s", next.View())
	}
}

func TestModelFixRunsCurrentEntryPrompt(t *testing.T) {
	var fixed []Entry
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		StateChange:      sharedtypes.StateChangeFixRequired,
		FixPrompt:        "Fix https://github.com/acme/widgets/issues/42 by adding a regression test.",
	}}, ModelActions{
		Fix: func(entry Entry) error {
			fixed = append(fixed, entry)
			return nil
		},
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if cmd == nil {
		t.Fatal("expected fix command")
	}
	next := updated.(Model)
	if _, ok := next.pendingActions["rec-1"]; !ok {
		t.Fatalf("expected pending fix action, got %#v", next.pendingActions)
	}
	finishMsg := runActionCmd(t, cmd)
	if finishMsg.err != nil {
		t.Fatalf("fix command error = %v", finishMsg.err)
	}
	if finishMsg.verb != "fix" {
		t.Fatalf("verb = %q, want fix", finishMsg.verb)
	}
	updated, _ = next.Update(finishMsg)
	next = updated.(Model)
	if len(fixed) != 1 || fixed[0].FixPrompt == "" {
		t.Fatalf("fixed = %#v, want entry with fix prompt", fixed)
	}
	if !strings.Contains(stripANSI(next.View()), "queued fix acme/widgets #42") {
		t.Fatalf("View() missing fix status in:\n%s", next.View())
	}
}

func TestModelOpenURLOpensCurrentEntryURL(t *testing.T) {
	var opened []string
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		URL:              "https://github.com/acme/widgets/issues/42",
	}}, ModelActions{
		OpenURL: func(entry Entry) error {
			opened = append(opened, entry.URL)
			return nil
		},
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if cmd == nil {
		t.Fatal("expected open url command")
	}
	if len(opened) != 0 {
		t.Fatalf("open url ran synchronously, opened = %#v", opened)
	}
	updated, _ = updated.(Model).Update(cmd())
	next := updated.(Model)
	if len(opened) != 1 || opened[0] != "https://github.com/acme/widgets/issues/42" {
		t.Fatalf("opened = %#v", opened)
	}
	if len(next.entries) != 1 {
		t.Fatalf("opening url should keep entry in inbox, got %d entries", len(next.entries))
	}
	if !strings.Contains(stripANSI(next.View()), "opened acme/widgets #42") {
		t.Fatalf("View() missing open status in:\n%s", next.View())
	}
}

func TestModelOpenURLLogsWhenNoURL(t *testing.T) {
	var opened int
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "panic in parser",
		URL:              "",
	}}, ModelActions{
		OpenURL: func(entry Entry) error {
			opened++
			return nil
		},
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if cmd != nil {
		t.Fatalf("expected no command when entry has no URL, got %v", cmd)
	}
	if opened != 0 {
		t.Fatalf("open url should not have been called, got %d", opened)
	}
	next := updated.(Model)
	if !strings.Contains(stripANSI(next.View()), "no URL for acme/widgets #42") {
		t.Fatalf("View() missing missing-url status in:\n%s", next.View())
	}
}

func TestModelEditUpdatesCurrentDraft(t *testing.T) {
	m := NewModelWithActions([]Entry{{
		RecommendationID:       "rec-1",
		RepoID:                 "acme/widgets",
		Number:                 1,
		Kind:                   sharedtypes.ItemKindIssue,
		Title:                  "one",
		StateChange:            sharedtypes.StateChangeNone,
		OriginalStateChange:    sharedtypes.StateChangeNone,
		ProposedLabels:         []string{"bug"},
		OriginalProposedLabels: []string{"bug"},
		DraftComment:           "old draft",
		OriginalDraftComment:   "old draft",
	}}, ModelActions{
		Edit: func(entry Entry) (Entry, error) {
			entry.StateChange = sharedtypes.StateChangeRequestChanges
			entry.ProposedLabels = []string{"needs-work"}
			entry.DraftComment = "new draft"
			return entry, nil
		},
	})
	m.width = 100

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	next := updated.(Model)
	view := stripANSI(next.View())
	if !strings.Contains(view, "new draft") {
		t.Fatalf("View() missing edited draft in:\n%s", view)
	}
	for _, want := range []string{"request changes", "labels: needs-work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in action summary:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "edited recommendation for acme/widgets #1") {
		t.Fatalf("View() missing edit status in:\n%s", view)
	}
}

// TestModelEditExecReturnsTeaCmdForExternalEditor pins the regression where
// pressing 'e' would synchronously spawn $EDITOR from inside Update(), with
// bubbletea still holding the alt screen and stdin/stdout. The fix routes the
// editor through tea.ExecProcess by exposing an EditExec hook that returns an
// *exec.Cmd, which bubbletea runs with the terminal released and restored.
// TestModelApproveAsyncDefersUntilFinishMsg pins the regression where
// pressing 'a' would block the bubbletea event loop for several seconds
// while gh did the real work, leaving the user with no visual feedback
// until the entire chain completed. The action must instead schedule a
// tea.Cmd and immediately mark the entry as pending so the next render
// can show "approving..." feedback.
func TestModelApproveAsyncDefersUntilFinishMsg(t *testing.T) {
	actions := ModelActions{
		Approve: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
	}}, actions)
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatal("expected non-nil cmd from approve - the action must run async via tea.Cmd, not synchronously inside Update")
	}
	next := updated.(Model)
	if _, ok := next.pendingActions["rec-1"]; !ok {
		t.Fatalf("expected pending action for rec-1, got %#v", next.pendingActions)
	}
	if len(next.entries) != 1 {
		t.Fatalf("entry should not be removed until actionFinishedMsg arrives, got %d entries", len(next.entries))
	}
}

// TestModelApproveFailureKeepsEntryAndLogsError verifies that a failed
// approve leaves the entry in the queue (so the user can retry) and
// records a failure row in the log.
func TestModelApproveFailureKeepsEntryAndLogsError(t *testing.T) {
	actions := ModelActions{
		Approve: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
	}}, actions)
	m.width = 100

	// Send the start, then a failed finish.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated, _ = updated.(Model).Update(actionFinishedMsg{
		verb:             "approve",
		recommendationID: "rec-1",
		err:              fmt.Errorf("boom"),
	})
	next := updated.(Model)
	if len(next.entries) != 1 {
		t.Fatalf("failed approve should keep the entry, got %d entries", len(next.entries))
	}
	if _, ok := next.pendingActions["rec-1"]; ok {
		t.Fatal("pending should be cleared even on error")
	}
	foundFailure := false
	for _, e := range next.logEntries {
		if e.state == logStateFailed && e.recommendationID == "rec-1" {
			foundFailure = true
			break
		}
	}
	if !foundFailure {
		t.Fatalf("expected failed log entry for rec-1, got %#v", next.logEntries)
	}
}

// TestModelPendingActionBlocksConflictingKey ensures the user can't kick
// off a second action on the same entry while one is in flight - the
// duplicate key press should be a no-op that surfaces a warning in the log.
func TestModelPendingActionBlocksConflictingKey(t *testing.T) {
	calls := 0
	actions := ModelActions{
		Dismiss: func([]Entry) error {
			calls++
			return nil
		},
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
	}}, actions)
	m.width = 100

	updated, cmd1 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if cmd1 == nil {
		t.Fatal("first mark-triaged action should return cmd")
	}
	updated2, cmd2 := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if cmd2 != nil {
		t.Fatal("second mark-triaged action while first is pending must be blocked - no cmd should be returned")
	}
	next := updated2.(Model)
	foundInfo := false
	for _, e := range next.logEntries {
		if e.state == logStateInfo {
			foundInfo = true
			break
		}
	}
	if !foundInfo {
		t.Fatal("expected info log entry warning the user about the conflicting keystroke")
	}
}

// TestModelPendingActionDoesNotBlockOtherEntries verifies the conflict
// guard is per-recommendation: pending on rec-1 should not block actions
// on rec-2.
func TestModelPendingActionDoesNotBlockOtherEntries(t *testing.T) {
	actions := ModelActions{
		Approve: func([]Entry) error { return nil },
		Dismiss: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue},
	}, actions)
	m.width = 100

	// Approve first entry.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	// Move to second.
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	// Mark second triaged - must not be blocked by the pending approve on rec-1.
	_, cmd := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if cmd == nil {
		t.Fatal("mark triaged on rec-2 must not be blocked by pending approve on rec-1")
	}
}

// TestModelQuitDuringPendingArmsConfirm verifies q while actions are
// in-flight asks for a second q to confirm, preventing accidental loss of
// in-progress work.
func TestModelQuitDuringPendingArmsConfirm(t *testing.T) {
	actions := ModelActions{
		Dismiss: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue,
	}}, actions)
	m.width = 100

	// Start a pending action.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	// Press q - should not quit yet.
	updated2, cmd := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	next := updated2.(Model)
	if next.quitting {
		t.Fatal("first q with pending actions should arm confirm, not quit")
	}
	if cmd != nil {
		// cmd may be tea.Quit or nil; arming should return nil
		t.Fatalf("first q should return nil cmd while pending, got %v", cmd)
	}
	if !next.quitArmed {
		t.Fatal("expected quitArmed = true after first q")
	}
	// Second q quits.
	updated3, _ := next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !updated3.(Model).quitting {
		t.Fatal("second q should quit")
	}
}

// TestModelPendingRendersSpinnerAndMorphsActionBar checks that while an
// action is in flight, (a) the card's bottom action bar replaces the
// a/e/m/r hints with a spinner and verb, so pressing those keys is
// obviously meaningless until the action completes, and (b) the rail
// glyph swaps to the pending marker.
func TestModelPendingRendersSpinnerAndMorphsActionBar(t *testing.T) {
	actions := ModelActions{
		Dismiss: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "in flight",
	}}, actions)
	m.width = 120
	m.height = 30

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	view := stripANSI(updated.(Model).View())
	if !strings.Contains(view, "marking triaged acme/widgets #42") {
		t.Fatalf("View() should show 'marking triaged acme/widgets #42' in the morphed action bar:\n%s", view)
	}
	if strings.Contains(view, "a approve") || strings.Contains(view, "m mark triaged") {
		t.Fatalf("View() should hide the regular action hints while pending:\n%s", view)
	}
	if !strings.Contains(view, "…") {
		t.Fatalf("View() should show pending glyph in the rail:\n%s", view)
	}
}

func TestModelQueueRailShowsPendingGlyphForInProgressFixJob(t *testing.T) {
	m := NewModel([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "fix is running",
		FixJobID:         "fix-1",
		FixStatus:        "running",
		FixPhase:         "running_agent",
	}})
	m.width = 120
	m.height = 30

	rail := stripANSI(m.renderQueueRail(44, 12))
	if !strings.Contains(rail, "… #42") {
		t.Fatalf("renderQueueRail() should show pending glyph for running fix job:\n%s", rail)
	}
	if strings.Contains(rail, "○ #42") {
		t.Fatalf("renderQueueRail() should replace issue glyph while fix job is running:\n%s", rail)
	}
}

func TestModelQueueRailCursorHighlightUsesUninterruptedBackground(t *testing.T) {
	m := NewModel([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "selected-title",
	}, {
		RecommendationID: "rec-2",
		RepoID:           "acme/widgets",
		Number:           43,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "other-title",
	}})
	m.width = 120
	m.height = 30
	m.cursor = 0

	line := ""
	for _, candidate := range strings.Split(m.renderQueueRail(44, 12), "\n") {
		if strings.Contains(candidate, "selected-title") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatal("selected row not found")
	}
	if w := lipgloss.Width(line); w != 44 {
		t.Fatalf("highlighted row width = %d, want 44:\n%s", w, line)
	}

	titleIdx := strings.Index(line, "selected-title")
	beforeTitle := line[:titleIdx]
	backgroundIdx := strings.LastIndex(beforeTitle, "44m")
	if backgroundIdx < 0 {
		t.Fatalf("selected row should start a blue background before the title:\n%q", line)
	}
	if resetIdx := strings.LastIndex(beforeTitle, "\x1b[0m"); resetIdx > backgroundIdx {
		t.Fatalf("selected row background should not be reset before the title:\n%q", line)
	}
}

// TestModelLogPanelWrapsLongFailureMessage pins the regression where a
// long error message (e.g. a wrapped gh CLI/GraphQL error) overflowed
// past the activity panel's right border, hiding the rest of the message.
// The panel must wrap inside its width so the whole error stays visible.
func TestModelLogPanelWrapsLongFailureMessage(t *testing.T) {
	actions := ModelActions{
		Approve: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "kunchenguid/gnhf",
		Number:           96,
		Kind:             sharedtypes.ItemKindPR,
		Title:            "long-error case",
	}}, actions)
	m.width = 100
	m.height = 30

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	longErr := errors.New("merge kunchenguid/gnhf#96: gh pr merge kunchenguid/gnhf#96: GraphQL: Merge commits are not allowed for this repository (mergePullRequest)")
	updated, _ = updated.(Model).Update(actionFinishedMsg{
		verb:             "approve",
		recommendationID: "rec-1",
		err:              longErr,
	})
	view := stripANSI(updated.(Model).View())
	for _, want := range []string{
		"approve failed kunchenguid/gnhf #96",
		"GraphQL: Merge commits are not allowed",
		"(mergePullRequest)",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() should contain %q (long error must wrap, not get truncated):\n%s", want, view)
		}
	}
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Fatalf("View line[%d] = %q has width %d, exceeds m.width %d", i, line, w, m.width)
		}
	}
}

// TestModelLogPanelRendersAndShrinksToFitTerminal verifies the activity
// log appears below the card with successful/failed entries, and that on
// a very short terminal the panel is dropped rather than overflowing.
func TestModelLogPanelRendersAndShrinksToFitTerminal(t *testing.T) {
	actions := ModelActions{
		Dismiss: func([]Entry) error { return nil },
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "log this",
	}}, actions)
	m.width = 120
	m.height = 30

	// Drive a successful mark-triaged action end-to-end.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	finishMsg := runActionCmd(t, cmd)
	updated, _ = updated.(Model).Update(finishMsg)
	tall := updated.(Model)
	tallView := stripANSI(tall.View())
	if !strings.Contains(tallView, "Activity") {
		t.Fatalf("expected Activity log panel, got:\n%s", tallView)
	}
	if !strings.Contains(tallView, "marked triaged acme/widgets #42") {
		t.Fatalf("expected marked-triaged log line, got:\n%s", tallView)
	}

	// Now squeeze the terminal vertically. The log panel must be the first
	// thing dropped so the card and nav bar still fit.
	short := tall
	short.height = 12
	shortView := stripANSI(short.View())
	totalLines := len(strings.Split(strings.TrimRight(shortView, "\n"), "\n"))
	if totalLines > short.height {
		t.Fatalf("View at height=%d produced %d lines (overflow):\n%s", short.height, totalLines, shortView)
	}
}

func TestModelEditExecReturnsTeaCmdForExternalEditor(t *testing.T) {
	var prepared bool
	actions := ModelActions{
		EditExec: func(entry Entry) (*exec.Cmd, func(error) (Entry, error), error) {
			prepared = true
			cmd := exec.Command("true")
			finish := func(error) (Entry, error) {
				entry.DraftComment = "edited via exec"
				return entry, nil
			}
			return cmd, finish, nil
		},
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID:     "rec-1",
		RepoID:               "acme/widgets",
		Number:               1,
		Kind:                 sharedtypes.ItemKindIssue,
		Title:                "one",
		DraftComment:         "old",
		OriginalDraftComment: "old",
	}}, actions)
	m.width = 100

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !prepared {
		t.Fatalf("EditExec was not invoked when 'e' was pressed")
	}
	if cmd == nil {
		t.Fatalf("expected non-nil tea.Cmd from edit; without it the editor would run synchronously and clobber the bubbletea alt screen")
	}
}

// TestModelEditExecAppliesFinishCallbackResult verifies that once the editor
// command exits and bubbletea delivers editFinishedMsg, the finish callback
// is invoked and the resulting entry replaces the original (matched by
// RecommendationID so reloads during edit don't clobber the wrong row).
func TestModelEditExecAppliesFinishCallbackResult(t *testing.T) {
	finish := func(error) (Entry, error) {
		return Entry{
			RecommendationID: "rec-1",
			RepoID:           "acme/widgets",
			Number:           1,
			Kind:             sharedtypes.ItemKindIssue,
			Title:            "one",
			StateChange:      sharedtypes.StateChangeRequestChanges,
			ProposedLabels:   []string{"needs-work"},
			DraftComment:     "new draft",
		}, nil
	}
	m := NewModelWithActions([]Entry{{
		RecommendationID:       "rec-1",
		RepoID:                 "acme/widgets",
		Number:                 1,
		Kind:                   sharedtypes.ItemKindIssue,
		Title:                  "one",
		StateChange:            sharedtypes.StateChangeNone,
		OriginalStateChange:    sharedtypes.StateChangeNone,
		ProposedLabels:         []string{"bug"},
		OriginalProposedLabels: []string{"bug"},
		DraftComment:           "old",
		OriginalDraftComment:   "old",
	}}, ModelActions{})
	m.width = 100

	updated, _ := m.Update(editFinishedMsg{recommendationID: "rec-1", finish: finish})
	next := updated.(Model)
	view := stripANSI(next.View())
	if !strings.Contains(view, "new draft") {
		t.Fatalf("View() missing edited draft after editFinishedMsg:\n%s", view)
	}
	for _, want := range []string{"request changes", "labels: needs-work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in action summary:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "edited recommendation for acme/widgets #1") {
		t.Fatalf("View() missing edit status:\n%s", view)
	}
}

func TestModelRerunRefreshesCurrentEntry(t *testing.T) {
	var gotInstructions string
	m := NewModelWithActions([]Entry{{
		RecommendationID:     "rec-1",
		RepoID:               "acme/widgets",
		Number:               1,
		Kind:                 sharedtypes.ItemKindIssue,
		Title:                "one",
		StateChange:          sharedtypes.StateChangeNone,
		DraftComment:         "old draft",
		OriginalDraftComment: "old draft",
		Rationale:            "old rationale",
	}}, ModelActions{
		Rerun: func(entries []Entry, instructions string) ([]Entry, error) {
			gotInstructions = instructions
			entry := entries[0]
			entry.RecommendationID = "rec-2"
			entry.DraftComment = "new draft"
			entry.OriginalDraftComment = "new draft"
			entry.Rationale = "new rationale"
			return []Entry{entry}, nil
		},
	})
	m.width = 100

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Fatalf("pressing r should open the rerun instructions prompt, got cmd %T", cmd)
	}
	view := stripANSI(updated.(Model).View())
	if !strings.Contains(view, "Rerun triage") || !strings.Contains(view, "Add instructions for the agent") {
		t.Fatalf("View() missing rerun instructions prompt:\n%s", view)
	}

	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Focus on the maintainer's clarification.")})
	updated, cmd = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	finishMsg := runActionCmd(t, cmd)
	updated, _ = updated.(Model).Update(finishMsg)
	next := updated.(Model)
	view = stripANSI(next.View())
	if gotInstructions != "Focus on the maintainer's clarification." {
		t.Fatalf("rerun instructions = %q", gotInstructions)
	}
	if !strings.Contains(view, "new draft") {
		t.Fatalf("View() missing rerun draft in:\n%s", view)
	}
	if !strings.Contains(view, "new rationale") {
		t.Fatalf("View() missing rerun rationale in:\n%s", view)
	}
	if !strings.Contains(view, "reran acme/widgets #1") {
		t.Fatalf("View() missing rerun status in:\n%s", view)
	}
	if next.entries[0].RecommendationID != "rec-2" {
		t.Fatalf("RecommendationID after rerun = %q, want rec-2", next.entries[0].RecommendationID)
	}
	if next.entries[0].OriginalDraftComment != "new draft" {
		t.Fatalf("OriginalDraftComment after rerun = %q, want new draft", next.entries[0].OriginalDraftComment)
	}
}

func TestModelViewFitsWithinTerminalHeightWithRerunInput(t *testing.T) {
	m := NewModelWithActions([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           1,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "one",
		StateChange:      sharedtypes.StateChangeNone,
		Rationale:        strings.Repeat("needs careful follow-up ", 20),
		DraftComment:     "please share repro",
	}}, ModelActions{
		Rerun: func(entries []Entry, instructions string) ([]Entry, error) {
			return entries, nil
		},
	})
	m.width = 100
	m.height = 24

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Fatalf("pressing r returned cmd %T", cmd)
	}
	view := updated.(Model).View()
	height := lipgloss.Height(view)
	if height > 24 {
		t.Fatalf("View() height = %d, want <= 24 with rerun input\n%s", height, view)
	}
}

func TestModelReloadRefreshesEntriesAndPreservesEditedDrafts(t *testing.T) {
	m := NewModelWithActions([]Entry{
		{
			RecommendationID:       "rec-1",
			RepoID:                 "acme/widgets",
			Number:                 1,
			Kind:                   sharedtypes.ItemKindIssue,
			Title:                  "one",
			StateChange:            sharedtypes.StateChangeNone,
			OriginalStateChange:    sharedtypes.StateChangeNone,
			ProposedLabels:         []string{"bug"},
			OriginalProposedLabels: []string{"bug"},
			DraftComment:           "edited draft",
			OriginalDraftComment:   "old draft",
		},
		{
			RecommendationID:       "rec-2",
			RepoID:                 "acme/widgets",
			Number:                 2,
			Kind:                   sharedtypes.ItemKindPR,
			Title:                  "two",
			StateChange:            sharedtypes.StateChangeMerge,
			OriginalStateChange:    sharedtypes.StateChangeMerge,
			ProposedLabels:         []string{"ready-to-merge"},
			OriginalProposedLabels: []string{"ready-to-merge"},
			DraftComment:           "second draft",
			OriginalDraftComment:   "second draft",
		},
	}, ModelActions{Reload: func() ([]Entry, error) { return nil, nil }})
	m.width = 100
	m.cursor = 1

	updated, _ := m.Update(reloadedEntriesMsg{Entries: []Entry{
		{
			RecommendationID:     "rec-1",
			RepoID:               "acme/widgets",
			Number:               1,
			Kind:                 sharedtypes.ItemKindIssue,
			Title:                "one refreshed",
			StateChange:          sharedtypes.StateChangeNone,
			OriginalStateChange:  sharedtypes.StateChangeNone,
			DraftComment:         "server draft",
			OriginalDraftComment: "server draft",
		},
		{
			RecommendationID:     "rec-3",
			RepoID:               "acme/widgets",
			Number:               3,
			Kind:                 sharedtypes.ItemKindIssue,
			Title:                "three",
			StateChange:          sharedtypes.StateChangeNone,
			DraftComment:         "third draft",
			OriginalDraftComment: "third draft",
		},
	}})
	next := updated.(Model)

	if len(next.entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(next.entries))
	}
	if next.entries[0].DraftComment != "edited draft" {
		t.Fatalf("edited draft after reload = %q, want %q", next.entries[0].DraftComment, "edited draft")
	}
	if next.allEntries[0].DraftComment != "edited draft" {
		t.Fatalf("allEntries draft after reload = %q, want %q", next.allEntries[0].DraftComment, "edited draft")
	}
	if next.entries[0].OriginalDraftComment != "old draft" {
		t.Fatalf("original draft after reload = %q, want %q", next.entries[0].OriginalDraftComment, "old draft")
	}
	if got := next.entries[0].ProposedLabels; len(got) != 1 || got[0] != "bug" {
		t.Fatalf("edited labels after reload = %#v, want [bug]", got)
	}
	if next.entries[0].OriginalStateChange != sharedtypes.StateChangeNone {
		t.Fatalf("original action after reload = %q, want %q", next.entries[0].OriginalStateChange, sharedtypes.StateChangeNone)
	}
	if next.entries[0].Title != "one refreshed" {
		t.Fatalf("refreshed title = %q, want %q", next.entries[0].Title, "one refreshed")
	}
	if next.cursor != 1 {
		t.Fatalf("cursor after reload = %d, want 1", next.cursor)
	}
	if !strings.Contains(next.View(), "three") {
		t.Fatalf("View() missing refreshed entry in:\n%s", next.View())
	}
	if strings.Contains(next.View(), "two") {
		t.Fatalf("View() still contains removed entry in:\n%s", next.View())
	}
}

func TestModelReloadErrorShowsStatus(t *testing.T) {
	m := NewModelWithActions(nil, ModelActions{Reload: func() ([]Entry, error) { return nil, nil }})
	m.width = 100

	updated, _ := m.Update(reloadedEntriesMsg{Err: errReloadFailed{}})
	view := updated.(Model).View()
	if !strings.Contains(view, "refresh failed: reload failed") {
		t.Fatalf("View() missing refresh error status in:\n%s", view)
	}
}

func TestModelHelpToggleShowsAndHidesHelp(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:      "acme/widgets",
		Number:      1,
		Kind:        sharedtypes.ItemKindIssue,
		Title:       "one",
		StateChange: sharedtypes.StateChangeNone,
	}})
	m.width = 100

	if strings.Contains(m.View(), "Keyboard shortcuts") {
		t.Fatalf("View() unexpectedly shows help before toggle:\n%s", m.View())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	withHelp := updated.(Model)
	view := withHelp.View()
	for _, want := range []string{
		"Keyboard shortcuts",
		"approve active option",
		"copy active option's coding-agent prompt",
		"next item",
		"down / up          scroll overflowing card",
		"toggle this help",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q after help toggle:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"j / n", "k / p"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("View() should not advertise %q after help toggle:\n%s", unwanted, view)
		}
	}

	updated, _ = withHelp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	withoutHelp := updated.(Model)
	if strings.Contains(withoutHelp.View(), "Keyboard shortcuts") {
		t.Fatalf("View() still shows help after second toggle:\n%s", withoutHelp.View())
	}
}

func TestModelViewShowsApprovalErrorBanner(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:        "acme/widgets",
		Number:        42,
		Kind:          sharedtypes.ItemKindIssue,
		Title:         "Bug: triage queue stalls",
		StateChange:   sharedtypes.StateChangeNone,
		DraftComment:  "Can you share a minimal repro?",
		ApprovalError: "comment on acme/widgets#42: gh comment failed",
	}})
	m.width = 100

	view := m.View()
	for _, want := range []string{
		"Last approval failed:",
		"comment on acme/widgets#42: gh comment failed",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in:\n%s", want, view)
		}
	}
}

func TestModelActionSummaryDescribesProposedActions(t *testing.T) {
	m := NewModel([]Entry{
		{
			RepoID:       "acme/widgets",
			Number:       7,
			Kind:         sharedtypes.ItemKindPR,
			Title:        "feat: add streaming",
			StateChange:  sharedtypes.StateChangeNone,
			DraftComment: "Can you confirm the approach before I review?",
		},
		{
			RepoID:      "acme/widgets",
			Number:      8,
			Kind:        sharedtypes.ItemKindIssue,
			Title:       "Question already answered",
			StateChange: sharedtypes.StateChangeNone,
		},
	})
	m.width = 100

	// Cursor on first entry: has DraftComment -> "Will: comment".
	view := stripANSI(m.View())
	if !strings.Contains(view, "Will: comment") {
		t.Fatalf("View() missing 'Will: comment' for entry with DraftComment:\n%s", view)
	}

	// Move to second entry which has no DraftComment, no state change ->
	// "Will: mark triaged".
	m.cursor = 1
	view = stripANSI(m.View())
	if !strings.Contains(view, "Will: mark triaged") {
		t.Fatalf("View() missing 'Will: mark triaged' for empty proposal:\n%s", view)
	}
}

func TestModelViewMarksUnconfiguredEntriesAndSummary(t *testing.T) {
	m := NewModel([]Entry{
		{
			RepoID:      "acme/widgets",
			Number:      42,
			Kind:        sharedtypes.ItemKindIssue,
			Title:       "configured",
			StateChange: sharedtypes.StateChangeNone,
		},
		{
			RepoID:       "orphan/repo",
			Number:       7,
			Kind:         sharedtypes.ItemKindIssue,
			Title:        "unconfigured",
			StateChange:  sharedtypes.StateChangeNone,
			Unconfigured: true,
		},
	})
	m.width = 100

	// Move cursor to the unconfigured entry; the card title should mark it.
	m.cursor = 1
	view := m.View()
	if !strings.Contains(view, "unconfigured") {
		t.Fatalf("View() at unconfigured entry missing 'unconfigured' marker in:\n%s", view)
	}
	// Cursor back to the configured entry; "unconfigured" should not appear in the card title region.
	m.cursor = 0
	stripped := stripANSI(m.View())
	firstCardLine := ""
	for _, line := range strings.Split(stripped, "\n") {
		if strings.Contains(line, "acme/widgets") && strings.Contains(line, "#42") {
			firstCardLine = line
			break
		}
	}
	if firstCardLine == "" {
		t.Fatalf("View() missing card title for configured entry in:\n%s", stripped)
	}
	if strings.Contains(firstCardLine, "unconfigured") {
		t.Fatalf("configured entry's card title should not be marked unconfigured: %q", firstCardLine)
	}
}

func TestModelCapturesWindowSize(t *testing.T) {
	m := NewModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 42})
	got := updated.(Model)
	if got.width != 120 {
		t.Fatalf("width = %d, want 120", got.width)
	}
	if got.height != 42 {
		t.Fatalf("height = %d, want 42", got.height)
	}
}

func TestModelViewFitsWithinTerminalHeight(t *testing.T) {
	entries := make([]Entry, 0, 30)
	for i := 0; i < 30; i++ {
		entries = append(entries, Entry{
			RecommendationID: fmt.Sprintf("rec-%d", i),
			RepoID:           "acme/widgets",
			Number:           i + 1,
			Kind:             sharedtypes.ItemKindIssue,
			Title:            fmt.Sprintf("issue %d", i+1),
			StateChange:      sharedtypes.StateChangeNone,
			Confidence:       sharedtypes.ConfidenceMedium,
			Rationale:        "needs repro",
			DraftComment:     "please share repro",
		})
	}
	m := NewModel(entries)
	m.width = 120
	m.height = 30

	view := m.View()
	height := lipgloss.Height(view)
	if height > 30 {
		t.Fatalf("View() height = %d, want <= 30 (terminal height)\n%s", height, view)
	}
}

func TestModelInboxScrollsToKeepCursorVisible(t *testing.T) {
	entries := make([]Entry, 0, 30)
	for i := 0; i < 30; i++ {
		entries = append(entries, Entry{
			RecommendationID: fmt.Sprintf("rec-%d", i),
			RepoID:           "acme/widgets",
			Number:           i + 1,
			Kind:             sharedtypes.ItemKindIssue,
			Title:            fmt.Sprintf("issue-%d-title", i+1),
			StateChange:      sharedtypes.StateChangeNone,
		})
	}
	m := NewModel(entries)
	m.width = 120
	m.height = 30
	m.cursor = 25

	// Card view focuses on the cursor's entry; navigating updates the card.
	view := m.View()
	if !strings.Contains(view, "issue-26-title") {
		t.Fatalf("View() at cursor=25 must show entry 25 (issue-26-title) in:\n%s", view)
	}
	// On a 120-wide terminal the queue rail is shown; entry 0 should be
	// scrolled out so the cursor (25) stays visible.
	stripped := stripANSI(view)
	// With 30 entries (max digit width 2), entry 1's padded label is "#1 ".
	if strings.Contains(stripped, "#1   issue-1-title") {
		t.Fatalf("View() at cursor=25 should scroll entry 0 out of the rail in:\n%s", stripped)
	}
}

func TestModelQueueRailScrollHintShowsJKNavigation(t *testing.T) {
	got := stripANSI(formatScrollHint(0, 1))
	if !strings.Contains(got, "j ↓ / k ↑") {
		t.Fatalf("formatScrollHint() should show j/k navigation keys, got %q", got)
	}
	if strings.Contains(got, "(j/k)") {
		t.Fatalf("formatScrollHint() should not use ambiguous j/k suffix, got %q", got)
	}
}

func TestModelBottomNavBarOmitsInboxNavigation(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:      "acme/widgets",
		Number:      1,
		Kind:        sharedtypes.ItemKindIssue,
		Title:       "one",
		StateChange: sharedtypes.StateChangeNone,
	}})
	m.width = 100
	m.height = 30

	view := stripANSI(m.View())
	if !strings.Contains(view, "q quit") || !strings.Contains(view, "? help") {
		t.Fatalf("View() should keep app-level nav hints in bottom row:\n%s", view)
	}
	for _, unwanted := range []string{"j next", "k prev"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("View() should not show inbox navigation %q in bottom row:\n%s", unwanted, view)
		}
	}
}

func TestModelQueueRailGroupsByRepoWithDimmedHeadings(t *testing.T) {
	entries := []Entry{
		{RecommendationID: "a-1", RepoID: "acme/alpha", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "alpha-one", StateChange: sharedtypes.StateChangeNone},
		{RecommendationID: "a-2", RepoID: "acme/alpha", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "alpha-two", StateChange: sharedtypes.StateChangeNone},
		{RecommendationID: "b-1", RepoID: "beta/repo", Number: 1, Kind: sharedtypes.ItemKindPR, Title: "beta-one", StateChange: sharedtypes.StateChangeMerge},
	}
	m := NewModel(entries)
	m.width = 160
	m.height = 40

	stripped := stripANSI(m.View())

	// Both repo names should appear as group headings in the rail.
	for _, repo := range []string{"acme/alpha", "beta/repo"} {
		if !strings.Contains(stripped, repo) {
			t.Fatalf("View() missing repo heading %q in:\n%s", repo, stripped)
		}
	}

	// Entries should be grouped: alpha entries together, beta after.
	idxAlphaOne := strings.Index(stripped, "alpha-one")
	idxAlphaTwo := strings.Index(stripped, "alpha-two")
	idxBetaOne := strings.Index(stripped, "beta-one")
	if idxAlphaOne < 0 || idxAlphaTwo < 0 || idxBetaOne < 0 {
		t.Fatalf("missing entry titles in queue rail:\n%s", stripped)
	}
	if !(idxAlphaOne < idxAlphaTwo && idxAlphaTwo < idxBetaOne) {
		t.Fatalf("entries should appear in repo-grouped order (alpha-one < alpha-two < beta-one), got %d %d %d in:\n%s", idxAlphaOne, idxAlphaTwo, idxBetaOne, stripped)
	}

	// Repo headings should be dim (bright black, ANSI 38;5;8 or 90).
	rawView := m.View()
	if !strings.Contains(rawView, "\x1b[90macme/alpha\x1b[0m") && !strings.Contains(rawView, "\x1b[90macme/alpha") {
		// Accept either the closed or the open dim style as long as it's wrapped in dim escape.
		if !strings.Contains(rawView, "acme/alpha") {
			t.Fatalf("repo heading missing in raw view")
		}
	}
}

func TestModelResponsiveLayoutPlacesQueueRailBesideCardOnWideTerminal(t *testing.T) {
	entries := []Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "issue-title-MARKER", StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceHigh, Rationale: "RATIONALE-MARKER", DraftComment: "DRAFT-MARKER"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindPR, Title: "second", StateChange: sharedtypes.StateChangeMerge},
	}
	m := NewModel(entries)
	m.width = 160
	m.height = 40

	view := m.View()
	lines := strings.Split(stripANSI(view), "\n")
	var railLine, cardLine int = -1, -1
	for i, line := range lines {
		if railLine < 0 && strings.Contains(line, "Inbox · ") {
			railLine = i
		}
		if cardLine < 0 && strings.Contains(line, "acme/widgets · ○ #1") {
			cardLine = i
		}
	}
	if railLine < 0 || cardLine < 0 {
		t.Fatalf("View() missing rail or card title in:\n%s", view)
	}
	if railLine != cardLine {
		t.Fatalf("Wide layout: queue rail (line %d) and card (line %d) should share the same line\n%s", railLine, cardLine, view)
	}
}

func TestModelNarrowLayoutHidesQueueRailAndShowsCardOnly(t *testing.T) {
	entries := []Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "first issue", StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceHigh, Rationale: "rationale", DraftComment: "draft"},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "second issue", StateChange: sharedtypes.StateChangeNone},
	}
	m := NewModel(entries)
	m.width = 80
	m.height = 40

	view := m.View()
	stripped := stripANSI(view)
	// On narrow widths, only the cursor's card is rendered; the second
	// entry's title should not be visible (no queue rail).
	if !strings.Contains(stripped, "first issue") {
		t.Fatalf("narrow View() missing cursor item title in:\n%s", stripped)
	}
	if strings.Contains(stripped, "second issue") {
		t.Fatalf("narrow View() should not show non-cursor entries (no rail), got:\n%s", stripped)
	}
}

func TestModelCompactModeForShortTerminals(t *testing.T) {
	entries := make([]Entry, 0, 5)
	for i := 0; i < 5; i++ {
		entries = append(entries, Entry{
			RecommendationID: fmt.Sprintf("rec-%d", i),
			RepoID:           "acme/widgets",
			Number:           i + 1,
			Kind:             sharedtypes.ItemKindIssue,
			Title:            fmt.Sprintf("issue %d", i+1),
			StateChange:      sharedtypes.StateChangeNone,
		})
	}
	m := NewModel(entries)
	m.width = 100
	m.height = 18

	view := m.View()
	if h := lipgloss.Height(view); h > 18 {
		t.Fatalf("compact View() height = %d, want <= 18\n%s", h, view)
	}
	// Action bar must remain visible even in compact mode.
	if !strings.Contains(view, "a approve") {
		t.Fatalf("compact View() missing action bar 'a approve' in:\n%s", view)
	}
}

func TestModelDetailsHidesNoneFields(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "title",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceHigh,
		Rationale:    "r",
		DraftComment: "d",
		// no labels, no follow-ups, no waiting on, no URL
	}})
	m.width = 100

	details := stripANSI(m.renderDetails())
	for _, banned := range []string{
		"Proposed labels: none",
		"Follow-ups: none",
		"Waiting on: none",
		"URL: ",
	} {
		if strings.Contains(details, banned) {
			t.Fatalf("renderDetails() should not contain %q in:\n%s", banned, details)
		}
	}
}

func TestModelDetailsStatusStripGroupsConfidenceAndWaitingOn(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "title",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceHigh,
		WaitingOn:    sharedtypes.WaitingOnMaintainer,
		TokensIn:     2100,
		TokensOut:    4200,
		Rationale:    "r",
		DraftComment: "d",
	}})
	m.width = 100

	stripped := stripANSI(m.renderDetails())
	firstLine := strings.SplitN(stripped, "\n", 2)[0]
	for _, want := range []string{"confidence: high"} {
		if !strings.Contains(firstLine, want) {
			t.Fatalf("status strip first line missing %q in %q (full:\n%s)", want, firstLine, stripped)
		}
	}
	for _, want := range []string{"currently waiting on maintainer"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("status strip missing %q in:\n%s", want, stripped)
		}
	}
	if strings.Contains(stripped, "2.1k in / 4.2k out") {
		t.Fatalf("status strip should not show token usage:\n%s", stripped)
	}
}

func TestCardTitleOmitsCardMetadata(t *testing.T) {
	entry := Entry{
		RepoID:   "acme/widgets",
		Number:   42,
		Kind:     sharedtypes.ItemKindIssue,
		Author:   "alice",
		AgeLabel: "2h",
	}

	got := stripANSI(cardTitle(entry))
	if strings.Contains(got, "@alice") || strings.Contains(got, "2h") {
		t.Fatalf("cardTitle() should keep contributor and age in card metadata, got %q", got)
	}
}

func TestModelDetailsConfidenceRendersWithLevelColor(t *testing.T) {
	cases := []struct {
		conf      sharedtypes.Confidence
		colorCode string
	}{
		{sharedtypes.ConfidenceHigh, ansiGreen},
		{sharedtypes.ConfidenceMedium, ansiYellow},
		{sharedtypes.ConfidenceLow, ansiRed},
	}
	for _, c := range cases {
		t.Run(string(c.conf), func(t *testing.T) {
			m := NewModel([]Entry{{
				RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue,
				Title: "t", StateChange: sharedtypes.StateChangeNone,
				Confidence: c.conf, Rationale: "r", DraftComment: "d",
			}})
			m.width = 100
			details := m.renderDetails()
			// Look for ANSI escape that includes the foreground color code for this level.
			// Format: \x1b[1;3<N>m where N is the ANSI color number.
			marker := "\x1b[1;3" + c.colorCode + "mconfidence: " + string(c.conf)
			if !strings.Contains(details, marker) {
				t.Fatalf("confidence %s should render with color %s. raw: %q", c.conf, c.colorCode, details)
			}
		})
	}
}

func TestModelDetailsActionSummaryAndURLAppearAfterBody(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:         "acme/widgets",
		Number:         42,
		Kind:           sharedtypes.ItemKindIssue,
		Title:          "title",
		StateChange:    sharedtypes.StateChangeClose,
		Confidence:     sharedtypes.ConfidenceHigh,
		Rationale:      "r",
		DraftComment:   "d",
		ProposedLabels: []string{"bug", "needs-repro"},
		URL:            "https://github.com/acme/widgets/issues/42",
	}})
	m.width = 100

	stripped := stripANSI(m.renderDetails())
	// The action summary should describe both the verbs and the labels:
	// "Will: comment + close   labels: bug, needs-repro"
	if !strings.Contains(stripped, "Will:") {
		t.Fatalf("renderDetails() missing 'Will:' summary in:\n%s", stripped)
	}
	if !strings.Contains(stripped, "comment + close") {
		t.Fatalf("renderDetails() expected 'comment + close' in summary, got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "labels: bug, needs-repro") {
		t.Fatalf("renderDetails() expected labels in summary, got:\n%s", stripped)
	}
	// URL appears on its own line below the summary so it never wraps.
	lines := strings.Split(strings.TrimRight(stripped, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "https://github.com/acme/widgets/issues/42") {
		t.Fatalf("URL should appear on the last line, got %q (full:\n%s)", last, stripped)
	}
}

func TestModelDetailsSectionLabelsAreCyanBold(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "title",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceHigh,
		Rationale:    "r",
		DraftComment: "d",
	}})
	m.width = 100
	details := m.renderDetails()
	// Cyan bold (ANSI 1;36) wrapping the label.
	for _, label := range []string{"Rationale:", "Draft response:"} {
		marker := "\x1b[1;36m" + label
		if !strings.Contains(details, marker) {
			t.Fatalf("expected cyan-bold %q in details, raw: %q", label, details)
		}
	}
}

func TestModelDetailsWrapsLongBodyTextInsteadOfTruncating(t *testing.T) {
	longRationale := "PR was opened on the deprecated repo. Maintainer already asked contributor to move it to the new home a month ago. No response and no equivalent PR exists in the new repo."
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "title",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceHigh,
		Rationale:    longRationale,
		DraftComment: "short draft",
	}})
	m.width = 80
	m.height = 60 // plenty of room

	// Render details box and rebuild paragraph text from the wrapped
	// inner lines. The full rationale should appear after rejoining.
	view := stripANSI(m.View())
	if strings.Contains(view, "…") {
		t.Fatalf("body text should not be truncated mid-line with …, got:\n%s", view)
	}
	innerLines := extractDetailsBodyLines(view)
	joined := strings.TrimSpace(strings.Join(innerLines, " "))
	joined = strings.Join(strings.Fields(joined), " ") // collapse multi-spaces
	if !strings.Contains(joined, longRationale) {
		t.Fatalf("expected full rationale text to be present (wrapped). joined=%q\nview=\n%s", joined, view)
	}
}

func TestDownScrollsOverflowingCardBeforeMovingInboxCursor(t *testing.T) {
	m := NewModel([]Entry{
		{
			RepoID:       "acme/widgets",
			Number:       1,
			Kind:         sharedtypes.ItemKindIssue,
			Title:        "long draft",
			Rationale:    "rationale",
			DraftComment: strings.Join([]string{"draft line 1", "draft line 2", "draft line 3", "draft line 4", "draft line 5"}, "\n"),
		},
		{
			RepoID:       "acme/widgets",
			Number:       2,
			Kind:         sharedtypes.ItemKindIssue,
			Title:        "second item",
			Rationale:    "second rationale",
			DraftComment: "second draft",
		},
	})
	m.width = 80
	m.height = 8

	initial := stripANSI(m.View())
	if !strings.Contains(initial, "Rationale:") || strings.Contains(initial, "  rationale") {
		t.Fatalf("initial view should show the top of the card and hide lower content, got:\n%s", initial)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)

	if m.cursor != 0 {
		t.Fatalf("down moved inbox cursor to %d, want 0 while card still has hidden content", m.cursor)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "  rationale") || strings.Contains(view, "second item") {
		t.Fatalf("down should scroll the active card instead of moving to the next item, got:\n%s", view)
	}
}

func TestCardScrollResetsWhenCurrentCardChangesAfterRemoval(t *testing.T) {
	m := NewModel([]Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Kind: sharedtypes.ItemKindIssue, Title: "one", Rationale: "r", DraftComment: strings.Join([]string{"one draft 1", "one draft 2", "one draft 3"}, "\n")},
		{RecommendationID: "rec-2", RepoID: "acme/widgets", Number: 2, Kind: sharedtypes.ItemKindIssue, Title: "two", Rationale: "r", DraftComment: strings.Join([]string{"two draft 1", "two draft 2", "two draft 3"}, "\n")},
	})
	m.width = 80
	m.height = 8
	m.cardScroll = 2

	m.removeEntries([]int{0})

	if m.cardScroll != 0 {
		t.Fatalf("cardScroll after removing current card = %d, want 0", m.cardScroll)
	}
}

func TestCardScrollResetsWhenCurrentCardIsReplaced(t *testing.T) {
	m := NewModel([]Entry{{
		RecommendationID: "rec-1",
		RepoID:           "acme/widgets",
		Number:           1,
		Kind:             sharedtypes.ItemKindIssue,
		Title:            "old",
		Rationale:        "old rationale",
		DraftComment:     strings.Join([]string{"old draft 1", "old draft 2", "old draft 3"}, "\n"),
	}})
	m.width = 80
	m.height = 8
	m.cardScroll = 2

	m.applyActionFinished(actionFinishedMsg{
		verb:             "rerun",
		recommendationID: "rec-1",
		updatedEntries: []Entry{{
			RecommendationID: "rec-1",
			RepoID:           "acme/widgets",
			Number:           1,
			Kind:             sharedtypes.ItemKindIssue,
			Title:            "new",
			Rationale:        "new rationale",
			DraftComment:     strings.Join([]string{"new draft 1", "new draft 2", "new draft 3"}, "\n"),
		}},
	})

	if m.cardScroll != 0 {
		t.Fatalf("cardScroll after replacing current card = %d, want 0", m.cardScroll)
	}
}

func TestCardMaxScrollDoesNotReserveHintLineWhenCardExactlyFits(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       1,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "title",
		Rationale:    "rationale",
		DraftComment: "draft",
	}})
	m.width = 80
	m.height = 20
	boxHeight := len(m.cardWrappedLines(80)) + 2

	if got := m.cardMaxScroll(boxHeight); got != 0 {
		t.Fatalf("cardMaxScroll for exactly fitting card = %d, want 0", got)
	}
}

// extractDetailsBodyLines returns the body lines of the focused-item card,
// with `│ ` and trailing ` │` borders stripped. The card title carries the
// repo+item identifier so we look for the first ╭ that's followed by a
// repo-style "owner/name" segment.
func extractDetailsBodyLines(view string) []string {
	var out []string
	inCard := false
	for _, line := range strings.Split(view, "\n") {
		if !inCard && strings.Contains(line, "╭") && strings.Contains(line, "/") {
			inCard = true
			continue
		}
		if inCard && strings.Contains(line, "╰") {
			break
		}
		if !inCard {
			continue
		}
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "│")
		trimmed = strings.TrimSuffix(trimmed, "│")
		out = append(out, trimmed)
	}
	return out
}

func TestModelDetailsURLNotBrokenInWrap(t *testing.T) {
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       42,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "t",
		StateChange:  sharedtypes.StateChangeNone,
		Confidence:   sharedtypes.ConfidenceHigh,
		Rationale:    "r",
		DraftComment: "d",
		URL:          "https://github.com/kunchenguid/example-with-a-fairly-long-path/pull/9999",
	}})
	m.width = 80
	m.height = 60

	view := stripANSI(m.View())
	if !strings.Contains(view, "https://github.com/kunchenguid/example-with-a-fairly-long-path/pull/9999") {
		t.Fatalf("URL should appear unbroken in details, got:\n%s", view)
	}
}

func TestWrapLinePreservesIndent(t *testing.T) {
	got := wrapLine("  this is a fairly long body line that should wrap", 20)
	want := []string{"  this is a fairly", "  long body line", "  that should wrap"}
	if len(got) != len(want) {
		t.Fatalf("wrapLine line count = %d, want %d (got %#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrapLine[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWrapLineHardBreaksLongTokensToProtectLayout pins the regression where
// a single unbreakable token (regex, URL, code identifier) longer than the
// box width was emitted whole and spilled into the adjacent panel. Layout
// integrity is more important than keeping the token contiguous - if the
// token doesn't fit, it must hard-wrap at the width boundary.
func TestWrapLineHardBreaksLongTokensToProtectLayout(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"regex with no spaces", "  " + `(/^(async\s*)?(\(.*?\)\s*=>|[a-zA-Z_$][a-zA-Z0-9_$]*\s*=>|function[\s*(]/).test(trimmed))`},
		{"long url", "  https://example.com/very/long/path/that/exceeds-the-width-of-the-box"},
		{"runs of identifier characters", "  superCalifragilisticExpialidociousSuperCalifragilisticExpialidocious"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapLine(tc.line, 20)
			for i, line := range got {
				if w := lipgloss.Width(line); w > 20 {
					t.Fatalf("wrapped line[%d] = %q has width %d, exceeds 20", i, line, w)
				}
			}
			// Reconstruct the original token (minus continuation indents)
			// to confirm no characters were dropped.
			var rebuilt strings.Builder
			for _, line := range got {
				rebuilt.WriteString(strings.TrimLeft(line, " \t"))
			}
			trimmedInput := strings.TrimLeft(tc.line, " \t")
			if rebuilt.String() != trimmedInput {
				t.Fatalf("rebuilt %q, want %q", rebuilt.String(), trimmedInput)
			}
		})
	}
}

// TestRenderCardLongRegexDoesNotBleedIntoNeighbor pins the higher-level
// regression: a long regex inside the card body must not produce a rendered
// line wider than the card box (which would bleed into the rail).
func TestRenderCardLongRegexDoesNotBleedIntoNeighbor(t *testing.T) {
	rationale := "Bug repro: " + `(/^(async\s*)?(\(.*?\)\s*=>|[a-zA-Z_$][a-zA-Z0-9_$]*\s*=>|function[\s*(]/).test(trimmed))` + " - regex matches the IIFE wrap."
	m := NewModel([]Entry{{
		RepoID:       "acme/widgets",
		Number:       1,
		Kind:         sharedtypes.ItemKindIssue,
		Title:        "long regex inside rationale",
		Rationale:    rationale,
		DraftComment: "ok",
		AgeLabel:     "1m",
	}})
	const cardWidth = 50
	rendered := stripANSI(m.renderCard(cardWidth, 30))
	for i, line := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(line); w > cardWidth {
			t.Fatalf("renderCard line[%d] = %q has width %d, exceeds card width %d (would bleed into adjacent panel)", i, line, w, cardWidth)
		}
	}
}

func TestWaitForNotificationReturnsReloadMsg(t *testing.T) {
	notify := make(chan struct{}, 1)
	notify <- struct{}{}

	msg := waitForNotification(notify)()
	if _, ok := msg.(notifyReloadMsg); !ok {
		t.Fatalf("waitForNotification() message = %T, want notifyReloadMsg", msg)
	}
}

type errReloadFailed struct{}

func (errReloadFailed) Error() string { return "reload failed" }

func TestRoleFilterCyclingNarrowsInbox(t *testing.T) {
	entries := []Entry{
		{RecommendationID: "rec-1", RepoID: "acme/widgets", Number: 1, Role: sharedtypes.RoleMaintainer, Title: "maintainer item", AgeLabel: "1m"},
		{RecommendationID: "rec-2", RepoID: "upstream/widgets", Number: 99, Role: sharedtypes.RoleContributor, Title: "contributor PR", AgeLabel: "2m"},
	}
	m := NewModel(entries)
	if got := len(m.entries); got != 2 {
		t.Fatalf("initial entries = %d, want 2", got)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	mm := updated.(Model)
	if mm.roleFilter != RoleFilterMaintainer {
		t.Fatalf("after first F roleFilter = %v, want maintainer", mm.roleFilter)
	}
	if len(mm.entries) != 1 || mm.entries[0].Role != sharedtypes.RoleMaintainer {
		t.Fatalf("maintainer filter did not narrow: %#v", mm.entries)
	}

	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	mm = updated.(Model)
	if mm.roleFilter != RoleFilterContributor {
		t.Fatalf("after second F roleFilter = %v, want contributor", mm.roleFilter)
	}
	if len(mm.entries) != 1 || mm.entries[0].Role != sharedtypes.RoleContributor {
		t.Fatalf("contributor filter did not narrow: %#v", mm.entries)
	}

	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	mm = updated.(Model)
	if mm.roleFilter != RoleFilterAll {
		t.Fatalf("after third F roleFilter = %v, want all", mm.roleFilter)
	}
	if len(mm.entries) != 2 {
		t.Fatalf("all filter did not restore: %#v", mm.entries)
	}
}

func TestCardTitleShowsContribBadgeForContributorEntry(t *testing.T) {
	contrib := Entry{RepoID: "upstream/widgets", Number: 99, Role: sharedtypes.RoleContributor, Kind: sharedtypes.ItemKindPR}
	if got := cardTitle(contrib); !strings.Contains(got, "contrib") {
		t.Fatalf("cardTitle() = %q, want to contain contrib", got)
	}
	maint := Entry{RepoID: "acme/widgets", Number: 1, Role: sharedtypes.RoleMaintainer, Kind: sharedtypes.ItemKindIssue}
	if got := cardTitle(maint); strings.Contains(got, "contrib") {
		t.Fatalf("cardTitle() = %q, must not include contrib for maintainer", got)
	}
}
