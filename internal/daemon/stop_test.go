package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStopReturnsNilWhenPIDFileMissing(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")

	err := Stop(pidPath, func(int) (bool, error) {
		t.Fatal("process check should not be called when pid file is missing")
		return false, nil
	}, func(int) error {
		t.Fatal("kill should not be called when pid file is missing")
		return nil
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestStopRemovesStalePIDFileWithoutKilling(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := Stop(pidPath, func(pid int) (bool, error) {
		if pid != 1234 {
			t.Fatalf("pid = %d, want 1234", pid)
		}
		return false, nil
	}, func(int) error {
		t.Fatal("kill should not be called for stale pid")
		return nil
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file still exists, stat err = %v", err)
	}
}

func TestStopSignalsRunningPIDAndRemovesPIDFile(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	checkCalls := 0
	killedPID := 0
	err := StopWithOptions(pidPath, StopOptions{
		Check: func(pid int) (bool, error) {
			if pid != 1234 {
				t.Fatalf("pid = %d, want 1234", pid)
			}
			checkCalls++
			// Simulate the daemon dying quickly after SIGTERM: alive
			// during initial ReadStatus, dead on the post-signal poll.
			return checkCalls == 1, nil
		},
		Term: func(pid int) error {
			killedPID = pid
			return nil
		},
		Sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if killedPID != 1234 {
		t.Fatalf("term pid = %d, want 1234", killedPID)
	}
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file still exists, stat err = %v", err)
	}
}

func TestStopWaitsForProcessToExitBeforeRemovingPIDFile(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	checkCalls := 0
	termCalls := 0
	killCalls := 0
	err := StopWithOptions(pidPath, StopOptions{
		Check: func(int) (bool, error) {
			checkCalls++
			// 1st call: ReadStatus says running.
			// 2nd-4th: still alive after SIGTERM.
			// 5th: finally dead.
			return checkCalls < 5, nil
		},
		Term: func(int) error {
			termCalls++
			return nil
		},
		Kill: func(int) error {
			killCalls++
			return nil
		},
		GracePeriod:  10 * time.Second,
		PollInterval: 100 * time.Millisecond,
		Sleep:        func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if termCalls != 1 {
		t.Fatalf("term calls = %d, want 1", termCalls)
	}
	if killCalls != 0 {
		t.Fatalf("kill calls = %d, want 0 (graceful exit before timeout)", killCalls)
	}
	if checkCalls < 5 {
		t.Fatalf("check calls = %d, want at least 5 (loop must keep polling until dead)", checkCalls)
	}
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file still exists, stat err = %v", err)
	}
}

func TestStopEscalatesToKillAfterGracePeriod(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	checkCalls := 0
	termCalls := 0
	killCalls := 0
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	err := StopWithOptions(pidPath, StopOptions{
		Check: func(int) (bool, error) {
			checkCalls++
			// Always alive until a SIGKILL has been sent, then dead.
			if killCalls > 0 {
				return false, nil
			}
			return true, nil
		},
		Term: func(int) error {
			termCalls++
			return nil
		},
		Kill: func(int) error {
			killCalls++
			return nil
		},
		GracePeriod:  1 * time.Second,
		PollInterval: 100 * time.Millisecond,
		Sleep:        func(d time.Duration) { now = now.Add(d) },
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if termCalls != 1 {
		t.Fatalf("term calls = %d, want 1", termCalls)
	}
	if killCalls != 1 {
		t.Fatalf("kill calls = %d, want 1 (escalation after grace)", killCalls)
	}
	if checkCalls < 2 {
		t.Fatalf("check calls = %d, want at least 2", checkCalls)
	}
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file still exists, stat err = %v", err)
	}
}
