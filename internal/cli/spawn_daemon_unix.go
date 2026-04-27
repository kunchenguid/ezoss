//go:build !windows

package cli

import "syscall"

// detachedDaemonProcAttr returns SysProcAttr that puts the spawned
// daemon into its own session with no controlling terminal. Without
// this, git/gh subprocesses run by the daemon can open /dev/tty
// directly and leak credential prompts into whatever terminal the
// user happened to start the daemon from. It also means signals
// delivered to the user's foreground process group (Ctrl+C, Ctrl+\,
// hangup on close) no longer reach the daemon.
func detachedDaemonProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
