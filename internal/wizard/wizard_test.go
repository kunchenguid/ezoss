package wizard

import (
	"context"
	"errors"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kunchenguid/ezoss/internal/ghclient"
)

type recorder struct {
	listCalls    []ghclient.RepoVisibility
	listRepos    []string
	listErr      error
	listVisCall  ghclient.RepoVisibility
	starredCalls int
	starredRepos []string
	starredErr   error
	telemetry    []telemetryEvent
}

type telemetryEvent struct {
	action string
	fields map[string]any
}

func (r *recorder) deps() Config {
	return Config{
		ListOwnedRepos: func(_ context.Context, vis ghclient.RepoVisibility) ([]string, error) {
			r.listCalls = append(r.listCalls, vis)
			r.listVisCall = vis
			return r.listRepos, r.listErr
		},
		ListStarredRepos: func(_ context.Context) ([]string, error) {
			r.starredCalls++
			return r.starredRepos, r.starredErr
		},
		Track: func(action string, fields map[string]any) {
			clone := make(map[string]any, len(fields))
			for k, v := range fields {
				clone[k] = v
			}
			r.telemetry = append(r.telemetry, telemetryEvent{action: action, fields: clone})
		},
	}
}

// drain runs the cmd chain until no more cmds are produced. It skips
// spinner ticks (which would loop forever) and unwraps batch messages.
func drain(m Model, cmd tea.Cmd) Model {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return m
		}
		if _, ok := msg.(spinnerTickMsg); ok {
			return m
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				m = drain(m, c)
			}
			return m
		}
		// Sequence messages contain ordered cmds; the wrapper type is unexported,
		// so just stop walking when we see one (the only sequence we emit is
		// quit, which we don't need to follow in tests).
		next, nextCmd := m.Update(msg)
		m = next.(Model)
		cmd = nextCmd
	}
	return m
}

func advance(m Model, msg tea.Msg) Model {
	next, cmd := m.Update(msg)
	m = next.(Model)
	return drain(m, cmd)
}

func TestNewModel_StartsOnModeSelect(t *testing.T) {
	m := NewModel((&recorder{}).deps())
	if m.screen != screenMode {
		t.Fatalf("screen = %v, want screenMode", m.screen)
	}
	if m.mode != ModeNone {
		t.Fatalf("mode = %v, want ModeNone", m.mode)
	}
}

func TestModeSelect_QuickKeyAllOwned_FetchesAndConfirms(t *testing.T) {
	r := &recorder{listRepos: []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.screen != screenBulkConfirm {
		t.Fatalf("screen = %v, want screenBulkConfirm", m.screen)
	}
	if m.mode != ModeAllOwned {
		t.Fatalf("mode = %v, want ModeAllOwned", m.mode)
	}
	if r.listVisCall != ghclient.RepoVisibilityAll {
		t.Fatalf("visibility = %q, want all", r.listVisCall)
	}
	if !reflect.DeepEqual(m.fetched, r.listRepos) {
		t.Fatalf("fetched = %v, want %v", m.fetched, r.listRepos)
	}
}

func TestModeSelect_QuickKeyPublicOwned_PassesPublicVisibility(t *testing.T) {
	r := &recorder{listRepos: []string{"kunchenguid/ezoss"}}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m.mode != ModeAllPublicOwned {
		t.Fatalf("mode = %v, want ModeAllPublicOwned", m.mode)
	}
	if r.listVisCall != ghclient.RepoVisibilityPublic {
		t.Fatalf("visibility = %q, want public", r.listVisCall)
	}
}

func TestModeSelect_PublicOwnedAndStarred_IntersectsTheTwoLists(t *testing.T) {
	r := &recorder{
		listRepos:    []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes", "kunchenguid/internal"},
		starredRepos: []string{"acme/widgets", "kunchenguid/ezoss", "kunchenguid/no-mistakes"},
	}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.mode != ModeAllPublicOwnedAndStarred {
		t.Fatalf("mode = %v, want ModeAllPublicOwnedAndStarred", m.mode)
	}
	if r.listVisCall != ghclient.RepoVisibilityPublic {
		t.Fatalf("ListOwnedRepos visibility = %q, want public", r.listVisCall)
	}
	if r.starredCalls != 1 {
		t.Fatalf("ListStarredRepos calls = %d, want 1", r.starredCalls)
	}

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	want := []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}
	if !reflect.DeepEqual(m.repos, want) {
		t.Fatalf("repos = %v, want %v (intersection of owned ∩ starred, owned ordering)", m.repos, want)
	}
}

func TestModeSelect_PublicOwnedAndStarred_EmptyIntersectionGoesToBulkEmpty(t *testing.T) {
	r := &recorder{
		listRepos:    []string{"kunchenguid/ezoss"},
		starredRepos: []string{"acme/widgets"},
	}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.screen != screenBulkEmpty {
		t.Fatalf("screen = %v, want screenBulkEmpty when intersection is empty", m.screen)
	}
}

func TestModeSelect_PublicOwnedAndStarred_PropagatesStarredError(t *testing.T) {
	r := &recorder{
		listRepos:  []string{"kunchenguid/ezoss"},
		starredErr: errors.New("starred fetch broke"),
	}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.screen != screenBulkError {
		t.Fatalf("screen = %v, want screenBulkError", m.screen)
	}
	if m.fetchErr == nil {
		t.Fatal("expected fetchErr to be set")
	}
}

func TestModeSelect_OneAtATime_GoesToDetectedWhenRemotePresent(t *testing.T) {
	r := &recorder{}
	cfg := r.deps()
	cfg.DetectedRepo = "kunchenguid/ezoss"
	m := NewModel(cfg)

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if m.screen != screenDetected {
		t.Fatalf("screen = %v, want screenDetected", m.screen)
	}
	if len(r.listCalls) != 0 {
		t.Fatalf("ListOwnedRepos should not be called for ModeOneAtATime, calls = %v", r.listCalls)
	}
}

func TestModeSelect_OneAtATime_GoesStraightToManualWithoutRemote(t *testing.T) {
	r := &recorder{}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if m.screen != screenManual {
		t.Fatalf("screen = %v, want screenManual", m.screen)
	}
}

func TestModeSelect_ArrowsThenEnter(t *testing.T) {
	r := &recorder{listRepos: []string{"a/b"}}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyDown})
	m = advance(m, tea.KeyMsg{Type: tea.KeyDown})
	m = advance(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedIdx != 3 {
		t.Fatalf("selectedIdx = %d, want 3", m.selectedIdx)
	}
	m = advance(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != ModeOneAtATime {
		t.Fatalf("mode = %v, want ModeOneAtATime", m.mode)
	}
}

func TestBulkConfirm_YesPersistsRepos(t *testing.T) {
	r := &recorder{listRepos: []string{"kunchenguid/ezoss", "kunchenguid/ezoss", "kunchenguid/no-mistakes"}}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.success {
		t.Fatalf("expected success after y on bulk confirm")
	}
	want := []string{"kunchenguid/ezoss", "kunchenguid/no-mistakes"}
	if !reflect.DeepEqual(m.repos, want) {
		t.Fatalf("repos = %v, want %v (should dedupe)", m.repos, want)
	}
	if m.screen != screenDone {
		t.Fatalf("screen = %v, want screenDone", m.screen)
	}
}

func TestBulkConfirm_NoReturnsToModeSelect(t *testing.T) {
	r := &recorder{listRepos: []string{"a/b"}}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.screen != screenMode {
		t.Fatalf("screen = %v, want screenMode after declining bulk confirm", m.screen)
	}
	if m.mode != ModeNone {
		t.Fatalf("mode = %v, want ModeNone after returning to select", m.mode)
	}
}

func TestBulkFetchError_ShowsRetryAndBack(t *testing.T) {
	r := &recorder{listErr: errors.New("not authenticated")}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.screen != screenBulkError {
		t.Fatalf("screen = %v, want screenBulkError", m.screen)
	}
	if m.fetchErr == nil {
		t.Fatal("expected fetchErr to be set")
	}

	r.listErr = nil
	r.listRepos = []string{"a/b"}
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.screen != screenBulkConfirm {
		t.Fatalf("screen = %v, want screenBulkConfirm after retry", m.screen)
	}
}

func TestBulkEmpty_BackReturnsToModeSelect(t *testing.T) {
	r := &recorder{listRepos: nil}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.screen != screenBulkEmpty {
		t.Fatalf("screen = %v, want screenBulkEmpty", m.screen)
	}
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if m.screen != screenMode {
		t.Fatalf("screen = %v, want screenMode after back", m.screen)
	}
}

func TestDetected_YAddsDetectedRepo(t *testing.T) {
	r := &recorder{}
	cfg := r.deps()
	cfg.DetectedRepo = "kunchenguid/ezoss"
	m := NewModel(cfg)

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.success {
		t.Fatal("expected success")
	}
	if !reflect.DeepEqual(m.repos, []string{"kunchenguid/ezoss"}) {
		t.Fatalf("repos = %v, want [kunchenguid/ezoss]", m.repos)
	}
}

func TestDetected_MFallsThroughToManual(t *testing.T) {
	r := &recorder{}
	cfg := r.deps()
	cfg.DetectedRepo = "kunchenguid/ezoss"
	m := NewModel(cfg)

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if m.screen != screenManual {
		t.Fatalf("screen = %v, want screenManual", m.screen)
	}
}

func TestManual_SubmitsValidRepo(t *testing.T) {
	r := &recorder{}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	for _, r := range "kunchenguid/ezoss" {
		m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = advance(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.success {
		t.Fatal("expected success on valid manual entry")
	}
	if !reflect.DeepEqual(m.repos, []string{"kunchenguid/ezoss"}) {
		t.Fatalf("repos = %v, want [kunchenguid/ezoss]", m.repos)
	}
}

func TestManual_RejectsInvalidRepoAndStaysOnScreen(t *testing.T) {
	r := &recorder{}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	for _, r := range "not-a-repo" {
		m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = advance(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenManual {
		t.Fatalf("screen = %v, want screenManual after invalid entry", m.screen)
	}
	if m.success {
		t.Fatal("invalid entry should not mark success")
	}
}

func TestModeSelect_QAborts(t *testing.T) {
	r := &recorder{}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !m.aborted {
		t.Fatal("expected aborted=true")
	}
	if m.success {
		t.Fatal("aborted run should not be success")
	}
}

func TestTelemetry_RecordsModeAndCompletion(t *testing.T) {
	r := &recorder{listRepos: []string{"kunchenguid/ezoss"}}
	m := NewModel(r.deps())

	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(r.telemetry) < 2 {
		t.Fatalf("telemetry events = %d, want at least 2", len(r.telemetry))
	}
	if r.telemetry[0].action != "mode_selected" {
		t.Fatalf("first event action = %q, want mode_selected", r.telemetry[0].action)
	}
	completion := r.telemetry[len(r.telemetry)-1]
	if completion.action != "completed" {
		t.Fatalf("last event action = %q, want completed", completion.action)
	}
	if got := completion.fields["count"]; got != 1 {
		t.Fatalf("completed count = %v, want 1", got)
	}
}

func TestResult_ReturnsImmutableCopy(t *testing.T) {
	r := &recorder{listRepos: []string{"a/b"}}
	m := NewModel(r.deps())
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = advance(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	res := m.Result()
	if len(res.Repos) > 0 {
		res.Repos[0] = "mutated/value"
	}
	if m.repos[0] == "mutated/value" {
		t.Fatal("Result() should hand back a copy, not share the underlying slice")
	}
}
