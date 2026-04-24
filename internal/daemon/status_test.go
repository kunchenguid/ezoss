package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStatusReturnsStoppedWhenPIDFileMissing(t *testing.T) {
	tempDir := t.TempDir()

	status, err := ReadStatus(filepath.Join(tempDir, "daemon.pid"), func(int) (bool, error) {
		t.Fatal("process check should not be called when pid file is missing")
		return false, nil
	})
	if err != nil {
		t.Fatalf("ReadStatus() error = %v", err)
	}
	if status.State != StateStopped {
		t.Fatalf("State = %q, want %q", status.State, StateStopped)
	}
	if status.PID != 0 {
		t.Fatalf("PID = %d, want 0", status.PID)
	}
	if status.Stale {
		t.Fatalf("Stale = true, want false")
	}
}

func TestReadStatusReturnsRunningForLivePID(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	status, err := ReadStatus(pidPath, func(pid int) (bool, error) {
		if pid != 1234 {
			t.Fatalf("pid = %d, want 1234", pid)
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("ReadStatus() error = %v", err)
	}
	if status.State != StateRunning {
		t.Fatalf("State = %q, want %q", status.State, StateRunning)
	}
	if status.PID != 1234 {
		t.Fatalf("PID = %d, want 1234", status.PID)
	}
	if status.Stale {
		t.Fatalf("Stale = true, want false")
	}
}

func TestReadStatusReturnsStoppedAndStaleForDeadPID(t *testing.T) {
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	status, err := ReadStatus(pidPath, func(pid int) (bool, error) {
		if pid != 1234 {
			t.Fatalf("pid = %d, want 1234", pid)
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("ReadStatus() error = %v", err)
	}
	if status.State != StateStopped {
		t.Fatalf("State = %q, want %q", status.State, StateStopped)
	}
	if status.PID != 1234 {
		t.Fatalf("PID = %d, want 1234", status.PID)
	}
	if !status.Stale {
		t.Fatalf("Stale = false, want true")
	}
}
