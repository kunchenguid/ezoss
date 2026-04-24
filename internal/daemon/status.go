package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

const (
	StateStopped = "stopped"
	StateRunning = "running"
)

type Status struct {
	State string
	PID   int
	Stale bool
}

type ProcessChecker func(pid int) (bool, error)

func ReadStatus(pidFile string, check ProcessChecker) (Status, error) {
	if check == nil {
		check = processRunning
	}

	contents, err := os.ReadFile(pidFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Status{State: StateStopped}, nil
		}
		return Status{}, fmt.Errorf("read pid file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || pid <= 0 {
		return Status{}, fmt.Errorf("parse pid file %s: %w", pidFile, err)
	}

	running, err := check(pid)
	if err != nil {
		return Status{}, fmt.Errorf("check pid %d: %w", pid, err)
	}
	if running {
		return Status{State: StateRunning, PID: pid}, nil
	}

	return Status{State: StateStopped, PID: pid, Stale: true}, nil
}
