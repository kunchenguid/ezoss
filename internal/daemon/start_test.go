package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ghclient"
	"github.com/kunchenguid/ezoss/internal/ipc"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

// ipcSocketParent picks a directory short enough to hold a Unix domain
// socket path within the 104-byte sun_path limit on macOS. On Windows
// the IPC transport is TCP-with-token via a flat file, so any short
// path works and we just use the per-test temp dir.
func ipcSocketParent(tempDir string) string {
	if runtime.GOOS == "windows" {
		return tempDir
	}
	return "/tmp"
}

func TestStartLaunchesWhenStopped(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	launched := 0

	err := Start(pidPath, func(int) (bool, error) {
		t.Fatal("process check should not be called when pid file is missing")
		return false, nil
	}, func() error {
		launched++
		return nil
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if launched != 1 {
		t.Fatalf("launch count = %d, want 1", launched)
	}
}

func TestStartReturnsAlreadyRunningWhenPIDIsLive(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := Start(pidPath, func(pid int) (bool, error) {
		if pid != 1234 {
			t.Fatalf("pid = %d, want 1234", pid)
		}
		return true, nil
	}, func() error {
		t.Fatal("launch should not be called when daemon is already running")
		return nil
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Start() error = %v, want %v", err, ErrAlreadyRunning)
	}
}

func TestStartRemovesStalePIDFileBeforeLaunch(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	launched := 0

	err := Start(pidPath, func(int) (bool, error) {
		return false, nil
	}, func() error {
		launched++
		if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pid file stat error = %v, want not exists", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if launched != 1 {
		t.Fatalf("launch count = %d, want 1", launched)
	}
}

func TestRunWritesAndRemovesPIDFile(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- Run(pidPath, sigCh)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		contents, err := os.ReadFile(pidPath)
		if err == nil {
			pid, err := strconv.Atoi(string(contents))
			if err != nil {
				t.Fatalf("pid contents parse error = %v", err)
			}
			if pid != os.Getpid() {
				t.Fatalf("pid contents = %d, want %d", pid, os.Getpid())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid file was not created: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not exit after signal")
	}

	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file stat error = %v, want not exists", err)
	}
}

func TestRunWithOptionsRefusesWhenPIDFileHoldsLiveProcess(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")

	// Write a "live" pid file - tests inject a checker that always
	// reports the pid as alive without depending on real OS state.
	if err := os.WriteFile(pidPath, []byte("9999\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	err := RunWithOptions(pidPath, sigCh, RunOptions{
		ProcessChecker: func(pid int) (bool, error) {
			if pid != 9999 {
				t.Fatalf("ProcessChecker called with pid %d, want 9999", pid)
			}
			return true, nil
		},
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("RunWithOptions() error = %v, want %v", err, ErrAlreadyRunning)
	}

	// PID file must be untouched - the surviving daemon's pid is
	// still inside it.
	contents, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := strings.TrimSpace(string(contents)); got != "9999" {
		t.Fatalf("pid file contents = %q, want 9999", got)
	}
}

func TestRunWithOptionsClearsStalePIDFileAndProceeds(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")

	if err := os.WriteFile(pidPath, []byte("9999\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			ProcessChecker: func(int) (bool, error) {
				// Pretend the previously-recorded pid is dead -
				// the daemon should claim the file and proceed.
				return false, nil
			},
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		contents, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(contents)))
			if pid == os.Getpid() {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid file was not claimed by current process within deadline; last contents=%q", contents)
		}
		time.Sleep(10 * time.Millisecond)
	}

	sigCh <- syscall.SIGTERM
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestDrainOverrunTickRemovesBufferedTickReturnsTrue(t *testing.T) {
	t.Parallel()

	ch := make(chan time.Time, 2)
	ch <- time.Now()
	ch <- time.Now()
	ticker := stubTicker{ch: ch}

	if !drainOverrunTick(ticker) {
		t.Fatal("drainOverrunTick() = false, want true when buffer non-empty")
	}
	// Buffer had two; one drain pulls one.
	if len(ch) != 1 {
		t.Fatalf("len(buffer) = %d, want 1 after one drain", len(ch))
	}

	// Drain again - one tick still queued.
	if !drainOverrunTick(ticker) {
		t.Fatal("drainOverrunTick() = false on second call, want true")
	}
	if len(ch) != 0 {
		t.Fatalf("len(buffer) = %d, want 0 after both drains", len(ch))
	}

	// No buffered tick - should return false without blocking.
	if drainOverrunTick(ticker) {
		t.Fatal("drainOverrunTick() = true on empty buffer, want false")
	}
}

func TestRunWithOptionsPollsImmediatelyAndOnEachTickUntilSignal(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 2)
	pollCalls := make(chan []string, 3)
	errCh := make(chan error, 1)

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets", "acme/gadgets"},
			PollInterval: time.Hour,
			PollOnce: func(_ context.Context, repos []string) error {
				pollCalls <- append([]string(nil), repos...)
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
		})
	}()

	for i := 0; i < 2; i++ {
		select {
		case got := <-pollCalls:
			want := []string{"acme/widgets", "acme/gadgets"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("poll repos = %#v, want %#v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("expected poll call")
		}

		if i == 0 {
			tickCh <- time.Now()
		}
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}

	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file stat error = %v, want not exists", err)
	}
}

func TestRunWithOptionsBacksOffAndKeepsRunningOnGenericPollError(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 1)
	sleepCalls := make(chan time.Duration, 1)
	pollCalls := make(chan int, 2)
	errCh := make(chan error, 1)
	callCount := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			PollOnce: func(context.Context, []string) error {
				callCount++
				pollCalls <- callCount
				if callCount == 1 {
					return errors.New("poll failed")
				}
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
			Sleep: func(delay time.Duration) {
				sleepCalls <- delay
			},
		})
	}()

	select {
	case got := <-pollCalls:
		if got != 1 {
			t.Fatalf("first poll call = %d, want 1", got)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned after generic poll error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected initial poll call")
	}

	select {
	case delay := <-sleepCalls:
		if delay != time.Hour {
			t.Fatalf("sleep delay = %v, want poll interval", delay)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned instead of backing off: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected generic-error backoff sleep")
	}

	tickCh <- time.Now()

	select {
	case got := <-pollCalls:
		if got != 2 {
			t.Fatalf("second poll call = %d, want 2", got)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned before second poll: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected second poll call after backoff")
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestRunWithOptionsBacksOffAndKeepsRunningOnRateLimitError(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 1)
	sleepCalls := make(chan time.Duration, 1)
	pollCalls := make(chan int, 2)
	errCh := make(chan error, 1)
	callCount := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			PollOnce: func(context.Context, []string) error {
				callCount++
				pollCalls <- callCount
				if callCount == 1 {
					return &ghclient.RateLimitError{Message: "secondary rate limit: retry after 90s", RetryAfter: 90 * time.Second}
				}
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
			Sleep: func(delay time.Duration) {
				sleepCalls <- delay
			},
		})
	}()

	select {
	case got := <-pollCalls:
		if got != 1 {
			t.Fatalf("first poll call = %d, want 1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected initial poll call")
	}

	select {
	case delay := <-sleepCalls:
		if delay != 90*time.Second {
			t.Fatalf("sleep delay = %v, want 90s", delay)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected rate-limit backoff sleep")
	}

	tickCh <- time.Now()

	select {
	case got := <-pollCalls:
		if got != 2 {
			t.Fatalf("second poll call = %d, want 2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected second poll call after backoff")
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestRunWithOptionsBacksOffAndKeepsRunningOnTransientGitHubServerError(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 1)
	sleepCalls := make(chan time.Duration, 1)
	pollCalls := make(chan int, 2)
	errCh := make(chan error, 1)
	callCount := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			PollOnce: func(context.Context, []string) error {
				callCount++
				pollCalls <- callCount
				if callCount == 1 {
					return errors.New("poll repo acme/widgets: list needing triage: gh issue list acme/widgets: HTTP 504: 504 Gateway Timeout (https://api.github.com/graphql)")
				}
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
			Sleep: func(delay time.Duration) {
				sleepCalls <- delay
			},
		})
	}()

	select {
	case got := <-pollCalls:
		if got != 1 {
			t.Fatalf("first poll call = %d, want 1", got)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned after transient error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected initial poll call")
	}

	select {
	case delay := <-sleepCalls:
		if delay != time.Hour {
			t.Fatalf("sleep delay = %v, want poll interval", delay)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned instead of backing off: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected transient-error backoff sleep")
	}

	tickCh <- time.Now()

	select {
	case got := <-pollCalls:
		if got != 2 {
			t.Fatalf("second poll call = %d, want 2", got)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned before second poll: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected second poll call after backoff")
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestRunWithOptionsBacksOffAndKeepsRunningOnJoinedPollErrors(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 1)
	sleepCalls := make(chan time.Duration, 1)
	pollCalls := make(chan int, 2)
	errCh := make(chan error, 1)
	callCount := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets", "acme/broken"},
			PollInterval: time.Hour,
			PollOnce: func(context.Context, []string) error {
				callCount++
				pollCalls <- callCount
				if callCount == 1 {
					return errors.Join(
						&ghclient.RateLimitError{Message: "secondary rate limit: retry after 90s", RetryAfter: 90 * time.Second},
						errors.New("poll failed"),
					)
				}
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
			Sleep: func(delay time.Duration) {
				sleepCalls <- delay
			},
		})
	}()

	<-pollCalls
	select {
	case delay := <-sleepCalls:
		if delay != 90*time.Second {
			t.Fatalf("sleep delay = %v, want 90s", delay)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned instead of backing off: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected joined-error backoff sleep")
	}

	tickCh <- time.Now()
	select {
	case got := <-pollCalls:
		if got != 2 {
			t.Fatalf("second poll call = %d, want 2", got)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned before second poll: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected second poll call after joined-error backoff")
	}

	sigCh <- syscall.SIGTERM
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestRunWithOptionsBacksOffAndKeepsRunningOnRecommendationSnapshotError(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 1)
	sleepCalls := make(chan time.Duration, 1)
	pollCalls := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	snapshotCalls := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			RecommendationSnapshot: func() ([]db.Recommendation, error) {
				snapshotCalls++
				if snapshotCalls == 1 {
					return nil, errors.New("snapshot failed")
				}
				return nil, nil
			},
			PollOnce: func(context.Context, []string) error {
				pollCalls <- struct{}{}
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
			Sleep: func(delay time.Duration) {
				sleepCalls <- delay
			},
		})
	}()

	select {
	case delay := <-sleepCalls:
		if delay != time.Hour {
			t.Fatalf("sleep delay = %v, want poll interval", delay)
		}
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned after snapshot error: %v", err)
	case <-pollCalls:
		t.Fatal("poll should not run until the snapshot succeeds")
	case <-time.After(2 * time.Second):
		t.Fatal("expected snapshot-error backoff sleep")
	}

	tickCh <- time.Now()
	select {
	case <-pollCalls:
	case err := <-errCh:
		t.Fatalf("RunWithOptions() returned before recovered snapshot poll: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected poll after snapshot error backoff")
	}

	sigCh <- syscall.SIGTERM
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestJoinedPollRetryDelayClassifiesNetworkFailuresAsTransient(t *testing.T) {
	_, reason, ok := joinedPollRetryDelay(errors.New("read tcp 127.0.0.1:1234->140.82.112.6:443: read: connection reset by peer"))
	if !ok {
		t.Fatal("expected network failure to be retryable")
	}
	if reason != "transient poll error" {
		t.Fatalf("reason = %q, want transient poll error", reason)
	}
}

func TestRunWithOptionsServesIPCHealthAndRemovesSocketOnExit(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	ipcPath := filepath.Join(ipcSocketParent(tempDir), fmt.Sprintf("am-ipc-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(ipcPath)
	sigCh := make(chan os.Signal, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			IPCPath: ipcPath,
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		client, err := ipc.Dial(ipcPath)
		if err == nil {
			var result ipc.HealthResult
			callErr := client.Call(ipc.MethodHealth, ipc.HealthParams{}, &result)
			_ = client.Close()
			if callErr != nil {
				t.Fatalf("Call(health) error = %v", callErr)
			}
			if result.Status != "ok" {
				t.Fatalf("health status = %q, want %q", result.Status, "ok")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ipc server was not reachable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}

	if _, err := os.Stat(ipcPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ipc path stat error = %v, want not exists", err)
	}
}

func TestRunWithOptionsStreamsRecommendationCreatedEvents(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	ipcPath := filepath.Join(ipcSocketParent(tempDir), fmt.Sprintf("am-ipc-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(ipcPath)

	database, err := db.Open(filepath.Join(tempDir, "ezoss.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	if err := database.UpsertRepo(db.Repo{ID: "acme/widgets"}); err != nil {
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

	sigCh := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	pollCalls := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			IPCPath:      ipcPath,
			PollOnce: func(context.Context, []string) error {
				pollCalls++
				if pollCalls > 1 {
					return nil
				}
				_, err := database.InsertRecommendation(db.NewRecommendation{
					ItemID: "acme/widgets#42",
					Agent:  sharedtypes.AgentClaude,
					Options: []db.NewRecommendationOption{{
						StateChange:    sharedtypes.StateChangeNone,
						Rationale:      "Needs a repro before deeper debugging.",
						DraftComment:   "Can you share a minimal repro?",
						ProposedLabels: []string{"bug"},
						Confidence:     sharedtypes.ConfidenceMedium,
					}},
				})
				return err
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: make(chan time.Time)}
			},
			RecommendationSnapshot: database.ListActiveRecommendations,
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		events, cancel, err := ipc.Subscribe(ipcPath, &ipc.SubscribeParams{})
		if err == nil {
			defer cancel()
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatal("Subscribe() channel closed before first event")
				}
				if event.Type != ipc.EventRecommendationCreated {
					t.Fatalf("Event.Type = %q, want %q", event.Type, ipc.EventRecommendationCreated)
				}
				if event.ItemID != "acme/widgets#42" {
					t.Fatalf("Event.ItemID = %q, want %q", event.ItemID, "acme/widgets#42")
				}
				if event.RecommendationID == "" {
					t.Fatal("expected recommendation id in created event")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for recommendation event")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ipc subscribe was not reachable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions() did not exit after signal")
	}
}

func TestDiffRecommendationEventsIncludesRemovedRecommendations(t *testing.T) {
	before := []db.Recommendation{{
		ID:     "rec-1",
		ItemID: "acme/widgets#42",
	}}

	after := []db.Recommendation{}

	got := diffRecommendationEvents(before, after)
	want := []ipc.Event{{
		Type:             ipc.EventRecommendationRemoved,
		RecommendationID: "rec-1",
		ItemID:           "acme/widgets#42",
	}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diffRecommendationEvents() = %#v, want %#v", got, want)
	}
}

type stubTicker struct {
	ch <-chan time.Time
}

func (s stubTicker) Chan() <-chan time.Time {
	return s.ch
}

func (stubTicker) Stop() {}

// safeBuffer wraps bytes.Buffer behind a mutex so the logger goroutine
// (slog handlers can be called from any goroutine the daemon owns) and
// the test goroutine can both touch it without racing.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunWithOptionsLogsLifecycleEvents(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time)
	var logBuf safeBuffer
	errCh := make(chan error, 1)

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			Logger:       NewLogger(&logBuf),
			StartupAttrs: []any{"version", "v9.9.9"},
			PollOnce: func(context.Context, []string) error {
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
		})
	}()

	// Wait for the initial cycle to log "cycle done" so the goroutine
	// is parked on the ticker; otherwise the SIGTERM races the cycle.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logBuf.String(), `msg="cycle done"`) {
		if time.Now().After(deadline) {
			t.Fatalf("did not see cycle done log within deadline; got:\n%s", logBuf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	sigCh <- syscall.SIGTERM
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunWithOptions() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions did not exit after signal")
	}

	out := logBuf.String()
	expected := []string{
		`msg="daemon started"`,
		`version=v9.9.9`,
		`msg="cycle started"`,
		`cycle=1`,
		`msg="cycle done"`,
		`msg="received signal"`,
		`action=stop`,
		`msg="daemon stopped"`,
	}
	for _, want := range expected {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\nfull log:\n%s", want, out)
		}
	}
}

func TestRunWithOptionsLogsRateLimitBackoff(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	tickCh := make(chan time.Time, 1)
	sleepCalls := make(chan time.Duration, 1)
	pollCalls := make(chan int, 2)
	errCh := make(chan error, 1)
	var logBuf safeBuffer
	callCount := 0

	go func() {
		errCh <- RunWithOptions(pidPath, sigCh, RunOptions{
			Repos:        []string{"acme/widgets"},
			PollInterval: time.Hour,
			Logger:       NewLogger(&logBuf),
			PollOnce: func(context.Context, []string) error {
				callCount++
				pollCalls <- callCount
				if callCount == 1 {
					return &ghclient.RateLimitError{Message: "secondary rate limit", RetryAfter: 90 * time.Second}
				}
				return nil
			},
			NewTicker: func(time.Duration) Ticker {
				return stubTicker{ch: tickCh}
			},
			Sleep: func(delay time.Duration) {
				sleepCalls <- delay
			},
		})
	}()

	<-pollCalls
	<-sleepCalls
	tickCh <- time.Now()
	<-pollCalls

	sigCh <- syscall.SIGTERM
	<-errCh

	out := logBuf.String()
	if !strings.Contains(out, `msg="rate limited"`) {
		t.Fatalf("expected rate limited log line, got:\n%s", out)
	}
	if !strings.Contains(out, `delay=1m30s`) {
		t.Fatalf("expected delay=1m30s in rate-limit log, got:\n%s", out)
	}
}

func TestRunWithOptionsLogsRecoveredPanicBeforeReturning(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	sigCh := make(chan os.Signal, 1)
	var logBuf safeBuffer

	err := RunWithOptions(pidPath, sigCh, RunOptions{
		Repos:        []string{"acme/widgets"},
		PollInterval: time.Hour,
		Logger:       NewLogger(&logBuf),
		PollOnce: func(context.Context, []string) error {
			panic("boom")
		},
		NewTicker: func(time.Duration) Ticker {
			return stubTicker{ch: make(chan time.Time)}
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	out := logBuf.String()
	if !strings.Contains(out, `msg="daemon panic"`) {
		t.Fatalf("expected recovered panic log line, got:\n%s", out)
	}
	if !strings.Contains(out, "goroutine") {
		t.Fatalf("expected stack trace in panic log: %s", out)
	}
	if !strings.Contains(out, `msg="daemon exiting on error"`) {
		t.Fatalf("expected fatal-exit log line so the failure reason lands in the log file, got:\n%s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("expected underlying error in fatal-exit log: %s", out)
	}
}
