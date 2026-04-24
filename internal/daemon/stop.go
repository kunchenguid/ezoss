package daemon

import (
	"fmt"
	"os"
	"time"
)

type ProcessSignaler func(pid int) error

// StopOptions controls how Stop tears down the running daemon. The
// zero value uses sensible production defaults: SIGTERM, a 10s grace
// period before SIGKILL, 100ms liveness polling.
type StopOptions struct {
	// Check overrides the OS-level liveness probe. Tests inject a
	// stub; production passes nil and gets processRunning.
	Check ProcessChecker
	// Term sends the graceful-shutdown signal (SIGTERM by default).
	Term ProcessSignaler
	// Kill sends the unconditional-shutdown signal (SIGKILL by
	// default). Used after GracePeriod elapses without exit.
	Kill ProcessSignaler
	// GracePeriod is the maximum time we wait after Term before
	// escalating to Kill. Zero means default (10s).
	GracePeriod time.Duration
	// PollInterval controls how often we re-check process liveness.
	// Zero means default (100ms).
	PollInterval time.Duration
	// Sleep / Now are seams for tests; production passes nil and gets
	// time.Sleep / time.Now.
	Sleep func(time.Duration)
	Now   func() time.Time
}

// Stop is the legacy wrapper around StopWithOptions for callers that
// only want defaults plus optional Check/Term overrides. New code
// should prefer StopWithOptions.
func Stop(pidFile string, check ProcessChecker, signal ProcessSignaler) error {
	return StopWithOptions(pidFile, StopOptions{
		Check: check,
		Term:  signal,
	})
}

// StopWithOptions sends SIGTERM to the daemon recorded in pidFile and
// blocks until the process actually exits (escalating to SIGKILL after
// GracePeriod). Only after the daemon is confirmed dead does it remove
// the pid file. Callers that immediately follow Stop with Start can
// rely on the OS having released the previous daemon's resources.
func StopWithOptions(pidFile string, opts StopOptions) error {
	term := opts.Term
	if term == nil {
		term = terminateProcess
	}
	kill := opts.Kill
	if kill == nil {
		kill = killProcess
	}
	grace := opts.GracePeriod
	if grace == 0 {
		grace = 10 * time.Second
	}
	pollInterval := opts.PollInterval
	if pollInterval == 0 {
		pollInterval = 100 * time.Millisecond
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	status, err := ReadStatus(pidFile, opts.Check)
	if err != nil {
		return fmt.Errorf("read daemon status: %w", err)
	}
	if status.State == StateStopped && !status.Stale {
		return nil
	}
	if status.State == StateRunning {
		if err := term(status.PID); err != nil {
			return fmt.Errorf("signal pid %d: %w", status.PID, err)
		}
		if err := waitForExit(status.PID, opts.Check, kill, grace, pollInterval, sleep, now); err != nil {
			return err
		}
	}
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	return nil
}

func waitForExit(pid int, check ProcessChecker, kill ProcessSignaler, grace, pollInterval time.Duration, sleep func(time.Duration), now func() time.Time) error {
	if check == nil {
		check = processRunning
	}
	deadline := now().Add(grace)
	killed := false
	for {
		alive, err := check(pid)
		if err != nil {
			return fmt.Errorf("check pid %d: %w", pid, err)
		}
		if !alive {
			return nil
		}
		if !killed && !now().Before(deadline) {
			if err := kill(pid); err != nil {
				return fmt.Errorf("kill pid %d after %s grace: %w", pid, grace, err)
			}
			killed = true
		}
		sleep(pollInterval)
	}
}
