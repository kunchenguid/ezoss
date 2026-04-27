//go:build windows

package cli

import "syscall"

// detachedDaemonProcAttr returns SysProcAttr that puts the spawned
// daemon into its own process group on Windows. Mirrors what the
// Unix Setsid path does: the daemon stops sharing a console group
// with the user's shell, so a Ctrl+C in that shell won't fire
// CTRL_C_EVENT into the daemon.
func detachedDaemonProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
