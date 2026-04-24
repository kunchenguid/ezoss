// Package wizard provides the interactive `ezoss init` flow. It walks the
// user through choosing a repo-seeding mode (all owned, all public owned, or
// one at a time) and confirms the resulting set before handing control back
// to the caller for persistence.
package wizard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/kunchenguid/ezoss/internal/ghclient"
)

// Mode identifies which repo-seeding strategy the user picked.
type Mode int

const (
	ModeNone Mode = iota
	ModeAllOwned
	ModeAllPublicOwned
	ModeAllPublicOwnedAndStarred
	ModeOneAtATime
)

func (m Mode) label() string {
	switch m {
	case ModeAllOwned:
		return "Add all repos I own"
	case ModeAllPublicOwned:
		return "Add all my public repos only"
	case ModeAllPublicOwnedAndStarred:
		return "Add public repos I both own and have starred"
	case ModeOneAtATime:
		return "Set up one repo at a time"
	}
	return ""
}

// Config wires the wizard to its external dependencies and pre-detected
// state. ListOwnedRepos and DetectedRepo are typically provided by the
// caller; tests inject stubs for both.
type Config struct {
	Context context.Context
	// DetectedRepo is the owner/name parsed from the current git remote.
	// Empty when no GitHub remote could be detected.
	DetectedRepo string
	// ListOwnedRepos fetches the owner/name list from `gh`. Required.
	ListOwnedRepos func(ctx context.Context, visibility ghclient.RepoVisibility) ([]string, error)
	// ListStarredRepos fetches the user's starred repos. Required only when
	// ModeAllPublicOwnedAndStarred is reachable; if nil, that mode reports a
	// fetch error rather than panicking.
	ListStarredRepos func(ctx context.Context) ([]string, error)
	// Track records telemetry events. Optional.
	Track func(action string, fields map[string]any)
	// Output overrides the Bubble Tea output writer.
	Output io.Writer
	// DisableInput disables Bubble Tea input (for tests).
	DisableInput bool
}

// Result describes what the wizard produced. Repos is the deduped owner/name
// set the user confirmed; the caller is responsible for persisting it.
type Result struct {
	Mode    Mode
	Repos   []string
	Aborted bool
	Err     error
}

// screen is the wizard's internal page state.
type screen int

const (
	screenMode screen = iota
	screenFetching
	screenBulkConfirm
	screenBulkEmpty
	screenBulkError
	screenDetected
	screenManual
	screenDone
)

// Model is the bubbletea model for the wizard.
type Model struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	screen      screen
	mode        Mode
	selectedIdx int

	fetched  []string
	fetchErr error

	input textinput.Model
	repos []string

	success  bool
	aborted  bool
	err      error
	quitting bool

	spinnerFrame int
	spinnerAlive bool

	width int
}

// modeOptions defines the mode-select rows in display order.
var modeOptions = []Mode{ModeAllOwned, ModeAllPublicOwned, ModeAllPublicOwnedAndStarred, ModeOneAtATime}

// NewModel constructs a wizard Model. The wizard starts on the mode-select
// screen regardless of detected state - the detected repo is only consulted
// after the user picks ModeOneAtATime.
func NewModel(cfg Config) Model {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.CharLimit = 120
	ti.Placeholder = "owner/name"

	baseCtx := cfg.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(baseCtx)

	return Model{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
		screen: screenMode,
		input:  ti,
	}
}

func (m Model) Init() tea.Cmd { return nil }

// Result reports the wizard's terminal state. Only meaningful after the
// program has exited.
func (m Model) Result() Result {
	return Result{
		Mode:    m.mode,
		Repos:   append([]string(nil), m.repos...),
		Aborted: m.aborted,
		Err:     m.err,
	}
}

// Update handles bubbletea events.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case fetchMsg:
		return m.handleFetch(msg)

	case spinnerTickMsg:
		m.spinnerAlive = false
		if m.screen != screenFetching {
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, m.scheduleSpinner()
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		return m.abort("interrupt")
	}

	switch m.screen {
	case screenMode:
		return m.handleModeKey(msg)
	case screenBulkConfirm:
		return m.handleBulkConfirmKey(key)
	case screenBulkEmpty, screenBulkError:
		return m.handleBulkErrorKey(key)
	case screenDetected:
		return m.handleDetectedKey(key)
	case screenManual:
		return m.handleManualKey(msg)
	case screenDone:
		return m.quit()
	}
	return m, nil
}

func (m Model) handleModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q":
		return m.abort("quit")
	case "1":
		return m.pickMode(ModeAllOwned)
	case "2":
		return m.pickMode(ModeAllPublicOwned)
	case "3":
		return m.pickMode(ModeAllPublicOwnedAndStarred)
	case "4":
		return m.pickMode(ModeOneAtATime)
	case "up", "k":
		if m.selectedIdx > 0 {
			m.selectedIdx--
		}
		return m, nil
	case "down", "j":
		if m.selectedIdx < len(modeOptions)-1 {
			m.selectedIdx++
		}
		return m, nil
	case "enter":
		return m.pickMode(modeOptions[m.selectedIdx])
	}
	return m, nil
}

func (m Model) handleBulkConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		m.repos = m.fetched
		m.success = true
		m.screen = screenDone
		m.track("completed", map[string]any{
			"mode":  modeName(m.mode),
			"count": len(m.repos),
		})
		return m, m.quitCmd()
	case "n", "N":
		// Cancel back to mode select to give them a second chance.
		m.screen = screenMode
		m.fetched = nil
		m.fetchErr = nil
		m.mode = ModeNone
		return m, nil
	case "q":
		return m.abort("quit")
	}
	return m, nil
}

func (m Model) handleBulkErrorKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "r", "R":
		m.screen = screenFetching
		m.fetched = nil
		m.fetchErr = nil
		return m, tea.Batch(m.fetchCmdForMode(m.mode), m.scheduleSpinner())
	case "b", "B":
		m.screen = screenMode
		m.fetched = nil
		m.fetchErr = nil
		m.mode = ModeNone
		return m, nil
	case "q":
		return m.abort("quit")
	}
	return m, nil
}

func (m Model) handleDetectedKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		m.repos = []string{m.cfg.DetectedRepo}
		m.success = true
		m.screen = screenDone
		m.track("completed", map[string]any{
			"mode":  modeName(m.mode),
			"count": 1,
		})
		return m, m.quitCmd()
	case "m", "M", "n", "N":
		m.screen = screenManual
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	case "q":
		return m.abort("quit")
	}
	return m, nil
}

func (m Model) handleManualKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		repoID, err := parseRepoID(value)
		if err != nil {
			m.input.SetValue("")
			m.input.Placeholder = err.Error()
			return m, nil
		}
		m.repos = []string{repoID}
		m.success = true
		m.screen = screenDone
		m.track("completed", map[string]any{
			"mode":  modeName(m.mode),
			"count": 1,
		})
		return m, m.quitCmd()
	case "esc":
		if m.cfg.DetectedRepo != "" {
			m.screen = screenDetected
			m.input.Blur()
			return m, nil
		}
		return m.abort("quit")
	case "ctrl+c":
		return m.abort("interrupt")
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) pickMode(mode Mode) (tea.Model, tea.Cmd) {
	m.mode = mode
	m.track("mode_selected", map[string]any{"mode": modeName(mode)})
	switch mode {
	case ModeAllOwned, ModeAllPublicOwned, ModeAllPublicOwnedAndStarred:
		m.screen = screenFetching
		return m, tea.Batch(m.fetchCmdForMode(mode), m.scheduleSpinner())
	case ModeOneAtATime:
		if m.cfg.DetectedRepo != "" {
			m.screen = screenDetected
			return m, nil
		}
		m.screen = screenManual
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m Model) handleFetch(msg fetchMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenFetching {
		return m, nil
	}
	if msg.err != nil {
		m.fetchErr = msg.err
		m.screen = screenBulkError
		return m, nil
	}
	if len(msg.repos) == 0 {
		m.screen = screenBulkEmpty
		return m, nil
	}
	m.fetched = dedupRepos(msg.repos)
	m.screen = screenBulkConfirm
	return m, nil
}

func (m Model) abort(reason string) (tea.Model, tea.Cmd) {
	m.aborted = true
	m.quitting = true
	m.cancel()
	m.track("aborted", map[string]any{"reason": reason, "screen": screenName(m.screen)})
	return m, m.quitCmd()
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	m.cancel()
	return m, m.quitCmd()
}

func (m Model) quitCmd() tea.Cmd {
	return tea.Sequence(tea.SetWindowTitle(""), tea.Quit)
}

func (m Model) track(action string, fields map[string]any) {
	if m.cfg.Track == nil {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	m.cfg.Track(action, fields)
}

// Commands.

type fetchMsg struct {
	repos []string
	err   error
}

type spinnerTickMsg struct{}

const spinnerInterval = 120 * time.Millisecond

// fetchCmdForMode returns the bubbletea Cmd that fetches repos for the
// given bulk mode. ModeOneAtATime is rejected as a programming error - it
// has no fetch step.
func (m Model) fetchCmdForMode(mode Mode) tea.Cmd {
	ctx := m.ctx
	owned := m.cfg.ListOwnedRepos
	starred := m.cfg.ListStarredRepos

	return func() tea.Msg {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		switch mode {
		case ModeAllOwned:
			if owned == nil {
				return fetchMsg{err: errors.New("no repo lister configured")}
			}
			repos, err := owned(fetchCtx, ghclient.RepoVisibilityAll)
			return fetchMsg{repos: repos, err: err}

		case ModeAllPublicOwned:
			if owned == nil {
				return fetchMsg{err: errors.New("no repo lister configured")}
			}
			repos, err := owned(fetchCtx, ghclient.RepoVisibilityPublic)
			return fetchMsg{repos: repos, err: err}

		case ModeAllPublicOwnedAndStarred:
			if owned == nil {
				return fetchMsg{err: errors.New("no repo lister configured")}
			}
			if starred == nil {
				return fetchMsg{err: errors.New("no starred repo lister configured")}
			}
			ownedRepos, err := owned(fetchCtx, ghclient.RepoVisibilityPublic)
			if err != nil {
				return fetchMsg{err: err}
			}
			starredRepos, err := starred(fetchCtx)
			if err != nil {
				return fetchMsg{err: err}
			}
			return fetchMsg{repos: intersectRepos(ownedRepos, starredRepos)}
		}

		return fetchMsg{err: fmt.Errorf("fetch not supported for mode %v", mode)}
	}
}

func (m *Model) scheduleSpinner() tea.Cmd {
	if m.spinnerAlive {
		return nil
	}
	m.spinnerAlive = true
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// Run invokes the wizard as an interactive bubbletea program.
func Run(cfg Config) (Result, error) {
	baseCtx := cfg.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	if err := baseCtx.Err(); err != nil {
		return Result{Err: err}, err
	}
	m := NewModel(cfg)
	options := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(baseCtx)}
	if cfg.DisableInput {
		options = append(options, tea.WithInput(nil))
	}
	if cfg.Output != nil {
		options = append(options, tea.WithOutput(cfg.Output))
	}
	defer resetTerminalTitle(cfg.Output)
	p := tea.NewProgram(m, options...)
	final, err := p.Run()
	if err != nil {
		return Result{Err: err}, err
	}
	fm, ok := final.(Model)
	if !ok {
		return Result{}, errors.New("wizard: unexpected terminal model type")
	}
	return fm.Result(), nil
}

// intersectRepos returns repos present in both lists, preserving the order
// of the first list. Whitespace-padded entries are normalized away.
func intersectRepos(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	bSet := make(map[string]struct{}, len(b))
	for _, r := range b {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		bSet[r] = struct{}{}
	}
	out := make([]string, 0, len(a))
	seen := make(map[string]struct{}, len(a))
	for _, r := range a {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, ok := bSet[r]; !ok {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// dedupRepos returns the input slice with consecutive duplicates collapsed
// while preserving the original ordering.
func dedupRepos(repos []string) []string {
	if len(repos) == 0 {
		return repos
	}
	seen := make(map[string]struct{}, len(repos))
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// parseRepoID validates a free-text "owner/name" entry. Mirrors the
// equivalent helper in internal/cli but kept local so the wizard package
// stays independent.
func parseRepoID(value string) (string, error) {
	owner, name, ok := strings.Cut(value, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%q is not owner/name", value)
	}
	return owner + "/" + name, nil
}

func modeName(m Mode) string {
	switch m {
	case ModeAllOwned:
		return "all_owned"
	case ModeAllPublicOwned:
		return "all_public_owned"
	case ModeAllPublicOwnedAndStarred:
		return "all_public_owned_and_starred"
	case ModeOneAtATime:
		return "one_at_a_time"
	}
	return "none"
}

func screenName(s screen) string {
	switch s {
	case screenMode:
		return "mode"
	case screenFetching:
		return "fetching"
	case screenBulkConfirm:
		return "bulk_confirm"
	case screenBulkEmpty:
		return "bulk_empty"
	case screenBulkError:
		return "bulk_error"
	case screenDetected:
		return "detected"
	case screenManual:
		return "manual"
	case screenDone:
		return "done"
	}
	return "unknown"
}

func resetTerminalTitle(output io.Writer) {
	if output == nil {
		output = os.Stdout
	}
	_, _ = io.WriteString(output, "\x1b]2;\x07")
}
