package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/paths"
)

var windowsExecutablePathForPID = defaultWindowsExecutablePathForPID

// daemonResetError signals that the daemon could not be brought back up after
// the binary was replaced. The caller may decide whether to surface this as a
// hard failure (the update succeeded but the daemon is offline).
type daemonResetError struct {
	err           error
	daemonOffline bool
}

func (e *daemonResetError) Error() string { return e.err.Error() }
func (e *daemonResetError) Unwrap() error { return e.err }
func (e *daemonResetError) Offline() bool { return e.daemonOffline }

// resetDaemonHook is overridable for tests.
var resetDaemonHook = defaultResetDaemon

// defaultResetDaemon stops and restarts the daemon so it picks up the freshly
// replaced binary. If a managed service (launchd/systemd/schtasks) is
// installed, we drive that; otherwise we fall back to the PID-file based
// stop+start.
func defaultResetDaemon(p *paths.Paths) error {
	if p == nil {
		return nil
	}
	managed := daemon.ServiceInstalled(p)
	if !managed && !daemonArtifactsExist(p) {
		return nil
	}

	if managed {
		if _, err := daemon.RestartService(p); err != nil {
			return &daemonResetError{err: fmt.Errorf("restart service: %w", err), daemonOffline: !daemonArtifactsExist(p)}
		}
		return nil
	}

	if err := daemon.Stop(p.PIDPath(), nil, nil); err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}
	if err := daemon.Start(p.PIDPath(), nil, func() error {
		return relaunchDetached(p)
	}); err != nil {
		return &daemonResetError{err: fmt.Errorf("start daemon: %w", err), daemonOffline: !daemonArtifactsExist(p)}
	}
	return nil
}

func relaunchDetached(p *paths.Paths) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	logPath := p.LogsDir() + string(os.PathSeparator) + "daemon.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	cmd := commandFor(exe, "daemon", "run")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "AM_HOME="+p.Root())
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch daemon: %w", err)
	}
	return nil
}

func daemonArtifactsExist(p *paths.Paths) bool {
	for _, path := range []string{p.IPCPath(), p.PIDPath()} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// ensureDaemonUsesCurrentExecutable refuses to update when the running
// daemon's executable path differs from the binary that the updater would
// replace. Without this guard, `~/go/bin/ezoss update` would replace the
// dev binary while the LaunchAgent at `~/.ezoss/bin/ezoss` keeps running
// the old code: the user thinks they updated, but no behavior changes.
//
// Returns nil when the daemon is not running or paths cannot be resolved
// (we don't want to block updates on transient inspection failures).
func (u *updater) ensureDaemonUsesCurrentExecutable() error {
	if u == nil || u.skipDaemonPathCheck || u.paths == nil || u.executablePath == "" {
		return nil
	}
	status, err := daemon.ReadStatus(u.paths.PIDPath(), nil)
	if err != nil || status.State != daemon.StateRunning || status.PID <= 0 {
		return nil
	}
	runningPath, err := executablePathForPID(status.PID)
	if err != nil || runningPath == "" {
		// Inspection failed - don't block the update on a transient error.
		return nil
	}
	current := resolveExecutablePath(u.executablePath)
	running := resolveExecutablePath(runningPath)
	if executablePathsMatch(current, running) {
		return nil
	}
	return fmt.Errorf(
		"daemon is running from %s, but update is running from %s; run update using the same binary that started the daemon, or restart the daemon from this binary first",
		running, current,
	)
}

func executablePathForPID(pid int) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	case "windows":
		return windowsExecutablePathForPID(pid)
	default:
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
}

func resolveExecutablePath(p string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func executablePathsMatch(a, b string) bool {
	if runtime.GOOS != "windows" {
		return a == b
	}
	a = filepath.Clean(strings.ReplaceAll(a, `\`, "/"))
	b = filepath.Clean(strings.ReplaceAll(b, `\`, "/"))
	return strings.EqualFold(a, b)
}
