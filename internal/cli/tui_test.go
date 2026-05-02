package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/agent"
	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/telemetry"
	"github.com/kunchenguid/ezoss/internal/triage"
	"github.com/kunchenguid/ezoss/internal/tui"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
	_ "modernc.org/sqlite"
)

type capturedLabelEdit struct {
	RepoID string
	Kind   sharedtypes.ItemKind
	Number int
	Add    []string
	Remove []string
}

type capturedComment struct {
	RepoID string
	Kind   sharedtypes.ItemKind
	Number int
	Body   string
}

type capturedClose struct {
	RepoID  string
	Kind    sharedtypes.ItemKind
	Number  int
	Comment string
}

type capturedRequestChanges struct {
	RepoID string
	Number int
	Body   string
}

type capturedMerge struct {
	RepoID string
	Number int
	Method string
}

type telemetryEvent struct {
	Name   string
	Fields telemetry.Fields
}

type telemetrySinkStub struct {
	events []telemetryEvent
}

func (s *telemetrySinkStub) Track(name string, fields telemetry.Fields) {
	cloned := make(telemetry.Fields, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	s.events = append(s.events, telemetryEvent{Name: name, Fields: cloned})
}

func (s *telemetrySinkStub) Pageview(string, telemetry.Fields) {}

func (s *telemetrySinkStub) Close(context.Context) error { return nil }

type stubLabelEditor struct {
	entries []capturedLabelEdit
	err     error
}

func (s *stubLabelEditor) EditLabels(_ context.Context, repo string, kind sharedtypes.ItemKind, number int, add []string, remove []string) error {
	s.entries = append(s.entries, capturedLabelEdit{
		RepoID: repo,
		Kind:   kind,
		Number: number,
		Add:    append([]string(nil), add...),
		Remove: append([]string(nil), remove...),
	})
	return s.err
}

type stubApprovalExecutor struct {
	labelErr   error
	commentErr error
	closeErr   error
	reviewErr  error
	mergeErr   error
	labels     []capturedLabelEdit
	comments   []capturedComment
	closes     []capturedClose
	reviews    []capturedRequestChanges
	merges     []capturedMerge
}

type stubDraftEditor struct {
	content string
	err     error
	inputs  []string
}

func (s *stubDraftEditor) Prepare(_ context.Context, initial string) (*exec.Cmd, func(error) (string, error), error) {
	s.inputs = append(s.inputs, initial)
	if s.err != nil {
		return nil, nil, s.err
	}
	finish := func(error) (string, error) {
		return s.content, nil
	}
	return nil, finish, nil
}

func (s *stubApprovalExecutor) EditLabels(_ context.Context, repo string, kind sharedtypes.ItemKind, number int, add []string, remove []string) error {
	s.labels = append(s.labels, capturedLabelEdit{
		RepoID: repo,
		Kind:   kind,
		Number: number,
		Add:    append([]string(nil), add...),
		Remove: append([]string(nil), remove...),
	})
	return s.labelErr
}

func (s *stubApprovalExecutor) Comment(_ context.Context, repo string, kind sharedtypes.ItemKind, number int, body string) error {
	s.comments = append(s.comments, capturedComment{RepoID: repo, Kind: kind, Number: number, Body: body})
	return s.commentErr
}

func (s *stubApprovalExecutor) Close(_ context.Context, repo string, kind sharedtypes.ItemKind, number int, comment string) error {
	s.closes = append(s.closes, capturedClose{RepoID: repo, Kind: kind, Number: number, Comment: comment})
	return s.closeErr
}

func (s *stubApprovalExecutor) RequestChanges(_ context.Context, repo string, number int, body string) error {
	s.reviews = append(s.reviews, capturedRequestChanges{RepoID: repo, Number: number, Body: body})
	return s.reviewErr
}

func (s *stubApprovalExecutor) Merge(_ context.Context, repo string, number int, method string) (string, error) {
	s.merges = append(s.merges, capturedMerge{RepoID: repo, Number: number, Method: method})
	return method, s.mergeErr
}

func TestRootCommandLaunchesTUIOnNoArgs(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalRunTUI := runTUI
	originalStartBackgroundUpdateCheck := startBackgroundUpdateCheck
	originalIsInteractiveTerminal := isInteractiveTerminal
	telemetrySink := &telemetrySinkStub{}
	resetTelemetry := telemetry.SetDefaultForTesting(telemetrySink)
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runTUI = originalRunTUI
		startBackgroundUpdateCheck = originalStartBackgroundUpdateCheck
		isInteractiveTerminal = originalIsInteractiveTerminal
		resetTelemetry()
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	isInteractiveTerminal = func() bool { return true }

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	firstRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}
	if err := database.MarkRecommendationSuperseded(firstRec.ID, time.Unix(firstRec.CreatedAt+1, 0)); err != nil {
		t.Fatalf("MarkRecommendationSuperseded() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  250,
		TokensOut: 30,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Second pass after more context.",
			DraftComment:   "Please also share the daemon log.",
			Followups:      []string{"Check whether the regression started after the queue refactor."},
			ProposedLabels: []string{"bug", "needs-repro"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceHigh,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	var got []tui.Entry
	updateCheckCalls := 0
	runTUI = func(entries []tui.Entry) error {
		got = append([]tui.Entry(nil), entries...)
		return nil
	}
	startBackgroundUpdateCheck = func() {
		updateCheckCalls++
	}

	cmd := NewRootCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("runTUI entries = %d, want 1", len(got))
	}
	if got[0].RepoID != "acme/widgets" || got[0].Number != 42 {
		t.Fatalf("runTUI entry = %#v", got[0])
	}
	if got[0].DraftComment != "Please also share the daemon log." {
		t.Fatalf("draft comment = %q, want %q", got[0].DraftComment, "Please also share the daemon log.")
	}
	if got[0].OriginalDraftComment != got[0].DraftComment {
		t.Fatalf("original draft comment = %q, want %q", got[0].OriginalDraftComment, got[0].DraftComment)
	}
	if len(got[0].Followups) != 1 || got[0].Followups[0] != "Check whether the regression started after the queue refactor." {
		t.Fatalf("followups = %#v", got[0].Followups)
	}
	if got[0].OriginalStateChange != got[0].StateChange {
		t.Fatalf("original proposed action = %q, want %q", got[0].OriginalStateChange, got[0].StateChange)
	}
	if len(got[0].OriginalProposedLabels) != len(got[0].ProposedLabels) {
		t.Fatalf("original proposed labels = %v, want %v", got[0].OriginalProposedLabels, got[0].ProposedLabels)
	}
	for i := range got[0].ProposedLabels {
		if got[0].OriginalProposedLabels[i] != got[0].ProposedLabels[i] {
			t.Fatalf("original proposed labels = %v, want %v", got[0].OriginalProposedLabels, got[0].ProposedLabels)
		}
	}
	if got[0].Edited() {
		t.Fatal("Edited() = true, want false for a freshly loaded inbox entry")
	}
	if got[0].TokensIn != 350 || got[0].TokensOut != 50 {
		t.Fatalf("token totals = %d in / %d out, want 350 in / 50 out", got[0].TokensIn, got[0].TokensOut)
	}
	if got[0].CurrentWaitingOn != sharedtypes.WaitingOnContributor {
		t.Fatalf("current waiting_on = %q, want %q", got[0].CurrentWaitingOn, sharedtypes.WaitingOnContributor)
	}
	if updateCheckCalls != 1 {
		t.Fatalf("background update checks = %d, want 1", updateCheckCalls)
	}
	if len(telemetrySink.events) != 1 {
		t.Fatalf("telemetry events = %d, want 1", len(telemetrySink.events))
	}
	if telemetrySink.events[0].Name != "command" {
		t.Fatalf("telemetry event name = %q, want %q", telemetrySink.events[0].Name, "command")
	}
	if telemetrySink.events[0].Fields["command"] != "inbox" {
		t.Fatalf("telemetry command = %v, want %q", telemetrySink.events[0].Fields["command"], "inbox")
	}
	if telemetrySink.events[0].Fields["entrypoint"] != "root" {
		t.Fatalf("telemetry entrypoint = %v, want %q", telemetrySink.events[0].Fields["entrypoint"], "root")
	}
}

func TestRootCommandExitsWhenInboxEmptyAndDaemonNotRunning(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalRunTUI := runTUI
	originalReadDaemonStatus := readDaemonStatus
	originalIsInteractiveTerminal := isInteractiveTerminal
	originalStartBackgroundUpdateCheck := startBackgroundUpdateCheck
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runTUI = originalRunTUI
		readDaemonStatus = originalReadDaemonStatus
		isInteractiveTerminal = originalIsInteractiveTerminal
		startBackgroundUpdateCheck = originalStartBackgroundUpdateCheck
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	isInteractiveTerminal = func() bool { return true }
	readDaemonStatus = func(string) (daemon.Status, error) {
		return daemon.Status{State: daemon.StateStopped}, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	runTUICalls := 0
	runTUI = func([]tui.Entry) error {
		runTUICalls++
		return nil
	}
	updateCheckCalls := 0
	startBackgroundUpdateCheck = func() {
		updateCheckCalls++
	}

	cmd := NewRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if runTUICalls != 0 {
		t.Fatalf("runTUI called %d times, want 0", runTUICalls)
	}
	if updateCheckCalls != 0 {
		t.Fatalf("startBackgroundUpdateCheck called %d times, want 0", updateCheckCalls)
	}
	msg := err.Error()
	if !strings.Contains(msg, "not running") {
		t.Fatalf("error = %q, want to contain %q", msg, "not running")
	}
	if !strings.Contains(msg, "ezoss daemon start") {
		t.Fatalf("error = %q, want to contain %q", msg, "ezoss daemon start")
	}
}

func TestRootCommandLaunchesTUIWhenInboxEmptyAndDaemonRunning(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalRunTUI := runTUI
	originalReadDaemonStatus := readDaemonStatus
	originalIsInteractiveTerminal := isInteractiveTerminal
	originalStartBackgroundUpdateCheck := startBackgroundUpdateCheck
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runTUI = originalRunTUI
		readDaemonStatus = originalReadDaemonStatus
		isInteractiveTerminal = originalIsInteractiveTerminal
		startBackgroundUpdateCheck = originalStartBackgroundUpdateCheck
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	isInteractiveTerminal = func() bool { return true }
	readDaemonStatus = func(string) (daemon.Status, error) {
		return daemon.Status{State: daemon.StateRunning}, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	runTUICalls := 0
	runTUI = func([]tui.Entry) error {
		runTUICalls++
		return nil
	}
	startBackgroundUpdateCheck = func() {}

	cmd := NewRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runTUICalls != 1 {
		t.Fatalf("runTUI calls = %d, want 1", runTUICalls)
	}
}

func TestRootCommandFallsBackToListWhenNotATerminal(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalRunTUI := runTUI
	originalIsInteractiveTerminal := isInteractiveTerminal
	originalStartBackgroundUpdateCheck := startBackgroundUpdateCheck
	t.Cleanup(func() {
		newPaths = originalNewPaths
		runTUI = originalRunTUI
		isInteractiveTerminal = originalIsInteractiveTerminal
		startBackgroundUpdateCheck = originalStartBackgroundUpdateCheck
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	isInteractiveTerminal = func() bool { return false }
	runTUICalls := 0
	runTUI = func([]tui.Entry) error {
		runTUICalls++
		return nil
	}
	startBackgroundUpdateCheck = func() {}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	stdout := &strings.Builder{}
	cmd := NewRootCmd()
	cmd.SetOut(stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"ITEM",
		"KIND   ACTION   CONFIDENCE  AGE  TITLE                     URL",
		"acme/widgets#42",
		"issue  comment  medium",
		"https://github.com/acme/widgets/issues/42",
		"1 pending recommendation. Re-run `ezoss` in a terminal to review in the TUI.",
		"warning: daemon is not running; this inbox will not refresh until you run `ezoss daemon start`.",
		"note: 1 recommendation is for a repo not in your config (acme/widgets); the daemon will not refresh it.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q does not contain %q", output, want)
		}
	}
	if runTUICalls != 0 {
		t.Fatalf("runTUI called %d times, want 0 when stdin is not a terminal", runTUICalls)
	}
}

func TestInboxModelActionsReloadLoadsUpdatedEntries(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	actions := inboxModelActions(nil)
	entries, err := actions.Reload()
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].RepoID != "acme/widgets" || entries[0].Number != 42 {
		t.Fatalf("entry = %#v", entries[0])
	}
}

func TestSubscribeInboxNotificationsEmitsSignalForRecommendationEvents(t *testing.T) {
	tempRoot := t.TempDir()
	resolvedPaths := paths.WithRoot(tempRoot)
	originalNewPaths := newPaths
	originalIPCSubscribe := ipcSubscribe
	t.Cleanup(func() {
		newPaths = originalNewPaths
		ipcSubscribe = originalIPCSubscribe
	})
	newPaths = func() (*paths.Paths, error) {
		return resolvedPaths, nil
	}

	events := make(chan ipc.Event, 4)
	var subscribedPath string
	var canceled bool
	ipcSubscribe = func(socketPath string, params *ipc.SubscribeParams) (<-chan ipc.Event, func(), error) {
		subscribedPath = socketPath
		return events, func() { canceled = true }, nil
	}

	notify, cancel, err := subscribeInboxNotifications()
	if err != nil {
		t.Fatalf("subscribeInboxNotifications() error = %v", err)
	}
	t.Cleanup(cancel)

	if subscribedPath != resolvedPaths.IPCPath() {
		t.Fatalf("subscribe path = %q, want %q", subscribedPath, resolvedPaths.IPCPath())
	}

	events <- ipc.Event{Type: ipc.EventRecommendationCreated}
	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for inbox notification")
	}

	events <- ipc.Event{Type: ipc.EventDaemonStatus}
	select {
	case <-notify:
		t.Fatal("received notification for non-recommendation event")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	if !canceled {
		t.Fatal("cancel did not close ipc subscription")
	}
}

func TestOpenInboxTUIShowsStatusWhenLiveUpdatesUnavailable(t *testing.T) {
	tempRoot := t.TempDir()
	resolvedPaths := paths.WithRoot(tempRoot)
	originalNewPaths := newPaths
	originalIPCSubscribe := ipcSubscribe
	originalRunTUIWithActions := runTUIWithActions
	originalReadDaemonStatus := readDaemonStatus
	t.Cleanup(func() {
		newPaths = originalNewPaths
		ipcSubscribe = originalIPCSubscribe
		runTUIWithActions = originalRunTUIWithActions
		readDaemonStatus = originalReadDaemonStatus
	})
	newPaths = func() (*paths.Paths, error) {
		return resolvedPaths, nil
	}
	readDaemonStatus = func(string) (daemon.Status, error) {
		return daemon.Status{State: daemon.StateStopped}, nil
	}

	subscribeErr := errors.New("dial ipc: connection refused")
	ipcSubscribe = func(string, *ipc.SubscribeParams) (<-chan ipc.Event, func(), error) {
		return nil, nil, subscribeErr
	}

	var capturedActions tui.ModelActions
	runTUIWithActions = func(entries []tui.Entry, actions tui.ModelActions) error {
		capturedActions = actions
		return nil
	}

	if err := openInboxTUI(nil); err != nil {
		t.Fatalf("openInboxTUI() error = %v", err)
	}

	if capturedActions.Notify != nil {
		t.Fatal("Notify should be nil when live update subscription fails")
	}
	if capturedActions.InitialStatus == "" {
		t.Fatal("InitialStatus should describe degraded live update mode")
	}
	if !strings.Contains(capturedActions.InitialStatus, "daemon is not running") {
		t.Fatalf("InitialStatus = %q, want it to contain daemon warning", capturedActions.InitialStatus)
	}
	if !strings.Contains(capturedActions.InitialStatus, subscribeErr.Error()) {
		t.Fatalf("InitialStatus = %q, want it to contain %q", capturedActions.InitialStatus, subscribeErr.Error())
	}
}

func TestDismissInboxEntriesMarksRecommendationDismissedAndTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewLabelEditor := newLabelEditor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newLabelEditor = originalNewLabelEditor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	editor := &stubLabelEditor{}
	newLabelEditor = func() labelEditor {
		return editor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := dismissInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
	}}); err != nil {
		t.Fatalf("dismissInboxEntries() error = %v", err)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after dismiss = %#v, want gh_triaged=true", item)
	}
	if len(editor.entries) != 1 {
		t.Fatalf("label edits = %d, want 1", len(editor.entries))
	}
	wantAdd := []string{ezossTriagedLabel, "ezoss/awaiting-contributor"}
	if len(editor.entries[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if editor.entries[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
		}
	}
}

func TestDismissInboxEntriesStillMarksTriagedWhenSyncDisabled(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewLabelEditor := newLabelEditor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newLabelEditor = originalNewLabelEditor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	editor := &stubLabelEditor{}
	newLabelEditor = func() labelEditor {
		return editor
	}

	configPath := filepath.Join(tempRoot, "config.yaml")
	if err := config.SaveGlobal(configPath, &config.GlobalConfig{SyncLabels: config.SyncLabels{Triaged: false, WaitingOn: true, Stale: true}}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := dismissInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
	}}); err != nil {
		t.Fatalf("dismissInboxEntries() error = %v", err)
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after dismiss = %#v, want gh_triaged=true", item)
	}
	if len(editor.entries) != 1 {
		t.Fatalf("label edits = %d, want 1", len(editor.entries))
	}
	wantAdd := []string{ezossTriagedLabel, "ezoss/awaiting-contributor"}
	if len(editor.entries[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if editor.entries[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
		}
	}

	approvals, err := database.ListApprovalsForRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("ListApprovalsForRecommendation() error = %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("len(approvals) = %d, want 1", len(approvals))
	}
	if len(approvals[0].FinalLabels) != len(wantAdd) {
		t.Fatalf("dismissed final labels = %#v, want %#v", approvals[0].FinalLabels, wantAdd)
	}
	for i, label := range wantAdd {
		if approvals[0].FinalLabels[i] != label {
			t.Fatalf("dismissed final labels = %#v, want %#v", approvals[0].FinalLabels, wantAdd)
		}
	}
}

func TestApproveInboxEntriesStillMarksTriagedWhenSyncDisabled(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	configPath := filepath.Join(tempRoot, "config.yaml")
	if err := config.SaveGlobal(configPath, &config.GlobalConfig{SyncLabels: config.SyncLabels{Triaged: false, WaitingOn: true, Stale: true}}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#43",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    43,
		Title:     "Bug: approval path retriggers",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#43",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs contributor follow-up before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           43,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Can you share a minimal repro?",
		ProposedLabels:   []string{"bug"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	item, err := database.GetItem("acme/widgets#43")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantAdd := []string{"bug", ezossTriagedLabel, "ezoss/awaiting-contributor"}
	if len(executor.labels[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
		}
	}
}

func TestDismissInboxEntriesRemovesObsoleteManagedLabels(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewLabelEditor := newLabelEditor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newLabelEditor = originalNewLabelEditor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	editor := &stubLabelEditor{}
	newLabelEditor = func() labelEditor {
		return editor
	}

	configPath := filepath.Join(tempRoot, "config.yaml")
	if err := config.SaveGlobal(configPath, &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	staleSince := time.Unix(1713511200, 0).UTC()
	if err := database.UpsertItem(db.Item{
		ID:         "acme/widgets#42",
		RepoID:     "acme/widgets",
		Kind:       sharedtypes.ItemKindIssue,
		Number:     42,
		Title:      "Bug: triage queue stalls",
		State:      sharedtypes.ItemStateOpen,
		WaitingOn:  sharedtypes.WaitingOnMaintainer,
		StaleSince: &staleSince,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs maintainer triage.",
			DraftComment:   "",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := dismissInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
	}}); err != nil {
		t.Fatalf("dismissInboxEntries() error = %v", err)
	}

	if len(editor.entries) != 1 {
		t.Fatalf("label edits = %d, want 1", len(editor.entries))
	}
	wantAdd := []string{ezossTriagedLabel, "ezoss/awaiting-maintainer", ezossStaleLabel}
	if len(editor.entries[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if editor.entries[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
		}
	}
	wantRemove := []string{"ezoss/awaiting-contributor"}
	if len(editor.entries[0].Remove) != len(wantRemove) {
		t.Fatalf("removed labels = %#v, want %#v", editor.entries[0].Remove, wantRemove)
	}
	for i, label := range wantRemove {
		if editor.entries[0].Remove[i] != label {
			t.Fatalf("removed labels = %#v, want %#v", editor.entries[0].Remove, wantRemove)
		}
	}
}

func TestDismissInboxEntriesDoesNotApplyRecommendationLabels(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewLabelEditor := newLabelEditor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newLabelEditor = originalNewLabelEditor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	editor := &stubLabelEditor{}
	newLabelEditor = func() labelEditor {
		return editor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#26",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    26,
		Title:     "Question about config",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#26",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "This looks like a docs question.",
			DraftComment:   "Can you share your config file?",
			ProposedLabels: []string{"question", "needs-info"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := dismissInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           26,
		Kind:             sharedtypes.ItemKindIssue,
		ProposedLabels:   []string{"question", "needs-info"},
	}}); err != nil {
		t.Fatalf("dismissInboxEntries() error = %v", err)
	}

	if len(editor.entries) != 1 {
		t.Fatalf("label edits = %d, want 1", len(editor.entries))
	}
	wantAdd := []string{ezossTriagedLabel, "ezoss/awaiting-maintainer"}
	if len(editor.entries[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if editor.entries[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", editor.entries[0].Add, wantAdd)
		}
	}

	approvals, err := database.ListApprovalsForRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("ListApprovalsForRecommendation() error = %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("len(approvals) = %d, want 1", len(approvals))
	}
	if len(approvals[0].FinalLabels) != len(wantAdd) {
		t.Fatalf("dismissed final labels = %#v, want %#v", approvals[0].FinalLabels, wantAdd)
	}
	for i, label := range wantAdd {
		if approvals[0].FinalLabels[i] != label {
			t.Fatalf("dismissed final labels = %#v, want %#v", approvals[0].FinalLabels, wantAdd)
		}
	}
}

func TestApproveInboxEntriesPersistsFailedApprovalAttempt(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{commentErr: errors.New("gh comment failed")}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	configPath := filepath.Join(tempRoot, "config.yaml")
	if err := config.SaveGlobal(configPath, &config.GlobalConfig{}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	primary := rec.Options[0]
	entry := tui.Entry{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		Options: []tui.EntryOption{{
			ID:                     primary.ID,
			StateChange:            primary.StateChange,
			OriginalStateChange:    primary.StateChange,
			ProposedLabels:         append([]string(nil), primary.ProposedLabels...),
			OriginalProposedLabels: append([]string(nil), primary.ProposedLabels...),
			DraftComment:           primary.DraftComment,
			OriginalDraftComment:   primary.DraftComment,
		}},
	}
	entry.SyncActive()
	err = approveInboxEntries(context.Background(), []tui.Entry{entry})
	if err == nil {
		t.Fatal("expected approveInboxEntries() to fail")
	}
	if got := err.Error(); got != "comment on acme/widgets#42: gh comment failed" {
		t.Fatalf("approveInboxEntries() error = %q", got)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 1 || active[0].ID != rec.ID {
		t.Fatalf("active recommendations = %#v, want original active recommendation", active)
	}

	approvals, err := database.ListApprovalsForRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("ListApprovalsForRecommendation() error = %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("len(approvals) = %d, want 1", len(approvals))
	}
	if approvals[0].Decision != sharedtypes.ApprovalDecisionApproved {
		t.Fatalf("approval decision = %q, want %q", approvals[0].Decision, sharedtypes.ApprovalDecisionApproved)
	}
	if approvals[0].ActedError != "comment on acme/widgets#42: gh comment failed" {
		t.Fatalf("approval acted_error = %q", approvals[0].ActedError)
	}
	if approvals[0].ActedAt == nil {
		t.Fatal("expected acted_at to be recorded")
	}

	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || item.GHTriaged {
		t.Fatalf("item after failed approval = %#v, want gh_triaged=false", item)
	}
}

func TestLoadInboxEntriesIncludesLatestApprovalError(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "acme/widgets#42",
		RepoID: "acme/widgets",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			DraftComment: "Can you share a minimal repro?",
			StateChange:  sharedtypes.StateChangeNone,
			Confidence:   sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}
	actedAt := time.Unix(1713000000, 0)
	primary := rec.Options[0]
	if _, err := database.InsertApproval(db.NewApproval{
		RecommendationID: rec.ID,
		OptionID:         primary.ID,
		Decision:         sharedtypes.ApprovalDecisionApproved,
		FinalComment:     primary.DraftComment,
		FinalStateChange: primary.StateChange,
		ActedAt:          &actedAt,
		ActedError:       "comment on acme/widgets#42: gh comment failed",
	}); err != nil {
		t.Fatalf("InsertApproval() error = %v", err)
	}

	entries, err := loadInboxEntries()
	if err != nil {
		t.Fatalf("loadInboxEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].ApprovalError != "comment on acme/widgets#42: gh comment failed" {
		t.Fatalf("ApprovalError = %q", entries[0].ApprovalError)
	}
}

func TestLoadInboxEntriesPopulatesGitHubURLForIssuesAndPRs(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "issue", Author: "alice", State: sharedtypes.ItemStateOpen, WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "acme/widgets#7", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindPR, Number: 7, Title: "pr", Author: "bob", State: sharedtypes.ItemStateOpen, WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#42", Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium, FixPrompt: "Fix https://github.com/acme/widgets/issues/42 and add a regression test."}}}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#7", Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeMerge, Confidence: sharedtypes.ConfidenceHigh}}}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	entries, err := loadInboxEntries()
	if err != nil {
		t.Fatalf("loadInboxEntries() error = %v", err)
	}
	urls := map[string]string{}
	authors := map[string]string{}
	currentWaitingOn := map[string]sharedtypes.WaitingOn{}
	for _, entry := range entries {
		key := fmt.Sprintf("%s#%d", entry.RepoID, entry.Number)
		urls[key] = entry.URL
		authors[key] = entry.Author
		currentWaitingOn[key] = entry.CurrentWaitingOn
	}
	if got, want := urls["acme/widgets#42"], "https://github.com/acme/widgets/issues/42"; got != want {
		t.Fatalf("issue URL = %q, want %q", got, want)
	}
	if got, want := urls["acme/widgets#7"], "https://github.com/acme/widgets/pull/7"; got != want {
		t.Fatalf("PR URL = %q, want %q", got, want)
	}
	if got, want := authors["acme/widgets#42"], "alice"; got != want {
		t.Fatalf("issue author = %q, want %q", got, want)
	}
	if got, want := authors["acme/widgets#7"], "bob"; got != want {
		t.Fatalf("PR author = %q, want %q", got, want)
	}
	if got, want := currentWaitingOn["acme/widgets#42"], sharedtypes.WaitingOnMaintainer; got != want {
		t.Fatalf("issue CurrentWaitingOn = %q, want %q", got, want)
	}
	if got, want := currentWaitingOn["acme/widgets#7"], sharedtypes.WaitingOnContributor; got != want {
		t.Fatalf("PR CurrentWaitingOn = %q, want %q", got, want)
	}
	prompts := map[string]string{}
	for _, entry := range entries {
		prompts[fmt.Sprintf("%s#%d", entry.RepoID, entry.Number)] = entry.FixPrompt
	}
	if got := prompts["acme/widgets#42"]; !strings.Contains(got, "https://github.com/acme/widgets/issues/42") {
		t.Fatalf("issue FixPrompt = %q, want original issue URL", got)
	}
}

func TestCopyTextWithSystemClipboardRejectsEmptyPrompt(t *testing.T) {
	err := copyTextWithSystemClipboard(context.Background(), "  \n\t")
	if err == nil {
		t.Fatal("expected empty prompt error")
	}
	if !strings.Contains(err.Error(), "prompt is empty") {
		t.Fatalf("error = %q, want empty prompt", err.Error())
	}
}

func TestOpenURLWithSystemBrowserRejectsEmptyURL(t *testing.T) {
	err := openURLWithSystemBrowser(context.Background(), "  \n\t")
	if err == nil {
		t.Fatal("expected empty url error")
	}
	if !strings.Contains(err.Error(), "url is empty") {
		t.Fatalf("error = %q, want empty url", err.Error())
	}
}

func TestOpenURLCommandsIncludesURLForCurrentPlatform(t *testing.T) {
	commands := openURLCommands("https://github.com/acme/widgets/issues/42")
	if len(commands) == 0 {
		t.Fatal("expected browser-open command for test platform")
	}
	last := commands[0][len(commands[0])-1]
	if last != "https://github.com/acme/widgets/issues/42" {
		t.Fatalf("command = %#v, want URL as final argument", commands[0])
	}
}

func TestInboxModelActionsOpenURLUsesEntryURL(t *testing.T) {
	originalOpenURLInBrowser := openURLInBrowser
	t.Cleanup(func() {
		openURLInBrowser = originalOpenURLInBrowser
	})

	var opened string
	openURLInBrowser = func(_ context.Context, url string) error {
		opened = url
		return nil
	}

	actions := inboxModelActions(nil)
	err := actions.OpenURL(tui.Entry{URL: "https://github.com/acme/widgets/issues/42"})
	if err != nil {
		t.Fatalf("OpenURL() error = %v", err)
	}
	if opened != "https://github.com/acme/widgets/issues/42" {
		t.Fatalf("opened = %q, want entry URL", opened)
	}
}

func TestLoadInboxEntriesKeepsNewestRecommendationsFirst(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "zeta/repo", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo(zeta) error = %v", err)
	}
	if err := database.UpsertRepo(db.Repo{ID: "alpha/repo", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo(alpha) error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "alpha/repo#1", RepoID: "alpha/repo", Kind: sharedtypes.ItemKindIssue, Number: 1, Title: "older", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem(older) error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "zeta/repo#2", RepoID: "zeta/repo", Kind: sharedtypes.ItemKindIssue, Number: 2, Title: "newer", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem(newer) error = %v", err)
	}

	older, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "alpha/repo#1", Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium}}})
	if err != nil {
		t.Fatalf("InsertRecommendation(older) error = %v", err)
	}
	newer, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "zeta/repo#2", Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceHigh}}})
	if err != nil {
		t.Fatalf("InsertRecommendation(newer) error = %v", err)
	}
	sqlDB, err := sql.Open("sqlite", filepath.Join(tempRoot, "ezoss.db")+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("sqlDB.Close() error = %v", err)
		}
	})

	if _, err := sqlDB.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, time.Now().Add(-2*time.Minute).Unix(), older.ID); err != nil {
		t.Fatalf("set older created_at: %v", err)
	}
	if _, err := sqlDB.Exec(`UPDATE recommendations SET created_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).Unix(), newer.ID); err != nil {
		t.Fatalf("set newer created_at: %v", err)
	}

	entries, err := loadInboxEntries()
	if err != nil {
		t.Fatalf("loadInboxEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if got := entries[0].RepoID; got != "zeta/repo" {
		t.Fatalf("entries[0].RepoID = %q, want newest recommendation repo zeta/repo", got)
	}
	if got := entries[1].RepoID; got != "alpha/repo" {
		t.Fatalf("entries[1].RepoID = %q, want older recommendation repo alpha/repo", got)
	}
}

func TestLoadInboxEntriesMarksUnconfiguredRecommendations(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	t.Cleanup(func() {
		newPaths = originalNewPaths
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{Repos: []string{"acme/widgets"}}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo(configured) error = %v", err)
	}
	if err := database.UpsertRepo(db.Repo{ID: "orphan/repo", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo(unconfigured) error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "acme/widgets#42", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 42, Title: "configured", State: sharedtypes.ItemStateOpen}); err != nil {
		t.Fatalf("UpsertItem(configured) error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "orphan/repo#7", RepoID: "orphan/repo", Kind: sharedtypes.ItemKindIssue, Number: 7, Title: "orphan", State: sharedtypes.ItemStateOpen}); err != nil {
		t.Fatalf("UpsertItem(unconfigured) error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{ItemID: "acme/widgets#42", Agent: sharedtypes.AgentClaude, Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium}}}); err != nil {
		t.Fatalf("InsertRecommendation(configured) error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{ItemID: "orphan/repo#7", Agent: sharedtypes.AgentClaude, Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium}}}); err != nil {
		t.Fatalf("InsertRecommendation(unconfigured) error = %v", err)
	}

	entries, err := loadInboxEntries()
	if err != nil {
		t.Fatalf("loadInboxEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	configured := make(map[string]bool, len(entries))
	for _, entry := range entries {
		configured[entry.RepoID] = entry.Unconfigured
	}
	if configured["acme/widgets"] {
		t.Fatalf("configured repo entry unexpectedly marked unconfigured: %#v", entries)
	}
	if !configured["orphan/repo"] {
		t.Fatalf("unconfigured repo entry was not marked unconfigured: %#v", entries)
	}
}

func TestApproveInboxEntriesRecordsEditedApprovalWhenDraftChanged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:     "acme/widgets#42",
		RepoID: "acme/widgets",
		Kind:   sharedtypes.ItemKindIssue,
		Number: 42,
		Title:  "Bug: triage queue stalls",
		State:  sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	entry := tui.Entry{
		RecommendationID:       rec.ID,
		RepoID:                 "acme/widgets",
		Number:                 42,
		Kind:                   sharedtypes.ItemKindIssue,
		Title:                  "Bug: triage queue stalls",
		DraftComment:           "Can you share a minimal repro and logs?",
		OriginalDraftComment:   "Can you share a minimal repro?",
		ProposedLabels:         []string{"bug"},
		OriginalProposedLabels: []string{"bug"},
		StateChange:            sharedtypes.StateChangeNone,
		OriginalStateChange:    sharedtypes.StateChangeNone,
	}
	if err := approveInboxEntries(context.Background(), []tui.Entry{entry}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	approvals, err := database.ListApprovalsForRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("ListApprovalsForRecommendation() error = %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(approvals))
	}
	if approvals[0].Decision != sharedtypes.ApprovalDecisionEdited {
		t.Fatalf("approval decision = %q, want %q", approvals[0].Decision, sharedtypes.ApprovalDecisionEdited)
	}
	if approvals[0].FinalComment != "Can you share a minimal repro and logs?" {
		t.Fatalf("final comment = %q, want edited draft", approvals[0].FinalComment)
	}
}

func TestEditInboxEntryUpdatesActionLabelsAndDraft(t *testing.T) {
	originalNewDraftEditor := newDraftEditor
	t.Cleanup(func() {
		newDraftEditor = originalNewDraftEditor
	})
	editor := &stubDraftEditor{content: "StateChange: request_changes\nLabels: needs-work, backend\n\nDraft response:\nPlease split this into smaller changes and add tests.\n"}
	newDraftEditor = func() draftEditor {
		return editor
	}

	cmd, finish, err := prepareInboxEntryEdit(context.Background(), tui.Entry{
		RepoID:                 "acme/widgets",
		Number:                 11,
		Kind:                   sharedtypes.ItemKindPR,
		DraftComment:           "Should we discuss the approach first?",
		OriginalDraftComment:   "Should we discuss the approach first?",
		ProposedLabels:         []string{"needs-decision"},
		OriginalProposedLabels: []string{"needs-decision"},
		StateChange:            sharedtypes.StateChangeNone,
		OriginalStateChange:    sharedtypes.StateChangeNone,
	})
	if err != nil {
		t.Fatalf("prepareInboxEntryEdit() error = %v", err)
	}
	if cmd != nil {
		t.Fatalf("stub editor should not return an *exec.Cmd, got %v", cmd)
	}
	entry, err := finish(nil)
	if err != nil {
		t.Fatalf("finish() error = %v", err)
	}

	if len(editor.inputs) != 1 {
		t.Fatalf("editor inputs = %d, want 1", len(editor.inputs))
	}
	if entry.StateChange != sharedtypes.StateChangeRequestChanges {
		t.Fatalf("edited action = %q, want %q", entry.StateChange, sharedtypes.StateChangeRequestChanges)
	}
	if got := entry.ProposedLabels; len(got) != 2 || got[0] != "needs-work" || got[1] != "backend" {
		t.Fatalf("edited labels = %#v, want [needs-work backend]", got)
	}
	if entry.DraftComment != "Please split this into smaller changes and add tests.\n" {
		t.Fatalf("edited draft = %q", entry.DraftComment)
	}
}

func TestApproveInboxEntriesTreatsEditedActionAsEditedApproval(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#11",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindPR,
		Number:    11,
		Title:     "feat: add queue sorting",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#11",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  160,
		TokensOut: 24,
		Options: []db.NewRecommendationOption{{
			Rationale:      "The PR may be valid, but the approach was not previously agreed on.",
			DraftComment:   "Should we discuss the approach first?",
			ProposedLabels: []string{"needs-decision"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID:       rec.ID,
		RepoID:                 "acme/widgets",
		Number:                 11,
		Kind:                   sharedtypes.ItemKindPR,
		DraftComment:           "Please split this into smaller changes and add tests.",
		OriginalDraftComment:   "Should we discuss the approach first?",
		ProposedLabels:         []string{"needs-work"},
		OriginalProposedLabels: []string{"needs-decision"},
		StateChange:            sharedtypes.StateChangeRequestChanges,
		OriginalStateChange:    sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.comments) != 0 {
		t.Fatalf("comments = %d, want 0", len(executor.comments))
	}
	if len(executor.reviews) != 1 {
		t.Fatalf("request changes reviews = %d, want 1", len(executor.reviews))
	}
	if executor.reviews[0].Body != "Please split this into smaller changes and add tests." {
		t.Fatalf("review body = %q", executor.reviews[0].Body)
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	if got := executor.labels[0].Add; len(got) == 0 || got[0] != "needs-work" {
		t.Fatalf("added labels = %#v, want edited labels first", got)
	}

	approvals, err := database.ListApprovalsForRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("ListApprovalsForRecommendation() error = %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(approvals))
	}
	if approvals[0].Decision != sharedtypes.ApprovalDecisionEdited {
		t.Fatalf("approval decision = %q, want %q", approvals[0].Decision, sharedtypes.ApprovalDecisionEdited)
	}
	if approvals[0].FinalStateChange != sharedtypes.StateChangeRequestChanges {
		t.Fatalf("final action = %q, want %q", approvals[0].FinalStateChange, sharedtypes.StateChangeRequestChanges)
	}
	if got := approvals[0].FinalLabels; len(got) == 0 || got[0] != "needs-work" {
		t.Fatalf("final labels = %#v, want edited labels", got)
	}
}

func TestApproveInboxEntriesExecutesCommentRecommendationAndMarksTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Needs a repro before deeper debugging.",
			DraftComment:   "Can you share a minimal repro?",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           42,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Can you share a minimal repro?",
		ProposedLabels:   []string{"bug"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#42")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
	if len(executor.comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(executor.comments))
	}
	if executor.comments[0].Body != "Can you share a minimal repro?" {
		t.Fatalf("comment body = %q, want %q", executor.comments[0].Body, "Can you share a minimal repro?")
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantCommentLabels := []string{"bug", ezossTriagedLabel, "ezoss/awaiting-contributor"}
	if len(executor.labels[0].Add) != len(wantCommentLabels) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantCommentLabels)
	}
	for i, label := range wantCommentLabels {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantCommentLabels)
		}
	}
	approvals, err := database.GetApproval(rec.ID)
	if err == nil && approvals != nil {
		t.Fatalf("GetApproval(rec.ID) unexpectedly returned approval by recommendation id")
	}
	rows, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() after approve error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("active recommendations after approve = %d, want 0", len(rows))
	}
}

func TestRerunInboxEntriesSupersedesRecommendationAndReturnsRefreshedEntry(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	originalNewAgent := newAgent
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
	})
	stubAgentLookPath(t, map[string]string{
		"codex": "/usr/local/bin/codex",
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "acme/widgets" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "acme/widgets")
		}
		return t.TempDir(), nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{item: ghclient.Item{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    42,
			Title:     "Bug: triage queue stalls",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/acme/widgets/issues/42",
			UpdatedAt: time.Unix(1713511200, 0).UTC(),
		}}
	}
	var prompt string
	newAgent = func(name sharedtypes.AgentName, _ string) (triageAgent, error) {
		if name != sharedtypes.AgentCodex {
			t.Fatalf("newAgent() name = %q, want %q", name, sharedtypes.AgentCodex)
		}
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSONTUI(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "The rerun found a clearer request for logs.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Please share the daemon log and the exact failing command.",
					Confidence:   sharedtypes.ConfidenceHigh,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}, onRun: func(opts agent.RunOpts) {
			prompt = opts.Prompt
		}}, nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	oldRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		Model:     "claude",
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Old rationale.",
			DraftComment:   "Old draft.",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	entries, err := rerunInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID:     oldRec.ID,
		RepoID:               "acme/widgets",
		Number:               42,
		Kind:                 sharedtypes.ItemKindIssue,
		Title:                "Bug: triage queue stalls",
		DraftComment:         "Old draft.",
		OriginalDraftComment: "Old draft.",
	}}, "Focus on whether the maintainer's new log changes the waiting_on state.")
	if err != nil {
		t.Fatalf("rerunInboxEntries() error = %v", err)
	}
	if !strings.Contains(prompt, "Maintainer-provided rerun instructions:") || !strings.Contains(prompt, "Focus on whether the maintainer's new log changes the waiting_on state.") {
		t.Fatalf("rerun prompt missing instructions:\n%s", prompt)
	}
	if len(entries) != 1 {
		t.Fatalf("len(rerun entries) = %d, want 1", len(entries))
	}
	if entries[0].RerunInstructions != "Focus on whether the maintainer's new log changes the waiting_on state." {
		t.Fatalf("entry RerunInstructions = %q", entries[0].RerunInstructions)
	}
	if entries[0].Role != sharedtypes.RoleMaintainer {
		t.Fatalf("entry Role = %q, want maintainer", entries[0].Role)
	}
	if entries[0].RecommendationID == oldRec.ID {
		t.Fatalf("rerun RecommendationID = %q, want a new recommendation", entries[0].RecommendationID)
	}
	if entries[0].DraftComment != "Please share the daemon log and the exact failing command." {
		t.Fatalf("rerun DraftComment = %q", entries[0].DraftComment)
	}
	if entries[0].OriginalDraftComment != entries[0].DraftComment {
		t.Fatalf("rerun OriginalDraftComment = %q, want %q", entries[0].OriginalDraftComment, entries[0].DraftComment)
	}
	if entries[0].TokensIn != 1000 || entries[0].TokensOut != 140 {
		t.Fatalf("rerun token totals = %d in / %d out, want 1000 in / 140 out", entries[0].TokensIn, entries[0].TokensOut)
	}

	storedOldRec, err := database.GetRecommendation(oldRec.ID)
	if err != nil {
		t.Fatalf("GetRecommendation(old) error = %v", err)
	}
	if storedOldRec == nil || storedOldRec.SupersededAt == nil {
		t.Fatalf("old recommendation after rerun = %#v, want superseded recommendation", storedOldRec)
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active recommendations = %d, want 1", len(active))
	}
	if active[0].ID != entries[0].RecommendationID {
		t.Fatalf("active recommendation id = %q, want %q", active[0].ID, entries[0].RecommendationID)
	}
}

func TestLoadInboxEntryPreservesContributorRole(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	if err := database.UpsertRepo(db.Repo{ID: "upstream/widgets"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{ID: "upstream/widgets#12", RepoID: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Number: 12, Role: sharedtypes.RoleContributor, State: sharedtypes.ItemStateOpen}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	if _, err := database.InsertRecommendation(db.NewRecommendation{ItemID: "upstream/widgets#12", Agent: sharedtypes.AgentClaude, Options: []db.NewRecommendationOption{{Rationale: "Needs follow-up.", StateChange: sharedtypes.StateChangeNone, Confidence: sharedtypes.ConfidenceMedium}}}); err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	entry, err := loadInboxEntry(database, "upstream/widgets", 12)
	if err != nil {
		t.Fatalf("loadInboxEntry() error = %v", err)
	}
	if entry == nil {
		t.Fatal("loadInboxEntry() = nil")
	}
	if entry.Role != sharedtypes.RoleContributor {
		t.Fatalf("entry Role = %q, want contributor", entry.Role)
	}
}

func TestRerunInboxEntriesPrefersRepoAgentOverride(t *testing.T) {
	tempRoot := t.TempDir()
	workingDir := t.TempDir()
	originalNewPaths := newPaths
	originalNewGitHubClient := newGitHubClient
	originalNewAgent := newAgent
	originalPrepareInvestigationCheckout := prepareInvestigationCheckout
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newGitHubClient = originalNewGitHubClient
		newAgent = originalNewAgent
		prepareInvestigationCheckout = originalPrepareInvestigationCheckout
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	stubAgentLookPath(t, map[string]string{
		"claude": "/usr/local/bin/claude",
		"codex":  "/usr/local/bin/codex",
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	prepareInvestigationCheckout = func(_ context.Context, root string, repo string) (string, error) {
		if root != tempRoot || repo != "acme/widgets" {
			t.Fatalf("prepareInvestigationCheckout(root, repo) = (%q, %q), want (%q, %q)", root, repo, tempRoot, "acme/widgets")
		}
		return workingDir, nil
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		Agent: sharedtypes.AgentCodex,
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workingDir, ".ezoss.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.ezoss.yaml) error = %v", err)
	}

	newGitHubClient = func() itemFetcher {
		return stubItemFetcher{item: ghclient.Item{
			Repo:      "acme/widgets",
			Kind:      sharedtypes.ItemKindIssue,
			Number:    42,
			Title:     "Bug: triage queue stalls",
			Author:    "alice",
			State:     sharedtypes.ItemStateOpen,
			URL:       "https://github.com/acme/widgets/issues/42",
			UpdatedAt: time.Unix(1713511200, 0).UTC(),
		}}
	}

	var gotName sharedtypes.AgentName
	newAgent = func(name sharedtypes.AgentName, _ string) (triageAgent, error) {
		gotName = name
		return stubTriageAgent{result: &agent.Result{
			Output: mustJSONTUI(t, triage.Recommendation{
				Options: []triage.RecommendationOption{{
					StateChange:  sharedtypes.StateChangeNone,
					Rationale:    "The rerun found a clearer request for logs.",
					WaitingOn:    sharedtypes.WaitingOnContributor,
					DraftComment: "Please share the daemon log and the exact failing command.",
					Confidence:   sharedtypes.ConfidenceHigh,
				}},
			}),
			Usage: agent.TokenUsage{InputTokens: 900, OutputTokens: 120},
		}}, nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#42",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    42,
		Title:     "Bug: triage queue stalls",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	oldRec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#42",
		Agent:     sharedtypes.AgentClaude,
		Model:     "claude",
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Old rationale.",
			DraftComment:   "Old draft.",
			ProposedLabels: []string{"bug"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if _, err := rerunInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID:     oldRec.ID,
		RepoID:               "acme/widgets",
		Number:               42,
		Kind:                 sharedtypes.ItemKindIssue,
		Title:                "Bug: triage queue stalls",
		DraftComment:         "Old draft.",
		OriginalDraftComment: "Old draft.",
	}}, "Use the repo-specific agent override."); err != nil {
		t.Fatalf("rerunInboxEntries() error = %v", err)
	}

	if gotName != sharedtypes.AgentClaude {
		t.Fatalf("newAgent() name = %q, want %q from repo override", gotName, sharedtypes.AgentClaude)
	}
}

func mustJSONTUI(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func TestExecuteApprovalTreatsFixRequiredAsNoStateChange(t *testing.T) {
	executor := &stubApprovalExecutor{}
	entry := tui.Entry{
		RepoID:       "acme/widgets",
		Kind:         sharedtypes.ItemKindIssue,
		Number:       42,
		DraftComment: "Thanks for the report. I'll put up a fix.",
		StateChange:  sharedtypes.StateChangeFixRequired,
	}

	if err := executeApproval(context.Background(), executor, entry, []string{"bug", ezossTriagedLabel}, nil, "merge"); err != nil {
		t.Fatalf("executeApproval() error = %v", err)
	}

	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	if len(executor.comments) != 1 || executor.comments[0].Body != entry.DraftComment {
		t.Fatalf("comments = %#v, want draft comment", executor.comments)
	}
	if len(executor.closes) != 0 || len(executor.reviews) != 0 || len(executor.merges) != 0 {
		t.Fatalf("state actions = closes:%#v reviews:%#v merges:%#v, want none", executor.closes, executor.reviews, executor.merges)
	}
}

func TestApproveInboxEntriesExecutesCloseRecommendationAndMarksTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#21",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    21,
		Title:     "Stale support request",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#21",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  120,
		TokensOut: 18,
		Options: []db.NewRecommendationOption{{
			Rationale:      "The contributor has been inactive past the stale threshold.",
			DraftComment:   "Closing as stale for now. Feel free to reopen with more detail.",
			ProposedLabels: []string{"stale"},
			StateChange:    sharedtypes.StateChangeClose,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           21,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Closing as stale for now. Feel free to reopen with more detail.",
		ProposedLabels:   []string{"stale"},
		StateChange:      sharedtypes.StateChangeClose,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.closes) != 1 {
		t.Fatalf("closes = %d, want 1", len(executor.closes))
	}
	if executor.closes[0].Comment != "Closing as stale for now. Feel free to reopen with more detail." {
		t.Fatalf("close comment = %q, want stale close comment", executor.closes[0].Comment)
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantCloseLabels := []string{"stale", ezossTriagedLabel, "ezoss/awaiting-contributor"}
	if len(executor.labels[0].Add) != len(wantCloseLabels) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantCloseLabels)
	}
	for i, label := range wantCloseLabels {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantCloseLabels)
		}
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#21")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
	if item.State != sharedtypes.ItemStateClosed {
		t.Fatalf("item state after close approval = %q, want %q", item.State, sharedtypes.ItemStateClosed)
	}
}

func TestApproveInboxEntriesSyncsWaitingOnAndStaleLabelsFromConfig(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		SyncLabels: config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	staleSince := time.Unix(1712592000, 0)
	if err := database.UpsertItem(db.Item{
		ID:         "acme/widgets#22",
		RepoID:     "acme/widgets",
		Kind:       sharedtypes.ItemKindIssue,
		Number:     22,
		Title:      "Contributor went quiet",
		State:      sharedtypes.ItemStateOpen,
		WaitingOn:  sharedtypes.WaitingOnContributor,
		StaleSince: &staleSince,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#22",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  100,
		TokensOut: 20,
		Options: []db.NewRecommendationOption{{
			Rationale:      "We are waiting on the contributor and the thread is stale.",
			DraftComment:   "Following up on the missing info.",
			ProposedLabels: []string{"needs-info"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           22,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Following up on the missing info.",
		ProposedLabels:   []string{"needs-info"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	want := []string{"needs-info", ezossTriagedLabel, "ezoss/awaiting-contributor", "ezoss/stale"}
	if len(executor.labels[0].Add) != len(want) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, want)
	}
	for i, label := range want {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, want)
		}
	}
	wantRemove := []string{"ezoss/awaiting-maintainer"}
	if len(executor.labels[0].Remove) != len(wantRemove) {
		t.Fatalf("removed labels = %#v, want %#v", executor.labels[0].Remove, wantRemove)
	}
	for i, label := range wantRemove {
		if executor.labels[0].Remove[i] != label {
			t.Fatalf("removed labels = %#v, want %#v", executor.labels[0].Remove, wantRemove)
		}
	}
}

func TestApproveInboxEntriesRemovesOutdatedManagedStateLabels(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		SyncLabels: config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#24",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    24,
		Title:     "Status changed",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#24",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  80,
		TokensOut: 18,
		Options: []db.NewRecommendationOption{{
			Rationale:      "We already have what we need locally.",
			DraftComment:   "Thanks, we'll take it from here.",
			ProposedLabels: []string{"question"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           24,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Thanks, we'll take it from here.",
		ProposedLabels:   []string{"question"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantAdd := []string{"question", ezossTriagedLabel, "ezoss/awaiting-maintainer"}
	if len(executor.labels[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
		}
	}
	wantRemove := []string{"ezoss/awaiting-contributor", "ezoss/stale"}
	if len(executor.labels[0].Remove) != len(wantRemove) {
		t.Fatalf("removed labels = %#v, want %#v", executor.labels[0].Remove, wantRemove)
	}
	for i, label := range wantRemove {
		if executor.labels[0].Remove[i] != label {
			t.Fatalf("removed labels = %#v, want %#v", executor.labels[0].Remove, wantRemove)
		}
	}
}

func TestApproveInboxEntriesAppliesApprovedOptionWaitingOn(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		SyncLabels: config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "acme/widgets#26", RepoID: "acme/widgets", Kind: sharedtypes.ItemKindIssue, Number: 26,
		Title: "Needs a follow-up", State: sharedtypes.ItemStateOpen, WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "acme/widgets#26", Agent: sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{DraftComment: "Can you share details?", StateChange: sharedtypes.StateChangeNone, WaitingOn: sharedtypes.WaitingOnContributor}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		OptionID:         rec.Options[0].ID,
		RepoID:           "acme/widgets",
		Number:           26,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Can you share details?",
		StateChange:      sharedtypes.StateChangeNone,
		WaitingOn:        sharedtypes.WaitingOnContributor,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	wantAdd := []string{ezossTriagedLabel, "ezoss/awaiting-contributor"}
	if len(executor.labels) != 1 || len(executor.labels[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels, wantAdd)
	}
	for i, label := range wantAdd {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
		}
	}
	item, err := database.GetItem("acme/widgets#26")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || item.WaitingOn != sharedtypes.WaitingOnContributor {
		t.Fatalf("item after approval = %#v, want waiting_on contributor", item)
	}
}

func TestApproveInboxEntriesDeduplicatesManagedLabels(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	if err := config.SaveGlobal(filepath.Join(tempRoot, "config.yaml"), &config.GlobalConfig{
		SyncLabels: config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true},
	}); err != nil {
		t.Fatalf("SaveGlobal() error = %v", err)
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	staleSince := time.Unix(1712592000, 0)
	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:         "acme/widgets#25",
		RepoID:     "acme/widgets",
		Kind:       sharedtypes.ItemKindIssue,
		Number:     25,
		Title:      "Duplicate label inputs",
		State:      sharedtypes.ItemStateOpen,
		WaitingOn:  sharedtypes.WaitingOnContributor,
		StaleSince: &staleSince,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#25",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  80,
		TokensOut: 18,
		Options: []db.NewRecommendationOption{{
			Rationale:      "The recommendation already includes managed labels and should not duplicate them on approval.",
			DraftComment:   "Please share the missing reproduction steps.",
			ProposedLabels: []string{"needs-info", ezossTriagedLabel, "ezoss/awaiting-contributor", ezossStaleLabel, "needs-info"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           25,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Please share the missing reproduction steps.",
		ProposedLabels:   []string{"needs-info", ezossTriagedLabel, "ezoss/awaiting-contributor", ezossStaleLabel, "needs-info"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantAdd := []string{"needs-info", ezossTriagedLabel, "ezoss/awaiting-contributor", ezossStaleLabel}
	if len(executor.labels[0].Add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
	}
	for i, label := range wantAdd {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantAdd)
		}
	}
}

func TestApprovalLabelEditsRemoveOptionalManagedLabelsWhenSyncDisabled(t *testing.T) {
	staleSince := time.Unix(1712592000, 0)
	item := &db.Item{
		WaitingOn:  sharedtypes.WaitingOnContributor,
		StaleSince: &staleSince,
	}

	add, remove := approvalLabelEdits(tui.Entry{ProposedLabels: []string{"bug"}}, item, config.SyncLabels{Triaged: true})

	wantAdd := []string{"bug", ezossTriagedLabel}
	if len(add) != len(wantAdd) {
		t.Fatalf("added labels = %#v, want %#v", add, wantAdd)
	}
	for i, label := range wantAdd {
		if add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", add, wantAdd)
		}
	}
	wantRemove := []string{"ezoss/awaiting-contributor", "ezoss/awaiting-maintainer", ezossStaleLabel}
	if len(remove) != len(wantRemove) {
		t.Fatalf("removed labels = %#v, want %#v", remove, wantRemove)
	}
	for i, label := range wantRemove {
		if remove[i] != label {
			t.Fatalf("removed labels = %#v, want %#v", remove, wantRemove)
		}
	}
}

func TestApproveInboxEntriesExecutesRequestChangesRecommendationAndMarksTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#9",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindPR,
		Number:    9,
		Title:     "feat: simplify queue rendering",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#9",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  180,
		TokensOut: 26,
		Options: []db.NewRecommendationOption{{
			Rationale:      "The PR changes behavior and still needs tests.",
			DraftComment:   "Please add coverage for the empty-state path before this lands.",
			ProposedLabels: []string{"needs-work"},
			StateChange:    sharedtypes.StateChangeRequestChanges,
			Confidence:     sharedtypes.ConfidenceHigh,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           9,
		Kind:             sharedtypes.ItemKindPR,
		DraftComment:     "Please add coverage for the empty-state path before this lands.",
		ProposedLabels:   []string{"needs-work"},
		StateChange:      sharedtypes.StateChangeRequestChanges,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.reviews) != 1 {
		t.Fatalf("request changes reviews = %d, want 1", len(executor.reviews))
	}
	if executor.reviews[0].Body != "Please add coverage for the empty-state path before this lands." {
		t.Fatalf("review body = %q, want request-changes comment", executor.reviews[0].Body)
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantRequestChangesLabels := []string{"needs-work", ezossTriagedLabel, "ezoss/awaiting-maintainer"}
	if len(executor.labels[0].Add) != len(wantRequestChangesLabels) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantRequestChangesLabels)
	}
	for i, label := range wantRequestChangesLabels {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantRequestChangesLabels)
		}
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#9")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
}

func TestApproveInboxEntriesExecutesRequestApprovalForReviewRecommendationAndMarksTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#11",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindPR,
		Number:    11,
		Title:     "feat: add queue sorting",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#11",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  160,
		TokensOut: 24,
		Options: []db.NewRecommendationOption{{
			Rationale:      "The PR may be valid, but the approach was not previously agreed on.",
			DraftComment:   "This looks like a substantial direction change. Should we do this review now, or would you prefer to discuss the approach first?",
			ProposedLabels: []string{"needs-decision"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           11,
		Kind:             sharedtypes.ItemKindPR,
		DraftComment:     "This looks like a substantial direction change. Should we do this review now, or would you prefer to discuss the approach first?",
		ProposedLabels:   []string{"needs-decision"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(executor.comments))
	}
	if executor.comments[0].Kind != sharedtypes.ItemKindPR {
		t.Fatalf("comment kind = %q, want %q", executor.comments[0].Kind, sharedtypes.ItemKindPR)
	}
	if executor.comments[0].Body != "This looks like a substantial direction change. Should we do this review now, or would you prefer to discuss the approach first?" {
		t.Fatalf("comment body = %q, want request-approval comment", executor.comments[0].Body)
	}
	if len(executor.reviews) != 0 {
		t.Fatalf("request changes reviews = %d, want 0", len(executor.reviews))
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantLabels := []string{"needs-decision", ezossTriagedLabel, "ezoss/awaiting-maintainer"}
	if len(executor.labels[0].Add) != len(wantLabels) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantLabels)
	}
	for i, label := range wantLabels {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantLabels)
		}
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#11")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
}

func TestApproveInboxEntriesExecutesMergeRecommendationAndMarksTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	originalLoadGlobalConfig := loadGlobalConfig
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
		loadGlobalConfig = originalLoadGlobalConfig
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}
	loadGlobalConfig = func(path string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{MergeMethod: "squash", SyncLabels: config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true}}, nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#10",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindPR,
		Number:    10,
		Title:     "feat: ship the queue refactor",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#10",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  140,
		TokensOut: 12,
		Options: []db.NewRecommendationOption{{
			Rationale:      "The PR is approved and ready to land.",
			DraftComment:   "",
			ProposedLabels: []string{"ready-to-merge"},
			StateChange:    sharedtypes.StateChangeMerge,
			Confidence:     sharedtypes.ConfidenceHigh,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           10,
		Kind:             sharedtypes.ItemKindPR,
		ProposedLabels:   []string{"ready-to-merge"},
		StateChange:      sharedtypes.StateChangeMerge,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.merges) != 1 {
		t.Fatalf("merges = %d, want 1", len(executor.merges))
	}
	if executor.merges[0].RepoID != "acme/widgets" || executor.merges[0].Number != 10 {
		t.Fatalf("merge call = %#v, want acme/widgets#10", executor.merges[0])
	}
	if executor.merges[0].Method != "squash" {
		t.Fatalf("merge method = %q, want squash", executor.merges[0].Method)
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantMergeLabels := []string{"ready-to-merge", ezossTriagedLabel, "ezoss/awaiting-maintainer"}
	if len(executor.labels[0].Add) != len(wantMergeLabels) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantMergeLabels)
	}
	for i, label := range wantMergeLabels {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantMergeLabels)
		}
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#10")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
	if item.State != sharedtypes.ItemStateMerged {
		t.Fatalf("item state after merge approval = %q, want %q", item.State, sharedtypes.ItemStateMerged)
	}
}

// TestApproveInboxEntriesAbortsDestructiveActionWhenLabelEditFails covers the
// failure mode where a proposed user label doesn't exist in the target repo.
// Previously the destructive action (merge here, also close / request_changes)
// ran first and labels last, so a missing-label error left the PR merged with
// no triage labels applied. The fix applies labels first; if they fail the
// merge must not happen, the recommendation must stay active, and the failed
// attempt must be recorded so the user can retry after fixing the label.
func TestApproveInboxEntriesAbortsMergeWhenLabelEditFails(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	originalLoadGlobalConfig := loadGlobalConfig
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
		loadGlobalConfig = originalLoadGlobalConfig
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{labelErr: errors.New("'agent-integration' not found")}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}
	loadGlobalConfig = func(path string) (*config.GlobalConfig, error) {
		return &config.GlobalConfig{MergeMethod: "squash", SyncLabels: config.SyncLabels{Triaged: true, WaitingOn: true, Stale: true}}, nil
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#10",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindPR,
		Number:    10,
		Title:     "feat: ship the queue refactor",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#10",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  140,
		TokensOut: 12,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Ready to land.",
			ProposedLabels: []string{"agent-integration"},
			StateChange:    sharedtypes.StateChangeMerge,
			Confidence:     sharedtypes.ConfidenceHigh,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	err = approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           10,
		Kind:             sharedtypes.ItemKindPR,
		ProposedLabels:   []string{"agent-integration"},
		StateChange:      sharedtypes.StateChangeMerge,
	}})
	if err == nil {
		t.Fatal("expected approveInboxEntries() to fail when label edit fails")
	}
	if !strings.Contains(err.Error(), "agent-integration") {
		t.Fatalf("approveInboxEntries() error = %q, want it to surface the underlying label error", err)
	}

	if len(executor.merges) != 0 {
		t.Fatalf("merges = %d, want 0 - destructive action must not run when labels fail", len(executor.merges))
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 1 || active[0].ID != rec.ID {
		t.Fatalf("active recommendations after failed approval = %#v, want original to remain active", active)
	}

	approvals, err := database.ListApprovalsForRecommendation(rec.ID)
	if err != nil {
		t.Fatalf("ListApprovalsForRecommendation() error = %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("approvals recorded = %d, want 1 (failed attempt)", len(approvals))
	}
	if approvals[0].ActedError == "" {
		t.Fatalf("approval acted_error = %q, want non-empty", approvals[0].ActedError)
	}

	item, err := database.GetItem("acme/widgets#10")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item.State != sharedtypes.ItemStateOpen {
		t.Fatalf("item state after failed merge = %q, want %q (no merge should have happened)", item.State, sharedtypes.ItemStateOpen)
	}
}

// TestApproveInboxEntriesAbortsCloseWhenLabelEditFails is the close-state-change
// counterpart to the merge test - same invariant: labels first, no destructive
// action on label failure.
func TestApproveInboxEntriesAbortsCloseWhenLabelEditFails(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{labelErr: errors.New("'mystery-label' not found")}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#21",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    21,
		Title:     "Stale support request",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnContributor,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#21",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  120,
		TokensOut: 18,
		Options: []db.NewRecommendationOption{{
			Rationale:      "Stale.",
			DraftComment:   "Closing as stale.",
			ProposedLabels: []string{"mystery-label"},
			StateChange:    sharedtypes.StateChangeClose,
			Confidence:     sharedtypes.ConfidenceMedium,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	err = approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           21,
		Kind:             sharedtypes.ItemKindIssue,
		DraftComment:     "Closing as stale.",
		ProposedLabels:   []string{"mystery-label"},
		StateChange:      sharedtypes.StateChangeClose,
	}})
	if err == nil {
		t.Fatal("expected approveInboxEntries() to fail when label edit fails")
	}

	if len(executor.closes) != 0 {
		t.Fatalf("closes = %d, want 0 - close must not run when labels fail", len(executor.closes))
	}
	item, err := database.GetItem("acme/widgets#21")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item.State != sharedtypes.ItemStateOpen {
		t.Fatalf("item state after failed close = %q, want %q", item.State, sharedtypes.ItemStateOpen)
	}
}

func TestApproveInboxEntriesContributorSkipsLabelEdits(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	if err := database.UpsertRepo(db.Repo{ID: "upstream/widgets", Source: db.RepoSourceContrib}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "upstream/widgets#12", RepoID: "upstream/widgets", Kind: sharedtypes.ItemKindPR, Role: sharedtypes.RoleContributor,
		Number: 12, Title: "question", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID: "upstream/widgets#12",
		Agent:  sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{
			DraftComment: "Thanks, that helps.", StateChange: sharedtypes.StateChangeNone,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "upstream/widgets",
		Number:           12,
		Kind:             sharedtypes.ItemKindPR,
		Role:             sharedtypes.RoleContributor,
		DraftComment:     "Thanks, that helps.",
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}
	if len(executor.labels) != 0 {
		t.Fatalf("label edits = %#v, want none for contributor approval", executor.labels)
	}
	if len(executor.comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(executor.comments))
	}
}

func TestDismissInboxEntriesContributorSkipsLabelEdits(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewLabelEditor := newLabelEditor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newLabelEditor = originalNewLabelEditor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	editor := &stubLabelEditor{}
	newLabelEditor = func() labelEditor { return editor }

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	if err := database.UpsertRepo(db.Repo{ID: "upstream/widgets", Source: db.RepoSourceContrib}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID: "upstream/widgets#13", RepoID: "upstream/widgets", Kind: sharedtypes.ItemKindIssue, Role: sharedtypes.RoleContributor,
		Number: 13, Title: "question", State: sharedtypes.ItemStateOpen,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:  "upstream/widgets#13",
		Agent:   sharedtypes.AgentClaude,
		Options: []db.NewRecommendationOption{{StateChange: sharedtypes.StateChangeNone}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := dismissInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "upstream/widgets",
		Number:           13,
		Kind:             sharedtypes.ItemKindIssue,
		Role:             sharedtypes.RoleContributor,
	}}); err != nil {
		t.Fatalf("dismissInboxEntries() error = %v", err)
	}
	if len(editor.entries) != 0 {
		t.Fatalf("label edits = %#v, want none for contributor dismissal", editor.entries)
	}
}

func TestApproveInboxEntriesExecutesNoneRecommendationAndMarksTriaged(t *testing.T) {
	tempRoot := t.TempDir()
	originalNewPaths := newPaths
	originalNewApprovalExecutor := newApprovalExecutor
	t.Cleanup(func() {
		newPaths = originalNewPaths
		newApprovalExecutor = originalNewApprovalExecutor
	})
	newPaths = func() (*paths.Paths, error) {
		return paths.WithRoot(tempRoot), nil
	}
	executor := &stubApprovalExecutor{}
	newApprovalExecutor = func() approvalExecutor {
		return executor
	}

	database, err := db.Open(filepath.Join(tempRoot, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets", DefaultBranch: "main"}); err != nil {
		t.Fatalf("UpsertRepo() error = %v", err)
	}
	if err := database.UpsertItem(db.Item{
		ID:        "acme/widgets#11",
		RepoID:    "acme/widgets",
		Kind:      sharedtypes.ItemKindIssue,
		Number:    11,
		Title:     "Question already answered",
		State:     sharedtypes.ItemStateOpen,
		WaitingOn: sharedtypes.WaitingOnMaintainer,
	}); err != nil {
		t.Fatalf("UpsertItem() error = %v", err)
	}
	rec, err := database.InsertRecommendation(db.NewRecommendation{
		ItemID:    "acme/widgets#11",
		Agent:     sharedtypes.AgentClaude,
		TokensIn:  80,
		TokensOut: 10,
		Options: []db.NewRecommendationOption{{
			Rationale:      "No GitHub action is needed beyond marking this triaged.",
			DraftComment:   "",
			ProposedLabels: []string{"question"},
			StateChange:    sharedtypes.StateChangeNone,
			Confidence:     sharedtypes.ConfidenceHigh,
		}},
	})
	if err != nil {
		t.Fatalf("InsertRecommendation() error = %v", err)
	}

	if err := approveInboxEntries(context.Background(), []tui.Entry{{
		RecommendationID: rec.ID,
		RepoID:           "acme/widgets",
		Number:           11,
		Kind:             sharedtypes.ItemKindIssue,
		ProposedLabels:   []string{"question"},
		StateChange:      sharedtypes.StateChangeNone,
	}}); err != nil {
		t.Fatalf("approveInboxEntries() error = %v", err)
	}

	if len(executor.comments) != 0 {
		t.Fatalf("comments = %d, want 0", len(executor.comments))
	}
	if len(executor.closes) != 0 {
		t.Fatalf("closes = %d, want 0", len(executor.closes))
	}
	if len(executor.reviews) != 0 {
		t.Fatalf("request changes reviews = %d, want 0", len(executor.reviews))
	}
	if len(executor.merges) != 0 {
		t.Fatalf("merges = %d, want 0", len(executor.merges))
	}
	if len(executor.labels) != 1 {
		t.Fatalf("label edits = %d, want 1", len(executor.labels))
	}
	wantLabels := []string{"question", ezossTriagedLabel, "ezoss/awaiting-maintainer"}
	if len(executor.labels[0].Add) != len(wantLabels) {
		t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantLabels)
	}
	for i, label := range wantLabels {
		if executor.labels[0].Add[i] != label {
			t.Fatalf("added labels = %#v, want %#v", executor.labels[0].Add, wantLabels)
		}
	}

	active, err := database.ListActiveRecommendations()
	if err != nil {
		t.Fatalf("ListActiveRecommendations() error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active recommendations = %d, want 0", len(active))
	}
	item, err := database.GetItem("acme/widgets#11")
	if err != nil {
		t.Fatalf("GetItem() error = %v", err)
	}
	if item == nil || !item.GHTriaged {
		t.Fatalf("item after approve = %#v, want gh_triaged=true", item)
	}
}
