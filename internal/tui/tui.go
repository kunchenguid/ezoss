package tui

import (
	"fmt"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
	"github.com/muesli/termenv"
)

const (
	ansiRed         = "1"
	ansiGreen       = "2"
	ansiYellow      = "3"
	ansiBlue        = "4"
	ansiCyan        = "6"
	ansiBrightBlack = "8"
)

func init() {
	lipgloss.SetColorProfile(termenv.ANSI)
}

// EntryOption is one self-contained resolution the agent proposed. An
// Entry has at least one option; the agent is encouraged to surface
// multiple options whenever there are multiple reasonable next steps.
// The user cycles between options and approves, edits, fixes, or marks one of them triaged.
type EntryOption struct {
	ID                     string
	StateChange            sharedtypes.StateChange
	OriginalStateChange    sharedtypes.StateChange
	ProposedLabels         []string
	OriginalProposedLabels []string
	Confidence             sharedtypes.Confidence
	Rationale              string
	DraftComment           string
	FixPrompt              string
	OriginalDraftComment   string
	Followups              []string
	WaitingOn              sharedtypes.WaitingOn
}

func (o EntryOption) Edited() bool {
	if o.DraftComment != o.OriginalDraftComment {
		return true
	}
	if o.StateChange != o.OriginalStateChange {
		return true
	}
	if len(o.ProposedLabels) != len(o.OriginalProposedLabels) {
		return true
	}
	for i := range o.ProposedLabels {
		if o.ProposedLabels[i] != o.OriginalProposedLabels[i] {
			return true
		}
	}
	return false
}

type Entry struct {
	RecommendationID  string
	RepoID            string
	Number            int
	Kind              sharedtypes.ItemKind
	Role              sharedtypes.Role
	Author            string
	Unconfigured      bool
	Title             string
	URL               string
	TokensIn          int
	TokensOut         int
	AgeLabel          string
	SelectionMarker   string
	ApprovalError     string
	CurrentWaitingOn  sharedtypes.WaitingOn
	RerunInstructions string
	FixJobID          string
	FixStatus         string
	FixPhase          string
	FixMessage        string
	FixError          string
	FixPRURL          string
	FixWorktreePath   string

	Options      []EntryOption
	ActiveOption int

	// Mirror of the active option for render convenience. Always kept
	// in sync via SyncActive(); CommitEdits() writes mirror back to
	// Options[ActiveOption].
	OptionID               string
	StateChange            sharedtypes.StateChange
	OriginalStateChange    sharedtypes.StateChange
	ProposedLabels         []string
	OriginalProposedLabels []string
	Confidence             sharedtypes.Confidence
	Rationale              string
	DraftComment           string
	FixPrompt              string
	OriginalDraftComment   string
	Followups              []string
	WaitingOn              sharedtypes.WaitingOn
}

// SyncActive copies the ActiveOption's fields onto the Entry's mirror
// fields. Callers should invoke this after constructing an Entry or
// changing ActiveOption.
func (e *Entry) SyncActive() {
	if e.ActiveOption < 0 || e.ActiveOption >= len(e.Options) {
		return
	}
	o := e.Options[e.ActiveOption]
	e.OptionID = o.ID
	e.StateChange = o.StateChange
	e.OriginalStateChange = o.OriginalStateChange
	e.ProposedLabels = append([]string(nil), o.ProposedLabels...)
	e.OriginalProposedLabels = append([]string(nil), o.OriginalProposedLabels...)
	e.Confidence = o.Confidence
	e.Rationale = o.Rationale
	e.DraftComment = o.DraftComment
	e.FixPrompt = o.FixPrompt
	e.OriginalDraftComment = o.OriginalDraftComment
	e.Followups = append([]string(nil), o.Followups...)
	e.WaitingOn = o.WaitingOn
}

// CommitEdits writes the mirrored editable fields back to the active
// option so cycling away and back retains in-progress edits.
func (e *Entry) CommitEdits() {
	if e.ActiveOption < 0 || e.ActiveOption >= len(e.Options) {
		return
	}
	o := &e.Options[e.ActiveOption]
	o.StateChange = e.StateChange
	o.ProposedLabels = append([]string(nil), e.ProposedLabels...)
	o.DraftComment = e.DraftComment
	o.FixPrompt = e.FixPrompt
}

func (e Entry) Edited() bool {
	if e.ActiveOption >= 0 && e.ActiveOption < len(e.Options) {
		return e.Options[e.ActiveOption].Edited()
	}
	// Legacy fallback: when callers haven't populated Options, fall
	// back to comparing the mirrored fields directly. Production code
	// always populates Options via loadInboxEntries.
	if e.DraftComment != e.OriginalDraftComment {
		return true
	}
	if e.StateChange != e.OriginalStateChange {
		return true
	}
	if len(e.ProposedLabels) != len(e.OriginalProposedLabels) {
		return true
	}
	for i := range e.ProposedLabels {
		if e.ProposedLabels[i] != e.OriginalProposedLabels[i] {
			return true
		}
	}
	return false
}

type ModelActions struct {
	Approve func([]Entry) error
	Dismiss func([]Entry) error
	// Edit performs an in-process update of the entry. Suitable for test
	// stubs and other synchronous edits that don't need raw terminal
	// access. For external editors (e.g. $EDITOR), use EditExec instead -
	// it routes the cmd through tea.ExecProcess so bubbletea releases the
	// alt screen during the edit and restores it on exit.
	Edit func(Entry) (Entry, error)
	// EditExec launches an external command (typically $EDITOR) via
	// tea.ExecProcess. The terminal is released for the duration of the
	// cmd, then restored, then finish is invoked with any cmd error to
	// produce the updated entry. Takes precedence over Edit when set.
	EditExec      func(Entry) (cmd *exec.Cmd, finish func(execErr error) (Entry, error), err error)
	InitialStatus string
	Notify        <-chan struct{}
	Reload        func() ([]Entry, error)
	Rerun         func([]Entry, string) ([]Entry, error)
	CopyPrompt    func(Entry) error
	Fix           func(Entry) error
	OpenURL       func(Entry) error
}

// RoleFilter narrows the inbox to one role. Cycled with the "F" key.
type RoleFilter int

const (
	RoleFilterAll RoleFilter = iota
	RoleFilterMaintainer
	RoleFilterContributor
)

type Model struct {
	entries    []Entry
	allEntries []Entry
	roleFilter RoleFilter
	cursor     int
	cardScroll int
	width      int
	height     int
	approve    func([]Entry) error
	dismiss    func([]Entry) error
	edit       func(Entry) (Entry, error)
	editExec   func(Entry) (*exec.Cmd, func(error) (Entry, error), error)
	notify     <-chan struct{}
	reload     func() ([]Entry, error)
	rerun      func([]Entry, string) ([]Entry, error)
	copyPrompt func(Entry) error
	fix        func(Entry) error
	openURL    func(Entry) error
	showHelp   bool
	quitting   bool
	rerunInput *rerunInputState

	// Async-action state. Approve/mark-triaged/rerun/fix run in a goroutine via tea.Cmd
	// so the event loop stays responsive; until the goroutine reports back
	// via actionFinishedMsg, the entry is "pending" and conflicting key
	// presses on the same entry are blocked with a warning in the log.
	pendingActions map[string]pendingAction
	logEntries     []logEntry
	logSeq         int
	spinnerFrame   int
	// quitArmed is set to true when the user pressed q while actions were
	// in flight. The next q quits; any other key disarms.
	quitArmed bool
}

type rerunInputState struct {
	entry Entry
	input textarea.Model
}

// pendingAction tracks a verb that's running in the background for one
// recommendation. The TUI uses this to: (1) animate a spinner, (2) refuse
// a duplicate keystroke for the same entry, (3) drive the log panel.
type pendingAction struct {
	verb      string
	startedAt time.Time
}

// logEntry is one row in the rolling log panel under the card. Pending
// rows show a spinner and elapsed time; done/failed rows are static.
type logEntry struct {
	id               int
	state            logState
	verb             string
	recommendationID string
	repoID           string
	number           int
	startedAt        time.Time
	finishedAt       time.Time
	err              error
	note             string // free-form text used by logStateInfo
}

type logState int

const (
	logStatePending logState = iota
	logStateDone
	logStateFailed
	logStateInfo // transient warnings, e.g., "still approving #9 - wait"
)

const (
	maxLogEntries     = 6
	maxLogPanelLines  = 5
	spinnerTickPeriod = 80 * time.Millisecond
)

// spinnerFrames is the braille rotation used while an action is in flight.
// Falls back gracefully to a single dot if the terminal can't render it.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// asyncVerbForms maps the internal action verb to its present-participle
// and past forms for use in log lines and the action-bar morph. Keep this
// aligned with the async action keys understood by Update (a/f/m/r).
var asyncVerbForms = map[string]struct {
	ing string
	ed  string
}{
	"approve": {"approving", "approved"},
	"fix":     {"queueing fix", "queued fix"},
	"mark":    {"marking triaged", "marked triaged"},
	"rerun":   {"rerunning", "reran"},
}

type refreshTickMsg struct{}

type notifyReloadMsg struct{}

type reloadedEntriesMsg struct {
	Entries []Entry
	Err     error
}

// editFinishedMsg is emitted by tea.ExecProcess after an external editor
// exits. The entry is matched by recommendationID rather than cursor index
// because reloads during the edit may have shifted the queue.
type editFinishedMsg struct {
	recommendationID string
	finish           func(error) (Entry, error)
	execErr          error
}

// actionFinishedMsg is delivered when an async approve/mark/rerun/fix
// completes. The entry is matched by recommendationID so reloads or
// reorderings during the action don't clobber the wrong row.
type actionFinishedMsg struct {
	verb             string
	recommendationID string
	err              error
	updatedEntries   []Entry // non-nil for rerun; replaces the entry in-place on success
}

type copyPromptFinishedMsg struct {
	repoID string
	number int
	err    error
}

type openURLFinishedMsg struct {
	repoID string
	number int
	url    string
	err    error
}

// spinnerTickMsg drives spinner animation while at least one action is
// pending. The handler advances the frame and reschedules itself; once
// pendingActions is empty, ticks stop.
type spinnerTickMsg struct{}

const refreshInterval = 5 * time.Second

func NewModel(entries []Entry) Model {
	cloned := append([]Entry(nil), entries...)
	all := append([]Entry(nil), entries...)
	return Model{entries: cloned, allEntries: all, width: 100}
}

func NewModelWithActions(entries []Entry, actions ModelActions) Model {
	m := NewModel(entries)
	m.approve = actions.Approve
	m.dismiss = actions.Dismiss
	m.edit = actions.Edit
	m.editExec = actions.EditExec
	if initial := strings.TrimSpace(actions.InitialStatus); initial != "" {
		m.pushLog(logEntry{state: logStateInfo, note: initial})
	}
	m.notify = actions.Notify
	m.reload = actions.Reload
	m.rerun = actions.Rerun
	m.copyPrompt = actions.CopyPrompt
	m.fix = actions.Fix
	m.openURL = actions.OpenURL
	return m
}

func NewModelWithDismiss(entries []Entry, dismiss func([]Entry) error) Model {
	return NewModelWithActions(entries, ModelActions{Dismiss: dismiss})
}

func Run(entries []Entry) error {
	p := tea.NewProgram(NewModel(entries), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func RunWithDismiss(entries []Entry, dismiss func([]Entry) error) error {
	p := tea.NewProgram(NewModelWithDismiss(entries, dismiss), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func RunWithActions(entries []Entry, actions ModelActions) error {
	p := tea.NewProgram(NewModelWithActions(entries, actions), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 2)
	if m.reload != nil {
		cmds = append(cmds, scheduleRefresh())
	}
	if m.notify != nil {
		cmds = append(cmds, waitForNotification(m.notify))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshTickMsg:
		if m.reload == nil {
			return m, nil
		}
		return m, tea.Batch(runReload(m.reload), scheduleRefresh())
	case notifyReloadMsg:
		if m.reload == nil {
			return m, waitForNotification(m.notify)
		}
		return m, tea.Batch(runReload(m.reload), waitForNotification(m.notify))
	case reloadedEntriesMsg:
		if msg.Err != nil {
			m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("refresh failed: %v", msg.Err)})
			return m, nil
		}
		m.applyReload(msg.Entries)
		return m, nil
	case editFinishedMsg:
		m.applyEditFinished(msg)
		return m, nil
	case actionFinishedMsg:
		m.applyActionFinished(msg)
		return m, nil
	case copyPromptFinishedMsg:
		if msg.err != nil {
			m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("copy prompt failed: %v", msg.err)})
			return m, nil
		}
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("copied prompt for %s #%d", msg.repoID, msg.number)})
		return m, nil
	case openURLFinishedMsg:
		if msg.err != nil {
			m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("open url failed: %v", msg.err)})
			return m, nil
		}
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("opened %s #%d", msg.repoID, msg.number)})
		return m, nil
	case spinnerTickMsg:
		m.spinnerFrame++
		if len(m.pendingActions) > 0 {
			return m, scheduleSpinnerTick()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		key := msg.String()
		if m.rerunInput != nil {
			if cmd := m.updateRerunInput(msg); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
		// Any non-q key disarms the quit-confirm latch.
		if key != "q" && key != "ctrl+c" {
			m.quitArmed = false
		}
		switch key {
		case "q", "ctrl+c":
			if len(m.pendingActions) > 0 && !m.quitArmed && key != "ctrl+c" {
				m.quitArmed = true
				m.pushLog(logEntry{
					state: logStateInfo,
					note:  "quit while actions pending - press q again to confirm",
				})
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "?":
			m.showHelp = !m.showHelp
		case "a":
			if cmd := m.approveCurrent(); cmd != nil {
				return m, withSpinner(cmd, m.kickSpinnerIfPending())
			}
		case "e":
			if cmd := m.editCurrent(); cmd != nil {
				return m, cmd
			}
		case "c":
			if cmd := m.copyPromptCurrent(); cmd != nil {
				return m, cmd
			}
		case "o":
			if cmd := m.openURLCurrent(); cmd != nil {
				return m, cmd
			}
		case "f":
			if cmd := m.fixCurrent(); cmd != nil {
				return m, withSpinner(cmd, m.kickSpinnerIfPending())
			}
		case "F":
			m.cycleRoleFilter()
		case "r":
			m.openRerunInput()
		case "m":
			if cmd := m.dismissCurrent(); cmd != nil {
				return m, withSpinner(cmd, m.kickSpinnerIfPending())
			}
		case "down":
			m.scrollCard(1)
		case "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
				m.cardScroll = 0
			}
		case "up":
			m.scrollCard(-1)
		case "k":
			if m.cursor > 0 {
				m.cursor--
				m.cardScroll = 0
			}
		case "tab":
			m.cycleOption(1)
		case "shift+tab":
			m.cycleOption(-1)
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			m.jumpToOption(int(key[0] - '1'))
		}
	}
	return m, nil
}

// currentEntries returns the cursor's entry as a single-element slice (and
// its index), used to drive actions on the focused item.
func (m *Model) currentEntries() ([]Entry, []int) {
	if len(m.entries) == 0 {
		return nil, nil
	}
	return []Entry{m.entries[m.cursor]}, []int{m.cursor}
}

// cycleOption shifts the cursor entry's active option by delta, wrapping
// at the ends, and re-syncs the entry's mirrored fields.
func (m *Model) cycleOption(delta int) {
	if len(m.entries) == 0 {
		return
	}
	entry := &m.entries[m.cursor]
	if len(entry.Options) <= 1 {
		return
	}
	entry.CommitEdits()
	idx := entry.ActiveOption + delta
	n := len(entry.Options)
	idx = ((idx % n) + n) % n
	entry.ActiveOption = idx
	entry.SyncActive()
	m.replaceAllEntry(*entry)
	m.cardScroll = 0
}

// jumpToOption switches the cursor entry's active option to idx (0-based).
// Out-of-range indices are ignored.
func (m *Model) jumpToOption(idx int) {
	if len(m.entries) == 0 {
		return
	}
	entry := &m.entries[m.cursor]
	if idx < 0 || idx >= len(entry.Options) {
		return
	}
	if idx == entry.ActiveOption {
		return
	}
	entry.CommitEdits()
	entry.ActiveOption = idx
	entry.SyncActive()
	m.replaceAllEntry(*entry)
	m.cardScroll = 0
}

func (m *Model) scrollCard(delta int) bool {
	if delta == 0 || len(m.entries) == 0 {
		return false
	}
	_, boxHeight := m.cardRenderSize()
	maxScroll := m.cardMaxScroll(boxHeight)
	if maxScroll <= 0 {
		m.cardScroll = 0
		return false
	}
	next := m.cardScroll + delta
	if next < 0 {
		next = 0
	}
	if next > maxScroll {
		next = maxScroll
	}
	if next == m.cardScroll {
		return false
	}
	m.cardScroll = next
	return true
}

func (m *Model) approveCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	m.entries[m.cursor].CommitEdits()
	entry := m.entries[m.cursor]
	if cmd, blocked := m.guardConflict(entry, "approve"); blocked {
		return cmd
	}
	if m.approve == nil {
		// No async work to schedule; mirror the old synchronous path so
		// tests that don't wire Approve still see the immediate state.
		m.removeEntries([]int{m.cursor})
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("approved %s #%d", entry.RepoID, entry.Number)})
		return nil
	}
	cmd := m.startAction(entry, "approve", func() tea.Msg {
		return actionFinishedMsg{
			verb:             "approve",
			recommendationID: entry.RecommendationID,
			err:              m.approve([]Entry{entry}),
		}
	})
	m.advanceCursorPastPending()
	return cmd
}

func (m *Model) editCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	entry := m.entries[m.cursor]
	if cmd, blocked := m.guardConflict(entry, "edit"); blocked {
		return cmd
	}
	if m.editExec != nil {
		recommendationID := m.entries[m.cursor].RecommendationID
		cmd, finish, err := m.editExec(m.entries[m.cursor])
		if err != nil {
			m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("edit failed: %v", err)})
			return nil
		}
		// EditExec is the bubbletea-friendly path: ExecProcess releases
		// the alt screen for the editor and restores it on exit, so the
		// terminal isn't left half-redrawn when the user returns.
		return tea.ExecProcess(cmd, func(execErr error) tea.Msg {
			return editFinishedMsg{recommendationID: recommendationID, finish: finish, execErr: execErr}
		})
	}
	if m.edit == nil {
		m.pushLog(logEntry{state: logStateInfo, note: "edit unavailable"})
		return nil
	}
	updated, err := m.edit(m.entries[m.cursor])
	if err != nil {
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("edit failed: %v", err)})
		return nil
	}
	updated.CommitEdits()
	m.entries[m.cursor] = updated
	m.replaceAllEntry(updated)
	m.cardScroll = 0
	m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("edited recommendation for %s #%d", updated.RepoID, updated.Number)})
	return nil
}

func (m *Model) copyPromptCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	entry := m.entries[m.cursor]
	if _, blocked := m.guardConflict(entry, "copy prompt"); blocked {
		return nil
	}
	if strings.TrimSpace(entry.FixPrompt) == "" {
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("no fix prompt for %s #%d", entry.RepoID, entry.Number)})
		return nil
	}
	if m.copyPrompt == nil {
		m.pushLog(logEntry{state: logStateInfo, note: "copy prompt unavailable"})
		return nil
	}
	copyPrompt := m.copyPrompt
	return func() tea.Msg {
		return copyPromptFinishedMsg{
			repoID: entry.RepoID,
			number: entry.Number,
			err:    copyPrompt(entry),
		}
	}
}

func (m *Model) openURLCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	entry := m.entries[m.cursor]
	if strings.TrimSpace(entry.URL) == "" {
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("no URL for %s #%d", entry.RepoID, entry.Number)})
		return nil
	}
	if m.openURL == nil {
		m.pushLog(logEntry{state: logStateInfo, note: "open url unavailable"})
		return nil
	}
	openURL := m.openURL
	return func() tea.Msg {
		return openURLFinishedMsg{
			repoID: entry.RepoID,
			number: entry.Number,
			url:    entry.URL,
			err:    openURL(entry),
		}
	}
}

func (m *Model) fixCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	entry := m.entries[m.cursor]
	if cmd, blocked := m.guardConflict(entry, "fix"); blocked {
		return cmd
	}
	if strings.TrimSpace(entry.FixPrompt) == "" {
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("no fix prompt for %s #%d", entry.RepoID, entry.Number)})
		return nil
	}
	if m.fix == nil {
		m.pushLog(logEntry{state: logStateInfo, note: "fix unavailable"})
		return nil
	}
	return m.startAction(entry, "fix", func() tea.Msg {
		return actionFinishedMsg{
			verb:             "fix",
			recommendationID: entry.RecommendationID,
			err:              m.fix(entry),
		}
	})
}

// guardConflict reports whether an action on entry should be refused
// because something is already pending for that recommendation. When
// blocked it pushes an info entry into the log and returns (nil, true);
// callers should bail out without scheduling work. The verb parameter is
// the requested action - kept for symmetry with the start path even though
// it's not in the warning text.
func (m *Model) guardConflict(entry Entry, _ string) (tea.Cmd, bool) {
	pending, ok := m.pendingActions[entry.RecommendationID]
	if !ok {
		return nil, false
	}
	m.pushLog(logEntry{
		state: logStateInfo,
		note:  fmt.Sprintf("still %s %s #%d - wait for it to finish", verbIng(pending.verb), entry.RepoID, entry.Number),
	})
	return nil, true
}

// startAction registers a pending entry and returns the bare action cmd
// (which runs the user-supplied work in a goroutine and emits
// actionFinishedMsg). Spinner ticking is kicked off by the caller via
// kickSpinnerIfPending - keeping it out of this Cmd avoids tying tests to
// tea.Tick's real wall-clock delay.
func (m *Model) startAction(entry Entry, verb string, run func() tea.Msg) tea.Cmd {
	if m.pendingActions == nil {
		m.pendingActions = make(map[string]pendingAction)
	}
	now := time.Now()
	m.pendingActions[entry.RecommendationID] = pendingAction{verb: verb, startedAt: now}
	m.pushLog(logEntry{
		state:            logStatePending,
		verb:             verb,
		recommendationID: entry.RecommendationID,
		repoID:           entry.RepoID,
		number:           entry.Number,
		startedAt:        now,
	})
	return func() tea.Msg { return run() }
}

// kickSpinnerIfPending returns a fresh spinner-tick cmd if at least one
// action is in flight. The tick handler reschedules itself until pending
// is empty, so this only needs to fire once per "newly busy" transition.
func (m *Model) kickSpinnerIfPending() tea.Cmd {
	if len(m.pendingActions) == 0 {
		return nil
	}
	return scheduleSpinnerTick()
}

func (m *Model) pushLog(e logEntry) {
	m.logSeq++
	e.id = m.logSeq
	m.logEntries = append(m.logEntries, e)
	if len(m.logEntries) > maxLogEntries {
		m.logEntries = m.logEntries[len(m.logEntries)-maxLogEntries:]
	}
}

// finishLog flips the most recent matching pending entry to done or failed.
// Matching by (recommendationID, verb) handles the rare case where two
// different verbs were attempted on the same item across the queue.
func (m *Model) finishLog(rid, verb string, err error) {
	for i := len(m.logEntries) - 1; i >= 0; i-- {
		e := &m.logEntries[i]
		if e.state == logStatePending && e.recommendationID == rid && e.verb == verb {
			e.finishedAt = time.Now()
			if err != nil {
				e.state = logStateFailed
				e.err = err
			} else {
				e.state = logStateDone
			}
			return
		}
	}
}

func (m *Model) applyActionFinished(msg actionFinishedMsg) {
	delete(m.pendingActions, msg.recommendationID)
	m.finishLog(msg.recommendationID, msg.verb, msg.err)

	if msg.err != nil {
		// finishLog already recorded the failure with the verb/repo/number;
		// no need to duplicate it elsewhere.
		return
	}

	idx := -1
	for i := range m.entries {
		if m.entries[i].RecommendationID == msg.recommendationID {
			idx = i
			break
		}
	}
	switch msg.verb {
	case "approve", "mark":
		if idx < 0 {
			m.removeAllEntriesByID(map[string]struct{}{msg.recommendationID: {}})
			return
		}
		if idx >= 0 {
			m.removeEntries([]int{idx})
		}
	case "rerun":
		if len(msg.updatedEntries) == 0 {
			return
		}
		updated := msg.updatedEntries[0]
		if idx >= 0 {
			m.entries[idx] = updated
			if idx == m.cursor {
				m.cardScroll = 0
			}
		}
		m.replaceAllEntry(updated, msg.recommendationID)
	}
}

func verbIng(verb string) string {
	if v, ok := asyncVerbForms[verb]; ok {
		return v.ing
	}
	return verb
}

func verbEd(verb string) string {
	if v, ok := asyncVerbForms[verb]; ok {
		return v.ed
	}
	return verb
}

func scheduleSpinnerTick() tea.Cmd {
	return tea.Tick(spinnerTickPeriod, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// withSpinner batches an action cmd with the spinner-kick cmd. If kick is
// nil (no pending actions), the action cmd is returned as-is so tests can
// drive it without unwrapping a Batch.
func withSpinner(action, kick tea.Cmd) tea.Cmd {
	if kick == nil {
		return action
	}
	return tea.Batch(action, kick)
}

func (m *Model) applyEditFinished(msg editFinishedMsg) {
	if msg.finish == nil {
		return
	}
	idx := -1
	for i := range m.entries {
		if m.entries[i].RecommendationID == msg.recommendationID {
			idx = i
			break
		}
	}
	if idx < 0 && !m.hasAllEntry(msg.recommendationID) {
		m.pushLog(logEntry{state: logStateInfo, note: "edit aborted: entry no longer in queue"})
		return
	}
	updated, err := msg.finish(msg.execErr)
	if err != nil {
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("edit failed: %v", err)})
		return
	}
	updated.CommitEdits()
	if idx >= 0 {
		m.entries[idx] = updated
	}
	m.replaceAllEntry(updated)
	if idx >= 0 && idx == m.cursor {
		m.cardScroll = 0
	}
	m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("edited recommendation for %s #%d", updated.RepoID, updated.Number)})
}

func (m *Model) rerunCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	entry := m.entries[m.cursor]
	return m.rerunEntry(entry, "")
}

func (m *Model) rerunEntry(entry Entry, instructions string) tea.Cmd {
	if cmd, blocked := m.guardConflict(entry, "rerun"); blocked {
		return cmd
	}
	if m.rerun == nil {
		m.pushLog(logEntry{state: logStateInfo, note: "rerun unavailable"})
		return nil
	}
	return m.startAction(entry, "rerun", func() tea.Msg {
		updated, err := m.rerun([]Entry{entry}, instructions)
		return actionFinishedMsg{
			verb:             "rerun",
			recommendationID: entry.RecommendationID,
			err:              err,
			updatedEntries:   updated,
		}
	})
}

func (m *Model) openRerunInput() {
	if len(m.entries) == 0 {
		return
	}
	entry := m.entries[m.cursor]
	if _, blocked := m.guardConflict(entry, "rerun"); blocked {
		return
	}
	if m.rerun == nil {
		m.pushLog(logEntry{state: logStateInfo, note: "rerun unavailable"})
		return
	}
	input := textarea.New()
	input.Placeholder = "Focus on the maintainer clarification, CI failure, security risk, or merge readiness."
	input.ShowLineNumbers = false
	input.Prompt = "> "
	input.SetWidth(m.rerunInputWidth())
	input.SetHeight(5)
	input.Focus()
	m.rerunInput = &rerunInputState{entry: entry, input: input}
}

func (m *Model) updateRerunInput(msg tea.KeyMsg) tea.Cmd {
	if m.rerunInput == nil {
		return nil
	}
	switch msg.String() {
	case "esc":
		m.rerunInput = nil
		return nil
	case "ctrl+c":
		m.quitting = true
		return tea.Quit
	case "ctrl+r", "ctrl+enter":
		state := m.rerunInput
		m.rerunInput = nil
		cmd := m.rerunEntry(state.entry, state.input.Value())
		return withSpinner(cmd, m.kickSpinnerIfPending())
	}
	input, cmd := m.rerunInput.input.Update(msg)
	m.rerunInput.input = input
	return cmd
}

func (m Model) rerunInputWidth() int {
	width := m.width
	if width < 80 {
		width = 80
	}
	contentWidth := width - 8
	if contentWidth < 40 {
		contentWidth = 40
	}
	return contentWidth
}

const (
	responsiveLayoutMinWidth  = 110
	responsiveLayoutMinHeight = 20
	responsiveLayoutGap       = 2

	// Rail (left) is the narrower context column; card (right) is the
	// focal column and always gets the bigger share so the eye lands there.
	responsiveRailMinWidth = 40
	responsiveRailMaxWidth = 60
	responsiveCardMinWidth = 60

	compactHeightThreshold = 24
	minInboxContentHeight  = 4
)

// View renders the TUI as a card-focused triage UI:
//
//   - The current item's full context (title, status, rationale, draft,
//     proposed actions) is the focus and lives in a centered card.
//   - On wide layouts a queue rail sits to the left, grouped by repo with
//     dimmed repo headings, providing context without stealing focus.
//   - The Decide bar (a/f/e/m/r/c) sits below the card so the actions are
//     anchored to the item being decided on.
//   - j/k navigate forward/back through the queue; arrow keys only scroll
//     overflowing card content.
//
// Single-item decision flow: every action key operates on the cursor's
// item. There is no batch selection.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	width := m.width
	if width < 80 {
		width = 80
	}

	compact := m.height > 0 && m.height < compactHeightThreshold
	sectionGap := "\n\n"
	gapHeight := 2
	if compact {
		sectionGap = "\n"
		gapHeight = 1
	}

	navBar := appNavBar()

	if len(m.entries) == 0 {
		empty := renderBox(width, "Inbox", metaStyle().Render("No pending recommendations.\nRun `ezoss daemon start` or `ezoss triage <url>` to populate the queue."))
		sections := []string{empty}
		if logPanel := m.renderLogPanel(width); logPanel != "" {
			sections = append(sections, logPanel)
		}
		sections = append(sections, navBar)
		return strings.Join(sections, sectionGap)
	}

	useRail := m.width >= responsiveLayoutMinWidth && m.height >= responsiveLayoutMinHeight && !m.showHelp

	logPanel := m.renderLogPanel(width)
	rerunInput := ""
	rerunInputHeight := 0
	if m.rerunInput != nil {
		rerunInput = m.renderRerunInput(width)
		rerunInputHeight = lipgloss.Height(rerunInput) + gapHeight
	}

	contentBudget := -1
	if m.height > 0 {
		fixed := lipgloss.Height(navBar) + gapHeight + rerunInputHeight
		if m.showHelp {
			fixed += lipgloss.Height(renderBox(width, "Keyboard shortcuts", m.renderHelp())) + gapHeight
		}
		// The log panel is the first thing we'll trim if the terminal is
		// short - card stays at minInboxContentHeight, log shrinks (or
		// vanishes) below that.
		if logPanel != "" {
			logHeight := lipgloss.Height(logPanel) + gapHeight
			available := m.height - fixed - (minInboxContentHeight + 2) - gapHeight
			if available < logHeight {
				logPanel = m.shrinkLogPanel(width, available)
				if logPanel == "" {
					logHeight = 0
				} else {
					logHeight = lipgloss.Height(logPanel) + gapHeight
				}
			}
			fixed += logHeight
		}
		contentBudget = m.height - fixed
		if contentBudget < minInboxContentHeight+2 {
			contentBudget = minInboxContentHeight + 2
		}
	}

	var bodySection string
	if useRail {
		railWidth, cardWidth := responsiveColumnWidths(width)
		rail := m.renderQueueRail(railWidth, contentBudget)
		card := m.renderCard(cardWidth, contentBudget)
		bodySection = renderResponsiveColumns(rail, card, railWidth, cardWidth, responsiveLayoutGap)
	} else {
		bodySection = m.renderCard(width, contentBudget)
	}

	sections := []string{bodySection}
	if rerunInput != "" {
		sections = append(sections, rerunInput)
	}
	if logPanel != "" {
		sections = append(sections, logPanel)
	}
	if m.showHelp {
		sections = append(sections, renderBox(width, "Keyboard shortcuts", m.renderHelp()))
	}
	sections = append(sections, navBar)
	return strings.Join(sections, sectionGap)
}

func (m Model) cardRenderSize() (int, int) {
	width := m.width
	if width < 80 {
		width = 80
	}

	compact := m.height > 0 && m.height < compactHeightThreshold
	gapHeight := 2
	if compact {
		gapHeight = 1
	}

	contentBudget := -1
	if m.height > 0 {
		navBar := appNavBar()
		fixed := lipgloss.Height(navBar) + gapHeight
		if m.rerunInput != nil {
			fixed += lipgloss.Height(m.renderRerunInput(width)) + gapHeight
		}
		if m.showHelp {
			fixed += lipgloss.Height(renderBox(width, "Keyboard shortcuts", m.renderHelp())) + gapHeight
		}
		logPanel := m.renderLogPanel(width)
		if logPanel != "" {
			logHeight := lipgloss.Height(logPanel) + gapHeight
			available := m.height - fixed - (minInboxContentHeight + 2) - gapHeight
			if available < logHeight {
				logPanel = m.shrinkLogPanel(width, available)
				if logPanel == "" {
					logHeight = 0
				} else {
					logHeight = lipgloss.Height(logPanel) + gapHeight
				}
			}
			fixed += logHeight
		}
		contentBudget = m.height - fixed
		if contentBudget < minInboxContentHeight+2 {
			contentBudget = minInboxContentHeight + 2
		}
	}

	useRail := m.width >= responsiveLayoutMinWidth && m.height >= responsiveLayoutMinHeight && !m.showHelp
	if useRail {
		_, cardWidth := responsiveColumnWidths(width)
		return cardWidth, contentBudget
	}
	return width, contentBudget
}

// renderLogPanel returns a single "Activity" box with the last few log
// entries (in-flight, completed, failed, info). Pending rows show a
// braille spinner that advances each tick. Returns empty string if there
// are no entries to show.
//
// Long entries (notably failure errors with embedded gh output) are
// word-wrapped to fit the panel width so they don't bleed past the box
// border - the user needs to see the *whole* error to understand what
// happened.
func (m Model) renderLogPanel(width int) string {
	if len(m.logEntries) == 0 {
		return ""
	}
	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}
	// Show the last few entries, newest at the bottom (most recent action
	// gets the most salient position). Take more than maxLogPanelLines
	// raw entries because each entry may wrap to multiple visual lines;
	// we'll trim the wrapped output below.
	visibleCount := len(m.logEntries)
	if visibleCount > maxLogEntries {
		visibleCount = maxLogEntries
	}
	visible := m.logEntries[len(m.logEntries)-visibleCount:]
	wrapped := make([]string, 0, len(visible))
	for _, e := range visible {
		wrapped = append(wrapped, wrapLines([]string{m.renderLogLine(e)}, contentWidth)...)
	}
	if len(wrapped) > maxLogPanelLines {
		wrapped = wrapped[len(wrapped)-maxLogPanelLines:]
	}
	return renderBox(width, "Activity", strings.Join(wrapped, "\n"))
}

func (m Model) renderRerunInput(width int) string {
	if m.rerunInput == nil {
		return ""
	}
	entry := m.rerunInput.entry
	input := m.rerunInput.input
	input.SetWidth(m.rerunInputWidth())
	body := strings.Join([]string{
		fmt.Sprintf("Add instructions for the agent rerun on %s #%d.", entry.RepoID, entry.Number),
		metaStyle().Render("Used as private context for this rerun. Nothing here is posted to GitHub."),
		"",
		input.View(),
		"",
		actionBarStyle().Render("ctrl+r rerun   enter newline   esc cancel"),
	}, "\n")
	return renderBox(width, "Rerun triage", body)
}

// shrinkLogPanel re-renders the log panel with at most enough content
// lines to fit in the available vertical budget. Returns empty string if
// the budget is too small for even the box chrome (top + bottom border +
// 1 row of content = 3 minimum).
func (m Model) shrinkLogPanel(width, available int) string {
	if available < 3 || len(m.logEntries) == 0 {
		return ""
	}
	contentLines := available - 2 // box top + bottom borders
	if contentLines < 1 {
		return ""
	}
	if contentLines > maxLogPanelLines {
		contentLines = maxLogPanelLines
	}
	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}
	// Wrap from the newest entries backward and stop once we have enough
	// visual lines - older entries fall off the top first.
	var visualLines []string
	for i := len(m.logEntries) - 1; i >= 0 && len(visualLines) < contentLines; i-- {
		entryLines := wrapLines([]string{m.renderLogLine(m.logEntries[i])}, contentWidth)
		// Prepend in reverse so the final order has oldest first within
		// the visible window.
		visualLines = append(entryLines, visualLines...)
	}
	if len(visualLines) > contentLines {
		visualLines = visualLines[len(visualLines)-contentLines:]
	}
	return renderBox(width, "Activity", strings.Join(visualLines, "\n"))
}

func (m Model) renderLogLine(e logEntry) string {
	switch e.state {
	case logStatePending:
		spinner := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
		elapsed := time.Since(e.startedAt).Truncate(time.Second)
		return fmt.Sprintf("%s %s %s #%d · %s", spinner, verbIng(e.verb), e.repoID, e.number, formatLogElapsed(elapsed))
	case logStateDone:
		dur := e.finishedAt.Sub(e.startedAt).Round(100 * time.Millisecond)
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)).Render("✓ ") +
			fmt.Sprintf("%s %s #%d · %s", verbEd(e.verb), e.repoID, e.number, dur)
	case logStateFailed:
		return errorStyle().Render("✗ ") +
			fmt.Sprintf("%s failed %s #%d: %v", e.verb, e.repoID, e.number, e.err)
	case logStateInfo:
		return metaStyle().Render(e.note)
	}
	return ""
}

func formatLogElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return d.String()
}

// renderDecideBar shows the highest-value per-item actions in a single bold
// line. The footer has finite space, so it keeps a stable priority order and
// ends with "? more" instead of trying to expose every binding inline.
//
// When the entry has an action in flight, the bar morphs into a
// spinner+verb so it's obvious that pressing another action key right now is
// a no-op until the action completes.
func renderDecideBar(maxWidth int, optionCount int) string {
	hints := []string{"a approve", "f fix", "e edit", "m mark triaged", "r rerun", "c copy", "o open"}
	if optionCount > 1 {
		hints = append(hints, "tab switch option")
	}
	bar := fitActionHints(hints, "? more", maxWidth)
	return actionBarStyle().Render(bar)
}

func fitActionHints(hints []string, more string, maxWidth int) string {
	const sep = "   "
	if maxWidth <= 0 {
		return strings.Join(append(hints, more), sep)
	}
	selected := make([]string, 0, len(hints)+1)
	for _, hint := range hints {
		candidate := append(append([]string(nil), selected...), hint)
		candidate = append(candidate, more)
		if lipgloss.Width(strings.Join(candidate, sep)) > maxWidth {
			break
		}
		selected = append(selected, hint)
	}
	selected = append(selected, more)
	if lipgloss.Width(strings.Join(selected, sep)) <= maxWidth || len(selected) == 1 {
		return strings.Join(selected, sep)
	}
	return more
}

// renderPendingDecideBar replaces the action keys with a spinner and the
// active verb so the user sees the in-flight state instead of stale hints.
func renderPendingDecideBar(verb string, spinnerFrame int, repoID string, number int, startedAt time.Time) string {
	spinner := spinnerFrames[spinnerFrame%len(spinnerFrames)]
	elapsed := time.Since(startedAt).Truncate(time.Second)
	return actionBarStyle().Render(fmt.Sprintf("%s %s %s #%d · %s", spinner, verbIng(verb), repoID, number, formatLogElapsed(elapsed)))
}

// responsiveColumnWidths splits width into (rail, card). The card is the
// focal element and always gets the larger share - rail tops out at 40% of
// the available width so the body content has room to breathe.
func responsiveColumnWidths(width int) (int, int) {
	railWidth := width * 2 / 5
	if railWidth < responsiveRailMinWidth {
		railWidth = responsiveRailMinWidth
	}
	if railWidth > responsiveRailMaxWidth {
		railWidth = responsiveRailMaxWidth
	}
	cardWidth := width - railWidth - responsiveLayoutGap
	if cardWidth < responsiveCardMinWidth {
		cardWidth = responsiveCardMinWidth
		railWidth = width - cardWidth - responsiveLayoutGap
	}
	return railWidth, cardWidth
}

func renderResponsiveColumns(left, right string, leftWidth, rightWidth, gap int) string {
	if right == "" {
		return left
	}
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	leftStyle := lipgloss.NewStyle().Width(leftWidth)
	rightStyle := lipgloss.NewStyle().Width(rightWidth)
	gapStr := strings.Repeat(" ", gap)
	var b strings.Builder
	for i := 0; i < maxLines; i++ {
		var ll, rl string
		if i < len(leftLines) {
			ll = leftLines[i]
		}
		if i < len(rightLines) {
			rl = rightLines[i]
		}
		b.WriteString(leftStyle.Render(ll))
		b.WriteString(gapStr)
		b.WriteString(rightStyle.Render(rl))
		if i < maxLines-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func appNavBar() string {
	return metaStyle().Render("q quit  ? help")
}

func (m Model) renderHelp() string {
	return strings.Join([]string{
		"j                  next item",
		"k                  previous item",
		"down / up          scroll overflowing card",
		"tab / shift+tab    cycle between alternate recommendations (when present)",
		"1-9                jump directly to that recommendation option",
		"a                  approve active option (queues fix_required jobs first)",
		"c                  copy active option's coding-agent prompt",
		"f                  queue or replace a cancellable coding-agent fix job",
		"e                  edit active option's draft, action, or labels",
		"m                  mark triaged without approving",
		"o                  open the current item's GitHub page in a browser",
		"r                  rerun the agent on the current item with instructions",
		"F                  cycle role filter: all / maintainer / contributor",
		"?                  toggle this help",
		"q                  quit",
	}, "\n")
}

// truncateToWidth shortens s so that lipgloss.Width(result) <= maxWidth.
// width <= 0 returns s unchanged. When truncating, an ellipsis "…" is
// appended to signal the cut.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	runes := []rune(s)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if lipgloss.Width(candidate) <= maxWidth {
			return candidate
		}
	}
	return "…"
}

// formatScrollHint returns a dim-styled scroll indicator suitable for the
// bottom border of a box. Empty when nothing is hidden.
func formatScrollHint(above, below int) string {
	var raw string
	switch {
	case above > 0 && below > 0:
		raw = fmt.Sprintf("↑ %d above  ↓ %d below (j ↓ / k ↑)", above, below)
	case below > 0:
		raw = fmt.Sprintf("↓ %d more (j ↓ / k ↑)", below)
	case above > 0:
		raw = fmt.Sprintf("↑ %d more (j ↓ / k ↑)", above)
	default:
		return ""
	}
	return metaStyle().Render(raw)
}

func formatCardScrollHint(above, below int) string {
	var raw string
	switch {
	case above > 0 && below > 0:
		raw = fmt.Sprintf("↑ %d above  ↓ %d below", above, below)
	case below > 0:
		raw = fmt.Sprintf("↓ %d more lines", below)
	case above > 0:
		raw = fmt.Sprintf("↑ %d previous lines", above)
	default:
		return ""
	}
	return metaStyle().Render(raw)
}

// entryKindGlyph returns a one-cell-wide colored glyph indicating item kind:
// green ○ for issues (mirrors GitHub's open-issue icon), cyan ⇡ for PRs
// (evokes "pull"). Both are width-1 so callers can align around them.
func entryKindGlyph(kind sharedtypes.ItemKind) string {
	if kind == sharedtypes.ItemKindPR {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiCyan)).Render("⇡")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)).Render("○")
}

func entryNumberLabel(entry Entry) string {
	return entryKindGlyph(entry.Kind) + " #" + strconv.Itoa(entry.Number)
}

// entryNumberLabelPadded right-pads the numeric portion to digitWidth so a
// list of mixed-width IDs lines up on the trailing column.
func entryNumberLabelPadded(entry Entry, digitWidth int) string {
	return entryKindGlyph(entry.Kind) + fmt.Sprintf(" #%-*d", digitWidth, entry.Number)
}

// pendingGlyph returns a one-cell-wide marker used in the rail in place
// of the kind glyph while an action is in flight on this item. Static
// (not animated) so the rail stays calm even when several items are busy.
func pendingGlyph() string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiYellow)).Render("…")
}

// entryNumberLabelPaddedWithPending mirrors entryNumberLabelPadded but
// substitutes pendingGlyph() for the kind glyph when the entry is busy.
func entryNumberLabelPaddedWithPending(entry Entry, digitWidth int, pending bool) string {
	glyph := entryKindGlyph(entry.Kind)
	if pending {
		glyph = pendingGlyph()
	}
	return glyph + fmt.Sprintf(" #%-*d", digitWidth, entry.Number)
}

// plainEntryNumberLabelPadded is the no-ANSI counterpart used inside spans
// that need a single uninterrupted background fill.
func plainEntryNumberLabelPadded(entry Entry, digitWidth int, pending bool) string {
	glyph := "○"
	if entry.Kind == sharedtypes.ItemKindPR {
		glyph = "⇡"
	}
	if pending {
		glyph = "…"
	}
	return glyph + fmt.Sprintf(" #%-*d", digitWidth, entry.Number)
}

// plainText is text the caller has affirmed contains no ANSI escape
// sequences. Construction is unexported so the type itself documents the
// contract: pass me only raw strings, not the output of lipgloss styling.
// The point is to make it impossible to accidentally route a pre-styled
// string into renderHighlightRow, where any inner reset (\x1b[0m) would
// cut the background fill mid-row.
type plainText string

// renderHighlightRow paints a full-bleed inbox row: bold text on a
// saturated blue background that runs unbroken from one box border to
// the other. Blue is dark enough that the terminal's default foreground
// reads clearly without us having to pin it (which would clash with
// users' themes). fullBleedWidth must be the row's total width
// including the 1-cell inner pads renderBoxLinesWithFooter would
// otherwise add (i.e. contentWidth + 2). The text is truncated to fit,
// then padded with one leading and one trailing space so the highlight
// visibly clears the borders.
func renderHighlightRow(plain plainText, fullBleedWidth int) string {
	body := " " + truncateToWidth(string(plain), fullBleedWidth-2) + " "
	return lipgloss.NewStyle().
		Bold(true).
		Background(lipgloss.Color(ansiBlue)).
		Width(fullBleedWidth).
		Render(body)
}

func fixJobInProgress(entry Entry) bool {
	if strings.TrimSpace(entry.FixJobID) == "" {
		return false
	}
	switch strings.TrimSpace(entry.FixStatus) {
	case "queued", "running":
		return true
	default:
		return false
	}
}

// maxNumberDigits returns the digit count of the widest Number in entries
// (minimum 1) so labels can be padded to a uniform width.
func maxNumberDigits(entries []Entry) int {
	max := 1
	for _, e := range entries {
		d := len(strconv.Itoa(e.Number))
		if d > max {
			max = d
		}
	}
	return max
}

func (m Model) renderDetails() string {
	if len(m.entries) == 0 {
		return metaStyle().Render("Start the daemon or run triage to populate the inbox.")
	}
	entry := m.entries[m.cursor]

	var lines []string
	if approvalError := renderApprovalError(entry.ApprovalError); approvalError != "" {
		lines = append(lines, approvalError)
	}
	if strip := renderStatusStrip(entry); strip != "" {
		lines = append(lines, strip)
	}
	if fixStatus := renderFixJobStatus(entry); fixStatus != "" {
		lines = append(lines, fixStatus)
	}
	if strings.TrimSpace(entry.RerunInstructions) != "" {
		lines = append(lines, sectionLabel("Rerun instructions"), renderIndentedBlock(entry.RerunInstructions, "  "))
	}

	if len(lines) > 0 {
		// Blank line between status strip / approval error and body.
		lines = append(lines, "")
	}
	lines = append(lines,
		sectionLabel("Rationale"),
		renderIndentedBlock(emptyFallback(entry.Rationale, "No rationale yet."), "  "),
		sectionLabel("Draft response"),
		renderIndentedBlock(emptyFallback(entry.DraftComment, "No draft response."), "  "),
	)
	if strings.TrimSpace(entry.FixPrompt) != "" {
		lines = append(lines, sectionLabel("Fix prompt"), renderIndentedBlock(formatFixPromptForDisplay(entry.FixPrompt), "  "))
	}
	if len(entry.Followups) > 0 {
		lines = append(lines, sectionLabel("Follow-ups"))
		for _, followup := range entry.Followups {
			lines = append(lines, "  - "+followup)
		}
	}

	if summary := renderActionSummary(entry); summary != "" {
		lines = append(lines, "", summary)
	}

	if footer := renderMetaFooter(entry); footer != "" {
		lines = append(lines, "", footer)
	}
	return strings.Join(lines, "\n")
}

func cardBodyLines(entry Entry) []string {
	var lines []string
	if approvalError := renderApprovalError(entry.ApprovalError); approvalError != "" {
		lines = append(lines, approvalError, "")
	}
	if heading := strings.TrimSpace(entry.Title); heading != "" {
		lines = append(lines, sectionLabelStyle().Render(heading), "")
	}
	if strip := renderStatusStrip(entry); strip != "" {
		lines = append(lines, strip, "")
	}
	if fixStatus := renderFixJobStatus(entry); fixStatus != "" {
		lines = append(lines, fixStatus, "")
	}
	if strings.TrimSpace(entry.RerunInstructions) != "" {
		lines = append(lines, sectionLabel("Rerun instructions"), renderIndentedBlock(entry.RerunInstructions, "  "), "")
	}
	lines = append(lines,
		sectionLabel("Rationale"),
		renderIndentedBlock(emptyFallback(entry.Rationale, "No rationale yet."), "  "),
		sectionLabel("Draft response"),
		renderIndentedBlock(emptyFallback(entry.DraftComment, "No draft response."), "  "),
	)
	if strings.TrimSpace(entry.FixPrompt) != "" {
		lines = append(lines, sectionLabel("Fix prompt"), renderIndentedBlock(formatFixPromptForDisplay(entry.FixPrompt), "  "))
	}
	if len(entry.Followups) > 0 {
		lines = append(lines, sectionLabel("Follow-ups"))
		for _, followup := range entry.Followups {
			lines = append(lines, "  - "+followup)
		}
	}
	if summary := renderActionSummary(entry); summary != "" {
		lines = append(lines, "", summary)
	}
	if footer := renderMetaFooter(entry); footer != "" {
		lines = append(lines, footer)
	}
	return lines
}

func renderFixJobStatus(entry Entry) string {
	if strings.TrimSpace(entry.FixJobID) == "" {
		return ""
	}
	state := strings.TrimSpace(entry.FixMessage)
	if state == "" {
		state = fixPhaseLabel(entry.FixPhase)
	}
	if state == "" {
		state = strings.TrimSpace(entry.FixStatus)
	}
	parts := []string{"Fix: " + state}
	if strings.TrimSpace(entry.FixPRURL) != "" {
		parts = append(parts, strings.TrimSpace(entry.FixPRURL))
	}
	if attach := renderNoMistakesAttachCommand(entry); attach != "" {
		parts = append(parts, "attach: "+attach)
	}
	if strings.TrimSpace(entry.FixError) != "" {
		parts = append(parts, "error: "+strings.TrimSpace(entry.FixError))
	}
	return metaStyle().Render(strings.Join(parts, " · "))
}

func renderNoMistakesAttachCommand(entry Entry) string {
	if strings.TrimSpace(entry.FixPhase) != "waiting_for_pr" || strings.TrimSpace(entry.FixWorktreePath) == "" {
		return ""
	}
	return "cd " + shellQuote(entry.FixWorktreePath) + " && no-mistakes attach"
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func fixPhaseLabel(phase string) string {
	switch strings.TrimSpace(phase) {
	case "preparing_worktree":
		return "preparing worktree"
	case "running_agent":
		return "running agent"
	case "committing":
		return "committing changes"
	case "pushing":
		return "pushing branch"
	case "waiting_for_pr":
		return "waiting for PR"
	case "pr_opened":
		return "PR opened"
	case "queued":
		return "queued"
	case "failed":
		return "failed"
	default:
		return strings.TrimSpace(phase)
	}
}

func (m Model) cardWrappedLines(width int) []string {
	if len(m.entries) == 0 {
		return nil
	}
	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}
	return wrapLines(cardBodyLines(m.entries[m.cursor]), contentWidth)
}

func (m Model) cardMaxScroll(boxHeight int) int {
	if boxHeight <= 0 {
		return 0
	}
	width, _ := m.cardRenderSize()
	wrapped := m.cardWrappedLines(width)
	contentHeight := boxHeight - 2
	if contentHeight < 1 {
		contentHeight = 1
	}
	if len(wrapped) <= contentHeight {
		return 0
	}
	visibleHeight := contentHeight - 1
	if visibleHeight < 1 {
		visibleHeight = 1
	}
	return len(wrapped) - visibleHeight
}

// renderActionSummary describes what `a approve` will execute, e.g.
// "Will: comment + close   labels: bug". The label "Will:" anchors the
// summary as the proposed action - what the agent recommends and what
// approve will perform.
func renderActionSummary(entry Entry) string {
	verbs := actionVerbs(entry)
	if len(verbs) == 0 && len(entry.ProposedLabels) == 0 {
		return sectionLabelStyle().Render("Will:") + " " + metaStyle().Render("mark triaged")
	}
	parts := make([]string, 0, 2)
	if len(verbs) > 0 {
		parts = append(parts, sectionLabelStyle().Render("Will:")+" "+strings.Join(verbs, " + "))
	}
	if len(entry.ProposedLabels) > 0 {
		parts = append(parts, metaStyle().Render("labels: "+strings.Join(entry.ProposedLabels, ", ")))
	}
	return strings.Join(parts, "   ")
}

func renderStatusStrip(entry Entry) string {
	primary := make([]string, 0, 3)
	if author := authorLabel(entry.Author); author != "" {
		primary = append(primary, metaStyle().Render("by "+author))
	}
	if conf := strings.TrimSpace(string(entry.Confidence)); conf != "" {
		primary = append(primary, confidenceStyle(entry.Confidence).Render("confidence: "+conf))
	}
	secondary := make([]string, 0, 1)
	currentWaitingOn := entry.CurrentWaitingOn
	if currentWaitingOn == "" {
		currentWaitingOn = entry.WaitingOn
	}
	if waiting := waitingOnLabel(currentWaitingOn, entry.Author); waiting != "" {
		secondary = append(secondary, metaStyle().Render("currently waiting on "+waiting))
	}
	if len(primary) == 0 && len(secondary) == 0 {
		return ""
	}
	sep := metaStyle().Render(" · ")
	if len(primary) == 0 {
		return strings.Join(secondary, sep)
	}
	if len(secondary) == 0 {
		return strings.Join(primary, sep)
	}
	return strings.Join(primary, sep) + "\n" + strings.Join(secondary, sep)
}

func authorLabel(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return ""
	}
	return "@" + strings.TrimPrefix(author, "@")
}

func waitingOnLabel(waitingOn sharedtypes.WaitingOn, author string) string {
	switch waitingOn {
	case sharedtypes.WaitingOnContributor:
		if label := authorLabel(author); label != "" {
			return label + " (contributor)"
		}
		return "contributor"
	case sharedtypes.WaitingOnMaintainer:
		return "maintainer"
	case sharedtypes.WaitingOnCI:
		return "CI"
	default:
		return ""
	}
}

// renderMetaFooter renders the URL on its own line below the body. Labels
// live in renderActionSummary so this is just the link to the upstream.
func renderMetaFooter(entry Entry) string {
	url := strings.TrimSpace(entry.URL)
	if url == "" {
		return ""
	}
	return metaStyle().Render(url)
}

func sectionLabelStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiCyan))
}

// sectionLabel renders a cyan-bold section header with a trailing colon so
// the label still reads correctly in monochrome terminals.
func sectionLabel(name string) string {
	return sectionLabelStyle().Render(name + ":")
}

func confidenceStyle(c sharedtypes.Confidence) lipgloss.Style {
	switch c {
	case sharedtypes.ConfidenceHigh:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiGreen))
	case sharedtypes.ConfidenceMedium:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiYellow))
	case sharedtypes.ConfidenceLow:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiRed))
	}
	return metaStyle()
}

// renderCard renders the cursor's item as the focal card: repo · type#N ·
// age in the title, the title and full body inside, the decide-bar action
// keys embedded in the bottom border, and proposed action + URL inside.
// Long content is word-wrapped to box width and clipped vertically; when
// clipped, a "↓ N more lines" hint joins the bottom-border footer.
func (m Model) renderCard(width, boxHeight int) string {
	if len(m.entries) == 0 {
		return renderBox(width, "Inbox", metaStyle().Render("No pending recommendations."))
	}
	entry := m.entries[m.cursor]
	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	title := cardTitle(entry)

	maxFooterWidth := width - 7
	if maxFooterWidth < 1 {
		maxFooterWidth = 1
	}
	actionFooter := renderDecideBar(maxFooterWidth, len(entry.Options))
	if pending, ok := m.pendingActions[entry.RecommendationID]; ok {
		actionFooter = renderPendingDecideBar(pending.verb, m.spinnerFrame, entry.RepoID, entry.Number, pending.startedAt)
	}
	wrapped := wrapLines(cardBodyLines(entry), contentWidth)
	if boxHeight <= 0 {
		return renderBoxWithFooter(width, title, strings.Join(wrapped, "\n"), actionFooter)
	}
	contentHeight := boxHeight - 2
	if contentHeight < 1 {
		contentHeight = 1
	}
	visibleHeight := contentHeight
	if len(wrapped) > contentHeight {
		visibleHeight = contentHeight - 1
		if visibleHeight < 1 {
			visibleHeight = 1
		}
	}
	maxScroll := 0
	if len(wrapped) > visibleHeight {
		maxScroll = len(wrapped) - visibleHeight
	}
	scroll := m.cardScroll
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if maxScroll == 0 {
		return renderBoxWithFooter(width, title, strings.Join(wrapped, "\n"), actionFooter)
	}
	end := scroll + visibleHeight
	if end > len(wrapped) {
		end = len(wrapped)
	}
	visible := strings.Join(wrapped[scroll:end], "\n")
	hint := formatCardScrollHint(scroll, len(wrapped)-end)
	sep := metaStyle().Render("  ·  ")
	if _, ok := m.pendingActions[entry.RecommendationID]; !ok {
		actionWidth := maxFooterWidth - lipgloss.Width(hint) - lipgloss.Width(sep)
		actionFooter = renderDecideBar(actionWidth, len(entry.Options))
	}
	footer := hint + sep + actionFooter
	return renderBoxWithFooter(width, title, visible, footer)
}

// cardTitle composes the card's title-bar string: "repo · ⇡ #42 · 2h · option 1/2".
// The option indicator is omitted when the recommendation has only one
// option - the queue position is communicated by the rail's cursor.
func cardTitle(entry Entry) string {
	parts := []string{entry.RepoID, entryNumberLabel(entry)}
	if badge := roleBadge(entry.Role); badge != "" {
		parts = append(parts, badge)
	}
	if len(entry.Options) > 1 {
		parts = append(parts, fmt.Sprintf("option %d/%d", entry.ActiveOption+1, len(entry.Options)))
	}
	if entry.Unconfigured {
		parts = append(parts, "unconfigured")
	}
	return strings.Join(parts, " · ")
}

// applyRoleFilter narrows the inbox to entries matching the filter.
// RoleFilterAll returns the input unchanged. The result preserves order.
func applyRoleFilter(entries []Entry, filter RoleFilter) []Entry {
	if filter == RoleFilterAll {
		return entries
	}
	want := sharedtypes.RoleMaintainer
	if filter == RoleFilterContributor {
		want = sharedtypes.RoleContributor
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		role := entry.Role
		if role == "" {
			role = sharedtypes.RoleMaintainer
		}
		if role == want {
			out = append(out, entry)
		}
	}
	return out
}

// cycleRoleFilter advances the inbox role filter through
// All -> Maintainer -> Contributor -> All. The cursor is reset to the
// top of the filtered list so the user lands on something visible.
func (m *Model) cycleRoleFilter() {
	switch m.roleFilter {
	case RoleFilterAll:
		m.roleFilter = RoleFilterMaintainer
	case RoleFilterMaintainer:
		m.roleFilter = RoleFilterContributor
	default:
		m.roleFilter = RoleFilterAll
	}
	if len(m.allEntries) == 0 {
		m.allEntries = append(m.allEntries[:0], m.entries...)
	}
	filtered := applyRoleFilter(m.allEntries, m.roleFilter)
	m.entries = append(m.entries[:0], filtered...)
	m.cursor = 0
	m.cardScroll = 0
	m.pushLog(logEntry{state: logStateInfo, note: "filter: " + roleFilterLabel(m.roleFilter)})
}

func roleFilterLabel(f RoleFilter) string {
	switch f {
	case RoleFilterMaintainer:
		return "maintainer"
	case RoleFilterContributor:
		return "contributor"
	default:
		return "all"
	}
}

// roleBadge returns a short marker for the entry's role. Maintainer is
// the default and produces no badge to keep the chrome quiet for the
// common case; contributor items get a small "contrib" tag so the user
// can tell at a glance which are theirs to push and which they own.
func roleBadge(role sharedtypes.Role) string {
	if role == sharedtypes.RoleContributor {
		return "contrib"
	}
	return ""
}

// renderQueueRail renders the queue grouped by repo, with the cursor's
// entry highlighted. Repo names appear as dim group headings. The list
// scrolls so the cursor stays visible; a scroll hint shows in the bottom
// border when entries are hidden above or below.
func (m Model) renderQueueRail(width, boxHeight int) string {
	if len(m.entries) == 0 {
		return renderBox(width, "Inbox", metaStyle().Render("Empty."))
	}
	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	// fullBleedWidth spans the box's borders inward, including the inner
	// pad spaces renderBox would otherwise add - used so the cursor's
	// background highlight reaches both borders.
	fullBleedWidth := contentWidth + 2

	type railLine struct {
		text      string
		anchor    bool // group headers are anchors that ideally stay visible
		fullBleed bool // line spans the inner pad spaces (cursor highlight)
	}

	digitWidth := maxNumberDigits(m.entries)
	var allLines []railLine
	cursorIdx := -1
	lastRepo := ""
	for i, entry := range m.entries {
		if entry.RepoID != lastRepo {
			allLines = append(allLines, railLine{
				text:   metaStyle().Render(truncateToWidth(entry.RepoID, contentWidth)),
				anchor: true,
			})
			lastRepo = entry.RepoID
		}
		_, isPending := m.pendingActions[entry.RecommendationID]
		isPending = isPending || fixJobInProgress(entry)
		fullBleed := false
		var text string
		if i == m.cursor {
			plainLabel := plainEntryNumberLabelPadded(entry, digitWidth, isPending)
			plain := fmt.Sprintf("%s  %s", plainLabel, entry.Title)
			text = renderHighlightRow(plainText(plain), fullBleedWidth)
			fullBleed = true
			cursorIdx = len(allLines)
		} else {
			label := entryNumberLabelPaddedWithPending(entry, digitWidth, isPending)
			text = fmt.Sprintf("%s  %s", label, entry.Title)
			text = truncateToWidth(text, contentWidth)
		}
		allLines = append(allLines, railLine{text: text, fullBleed: fullBleed})
	}

	visible := allLines
	scrollHint := ""
	contentHeight := -1
	if boxHeight > 0 {
		contentHeight = boxHeight - 2
		if contentHeight < 1 {
			contentHeight = 1
		}
	}
	if contentHeight > 0 && len(allLines) > contentHeight && cursorIdx >= 0 {
		start := cursorIdx - contentHeight/2
		if start < 0 {
			start = 0
		}
		end := start + contentHeight
		if end > len(allLines) {
			end = len(allLines)
			start = end - contentHeight
			if start < 0 {
				start = 0
			}
		}
		visible = allLines[start:end]
		scrollHint = formatScrollHint(start, len(allLines)-end)
	}

	bodyLines := make([]boxBodyLine, 0, len(visible))
	for _, line := range visible {
		bodyLines = append(bodyLines, boxBodyLine{text: line.text, fullBleed: line.fullBleed})
	}
	title := fmt.Sprintf("Inbox · %d of %d", m.cursor+1, len(m.entries))
	return renderBoxLinesWithFooter(width, title, bodyLines, scrollHint)
}

// renderDetailsBox renders the details pane within boxHeight total lines
// (including borders). boxHeight <= 0 disables clipping. Long lines are
// word-wrapped to fit the box width so prose stays fully readable; if the
// wrapped content still exceeds the height budget, the bottom lines are
// dropped and a "↓ N more lines" hint is shown in the bottom border.
func (m Model) renderDetailsBox(width, boxHeight int) string {
	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}
	wrapped := wrapLines(strings.Split(m.renderDetails(), "\n"), contentWidth)
	if boxHeight <= 0 {
		return renderBox(width, "Details", strings.Join(wrapped, "\n"))
	}
	contentHeight := boxHeight - 2
	if contentHeight < 1 {
		contentHeight = 1
	}
	if len(wrapped) <= contentHeight {
		return renderBox(width, "Details", strings.Join(wrapped, "\n"))
	}
	hidden := len(wrapped) - (contentHeight - 1)
	visible := strings.Join(wrapped[:contentHeight-1], "\n")
	hint := metaStyle().Render(fmt.Sprintf("↓ %d more lines", hidden))
	return renderBoxWithFooter(width, "Details", visible, hint)
}

// wrapLines word-wraps each input line to width, preserving any leading
// whitespace as the continuation indent. Tokens longer than the width
// (e.g., regex, URLs, long identifiers) are hard-broken at the boundary
// because the alternative - letting them spill past the box edge - bleeds
// into adjacent panels and breaks the layout.
func wrapLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapLine(line, width)...)
	}
	return out
}

func wrapLine(line string, width int) []string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return []string{line}
	}
	indent := leadingWhitespace(line)
	body := line[len(indent):]
	bodyWidth := width - lipgloss.Width(indent)
	if bodyWidth < 1 {
		return []string{line}
	}

	words := strings.Fields(body)
	if len(words) == 0 {
		return []string{line}
	}

	// Pre-split any token that on its own exceeds bodyWidth. Without this,
	// the greedy fill below would emit the oversized token whole on a line
	// of its own and bleed past the box edge.
	expanded := make([]string, 0, len(words))
	for _, w := range words {
		if lipgloss.Width(w) > bodyWidth {
			expanded = append(expanded, hardBreak(w, bodyWidth)...)
		} else {
			expanded = append(expanded, w)
		}
	}

	var wrapped []string
	cur := expanded[0]
	for _, word := range expanded[1:] {
		if lipgloss.Width(cur)+1+lipgloss.Width(word) <= bodyWidth {
			cur += " " + word
		} else {
			wrapped = append(wrapped, indent+cur)
			cur = word
		}
	}
	wrapped = append(wrapped, indent+cur)
	return wrapped
}

// hardBreak splits s into chunks that each fit within width display
// columns, measured by lipgloss.Width so wide runes (CJK, emoji) are
// accounted for. Splitting happens at rune boundaries to avoid cutting a
// multi-byte character in half.
func hardBreak(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var parts []string
	var cur strings.Builder
	curW := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if curW+rw > width && cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(r)
		curW += rw
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

func renderIndentedBlock(text string, indent string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func formatFixPromptForDisplay(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.Contains(text, "\n") {
		return text
	}
	for _, marker := range []string{
		"Problem",
		"Summary",
		"Reproduction / evidence",
		"Evidence",
		"Suspected files/components",
		"Acceptance criteria",
		"Verification steps",
		"Implementation notes",
	} {
		text = splitFixPromptSection(text, marker)
	}
	return text
}

func splitFixPromptSection(text string, marker string) string {
	for _, pattern := range []string{" " + marker + ": ", " " + marker + " "} {
		index := strings.Index(text, pattern)
		if index < 0 {
			continue
		}
		before := text[:index]
		after := text[index+len(pattern):]
		label := strings.TrimSpace(pattern)
		if strings.HasSuffix(label, ":") {
			label = strings.TrimSuffix(label, ":") + ":"
		}
		return before + "\n" + label + "\n" + after
	}
	return text
}

func renderBox(width int, title string, body string) string {
	return renderBoxWithFooter(width, title, body, "")
}

// boxBodyLine is one rendered body line with a flag controlling whether the
// inner left/right pad spaces should be omitted - useful for selection
// highlights that need to span all the way to both borders.
type boxBodyLine struct {
	text      string
	fullBleed bool
}

// renderBoxLinesWithFooter is the per-line variant of renderBoxWithFooter:
// fullBleed lines are expected to already span (contentWidth + 2) cells and
// are placed flush against the borders without the inner pad space.
func renderBoxLinesWithFooter(width int, title string, lines []boxBodyLine, footer string) string {
	if width < 6 {
		width = 6
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlack))
	styledTitle := titleStyle().Render(title)

	titleW := lipgloss.Width(styledTitle)
	fillW := width - 5 - titleW
	if fillW < 1 {
		fillW = 1
	}
	top := border.Render("╭─ ") + styledTitle + " " + border.Render(strings.Repeat("─", fillW)+"╮")

	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	rendered := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln.fullBleed {
			rendered = append(rendered, border.Render("│")+ln.text+border.Render("│"))
			continue
		}
		visW := lipgloss.Width(ln.text)
		pad := contentWidth - visW
		if pad < 0 {
			pad = 0
		}
		rendered = append(rendered, border.Render("│")+" "+ln.text+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}

	return top + "\n" + strings.Join(rendered, "\n") + "\n" + renderBottomBorder(width, footer)
}

// renderBoxWithFooter renders content inside a rounded-border box with the
// title embedded in the top border and an optional hint embedded in the
// bottom border (e.g., "↓ 3 more (j ↓ / k ↑)" for scrollable content).
func renderBoxWithFooter(width int, title string, body string, footer string) string {
	if width < 6 {
		width = 6
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlack))
	styledTitle := titleStyle().Render(title)

	titleW := lipgloss.Width(styledTitle)
	fillW := width - 5 - titleW
	if fillW < 1 {
		fillW = 1
	}
	top := border.Render("╭─ ") + styledTitle + " " + border.Render(strings.Repeat("─", fillW)+"╮")

	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	rawLines := strings.Split(body, "\n")
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}
	lines := make([]string, 0, len(rawLines))
	for _, cl := range rawLines {
		visW := lipgloss.Width(cl)
		pad := contentWidth - visW
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, border.Render("│")+" "+cl+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}

	return top + "\n" + strings.Join(lines, "\n") + "\n" + renderBottomBorder(width, footer)
}

// renderBottomBorder renders the closing border with an optional embedded
// footer string. The footer is rendered as-is (caller pre-styles), so the
// same primitive supports dim scroll hints and bolder action bars.
func renderBottomBorder(width int, footer string) string {
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlack))
	if footer == "" {
		fill := width - 2
		if fill < 1 {
			fill = 1
		}
		return border.Render("╰" + strings.Repeat("─", fill) + "╯")
	}
	rw := lipgloss.Width(footer)
	maxFooter := width - 7
	if maxFooter < 1 {
		maxFooter = 1
	}
	if rw > maxFooter {
		footer = clipStyledToWidth(footer, maxFooter)
		rw = lipgloss.Width(footer)
	}
	trail := width - rw - 6
	if trail < 1 {
		trail = 1
	}
	return border.Render("╰── ") + footer + " " + border.Render(strings.Repeat("─", trail)+"╯")
}

// clipStyledToWidth shortens a styled string so its visible width fits.
// ANSI escapes are kept verbatim; visible runes are dropped from the right
// until the display width is within budget.
func clipStyledToWidth(s string, max int) string {
	if max <= 0 || lipgloss.Width(s) <= max {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		if lipgloss.Width(string(runes)) <= max {
			return string(runes)
		}
	}
	return ""
}

func titleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiCyan))
}

func metaStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlack))
}

func actionBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiBlue))
}

func errorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ansiRed))
}

// actionVerbs returns the human-readable verbs for the recommendation's
// comment + state change combination, in the order they will be applied.
func actionVerbs(entry Entry) []string {
	var verbs []string
	if strings.TrimSpace(entry.DraftComment) != "" {
		verbs = append(verbs, "comment")
	}
	switch entry.StateChange {
	case sharedtypes.StateChangeClose:
		verbs = append(verbs, "close")
	case sharedtypes.StateChangeMerge:
		verbs = append(verbs, "merge")
	case sharedtypes.StateChangeRequestChanges:
		verbs = append(verbs, "request changes")
	case sharedtypes.StateChangeFixRequired:
		if strings.TrimSpace(entry.FixPrompt) != "" {
			verbs = append(verbs, "fix")
		}
	}
	return verbs
}

func emptyFallback(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func renderApprovalError(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	return errorStyle().Render("Last approval failed: " + message)
}

func formatTokens(count int) string {
	if count >= 1000 {
		whole := count / 1000
		tenth := (count % 1000) / 100
		return fmt.Sprintf("%d.%dk", whole, tenth)
	}
	return fmt.Sprintf("%d", count)
}

func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].RepoID != entries[j].RepoID {
			return entries[i].RepoID < entries[j].RepoID
		}
		return entries[i].Number < entries[j].Number
	})
}

func (m *Model) dismissCurrent() tea.Cmd {
	if len(m.entries) == 0 {
		return nil
	}
	entry := m.entries[m.cursor]
	if cmd, blocked := m.guardConflict(entry, "mark"); blocked {
		return cmd
	}
	if m.dismiss == nil {
		m.removeEntries([]int{m.cursor})
		m.pushLog(logEntry{state: logStateInfo, note: fmt.Sprintf("marked triaged %s #%d", entry.RepoID, entry.Number)})
		return nil
	}
	cmd := m.startAction(entry, "mark", func() tea.Msg {
		return actionFinishedMsg{
			verb:             "mark",
			recommendationID: entry.RecommendationID,
			err:              m.dismiss([]Entry{entry}),
		}
	})
	m.advanceCursorPastPending()
	return cmd
}

// advanceCursorPastPending moves the selection to the next entry when an
// action expected to remove the current entry has just been started. The
// pending entry stays visible (with its spinner) until the async action
// finishes, but the cursor moves on so the maintainer can immediately
// see what's next.
func (m *Model) advanceCursorPastPending() {
	if m.cursor < len(m.entries)-1 {
		m.cursor++
		m.cardScroll = 0
	}
}

func (m *Model) removeEntries(indices []int) {
	if len(indices) == 0 {
		return
	}
	origCursor := m.cursor
	currentID := m.currentRecommendationID()
	remove := make(map[int]struct{}, len(indices))
	removeIDs := make(map[string]struct{}, len(indices))
	for _, index := range indices {
		remove[index] = struct{}{}
		if index >= 0 && index < len(m.entries) && m.entries[index].RecommendationID != "" {
			removeIDs[m.entries[index].RecommendationID] = struct{}{}
		}
	}
	kept := m.entries[:0]
	for index, entry := range m.entries {
		if _, ok := remove[index]; ok {
			continue
		}
		kept = append(kept, entry)
	}
	m.entries = kept
	m.removeAllEntriesByID(removeIDs)

	// Try to keep the cursor on the same recommendation it pointed at
	// before the removal - critical when an earlier entry (one before
	// the cursor) is being removed by an async action that resolved
	// after the user already advanced.
	newIdx := -1
	if currentID != "" {
		for i, e := range m.entries {
			if e.RecommendationID == currentID {
				newIdx = i
				break
			}
		}
	}
	if newIdx >= 0 {
		m.cursor = newIdx
	} else {
		// The cursor's own entry was removed. Keep cursor on whatever
		// now occupies its position - which is the entry that came
		// immediately after the removed one - by subtracting the
		// number of removed entries that sat before it.
		removedBefore := 0
		for _, idx := range indices {
			if idx < origCursor {
				removedBefore++
			}
		}
		m.cursor = origCursor - removedBefore
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
	if m.cursor < 0 || len(m.entries) == 0 {
		m.cursor = 0
	}
	if currentID != m.currentRecommendationID() {
		m.cardScroll = 0
	}
}

func (m *Model) removeAllEntriesByID(ids map[string]struct{}) {
	if len(ids) == 0 || len(m.allEntries) == 0 {
		return
	}
	kept := m.allEntries[:0]
	for _, entry := range m.allEntries {
		if _, ok := ids[entry.RecommendationID]; ok {
			continue
		}
		kept = append(kept, entry)
	}
	m.allEntries = kept
}

func (m *Model) replaceAllEntry(updated Entry, oldIDs ...string) {
	if updated.RecommendationID == "" {
		return
	}
	for i := range m.allEntries {
		if m.allEntries[i].RecommendationID == updated.RecommendationID {
			m.allEntries[i] = updated
			return
		}
	}
	oldIDSet := make(map[string]struct{}, len(oldIDs))
	for _, id := range oldIDs {
		if id != "" && id != updated.RecommendationID {
			oldIDSet[id] = struct{}{}
		}
	}
	if len(oldIDSet) > 0 {
		for i := range m.allEntries {
			if _, ok := oldIDSet[m.allEntries[i].RecommendationID]; ok {
				m.allEntries[i] = updated
				return
			}
		}
	}
}

func (m *Model) hasAllEntry(recommendationID string) bool {
	if recommendationID == "" {
		return false
	}
	for i := range m.allEntries {
		if m.allEntries[i].RecommendationID == recommendationID {
			return true
		}
	}
	return false
}

func preserveEntryState(entry *Entry, prev Entry) {
	if len(prev.Options) > 0 && len(entry.Options) > 0 {
		byID := make(map[string]EntryOption, len(prev.Options))
		for _, o := range prev.Options {
			byID[o.ID] = o
		}
		for j := range entry.Options {
			if p, ok := byID[entry.Options[j].ID]; ok && p.Edited() {
				entry.Options[j].StateChange = p.StateChange
				entry.Options[j].ProposedLabels = append([]string(nil), p.ProposedLabels...)
				entry.Options[j].DraftComment = p.DraftComment
			}
		}
		if prev.ActiveOption < len(entry.Options) {
			entry.ActiveOption = prev.ActiveOption
		}
	}
	entry.SyncActive()
	entry.StateChange = prev.StateChange
	entry.OriginalStateChange = prev.OriginalStateChange
	entry.ProposedLabels = append([]string(nil), prev.ProposedLabels...)
	entry.OriginalProposedLabels = append([]string(nil), prev.OriginalProposedLabels...)
	entry.DraftComment = prev.DraftComment
	entry.OriginalDraftComment = prev.OriginalDraftComment
}

func (m *Model) currentRecommendationID() string {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return ""
	}
	return m.entries[m.cursor].RecommendationID
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func waitForNotification(notify <-chan struct{}) tea.Cmd {
	if notify == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-notify; !ok {
			return nil
		}
		return notifyReloadMsg{}
	}
}

func runReload(reload func() ([]Entry, error)) tea.Cmd {
	return func() tea.Msg {
		entries, err := reload()
		return reloadedEntriesMsg{Entries: entries, Err: err}
	}
}

func (m *Model) applyReload(entries []Entry) {
	currentID := ""
	var current Entry
	hasCurrent := false
	if m.cursor >= 0 && m.cursor < len(m.entries) {
		current = m.entries[m.cursor]
		current.CommitEdits()
		currentID = current.RecommendationID
		hasCurrent = true
	}

	editedByID := make(map[string]Entry)
	for _, entry := range m.allEntries {
		entry.CommitEdits()
		if entry.Edited() || entry.ActiveOption > 0 {
			editedByID[entry.RecommendationID] = entry
		}
	}
	for _, entry := range m.entries {
		entry.CommitEdits()
		if entry.Edited() || entry.ActiveOption > 0 {
			editedByID[entry.RecommendationID] = entry
		}
	}

	m.allEntries = append(m.allEntries[:0], entries...)
	for i := range m.allEntries {
		if prev, ok := editedByID[m.allEntries[i].RecommendationID]; ok {
			preserveEntryState(&m.allEntries[i], prev)
		}
	}
	entries = applyRoleFilter(entries, m.roleFilter)
	m.entries = append(m.entries[:0], entries...)
	newCursor := 0
	foundCursor := false
	for i := range m.entries {
		if prev, ok := editedByID[m.entries[i].RecommendationID]; ok {
			preserveEntryState(&m.entries[i], prev)
		}
		if !foundCursor && currentID != "" && m.entries[i].RecommendationID == currentID {
			newCursor = i
			foundCursor = true
		}
	}

	if len(m.entries) == 0 {
		m.cursor = 0
		m.cardScroll = 0
		return
	}
	if foundCursor {
		m.cursor = newCursor
		if hasCurrent && !reflect.DeepEqual(current, m.entries[m.cursor]) {
			m.cardScroll = 0
		}
		return
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
		m.cardScroll = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.cardScroll = 0
}
