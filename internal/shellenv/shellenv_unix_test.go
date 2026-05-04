//go:build !windows

package shellenv

import (
	"os/exec"
	"syscall"
	"testing"
)

// detachFromTerminal must put the spawned shell in a fresh session so it
// can't tcsetpgrp() on the parent's controlling terminal. If it could, an
// interactive zsh -i probe would steal foreground; the parent would then
// raise SIGTTOU on the next termios ioctl (e.g. Bubble Tea raw mode) and
// the outer shell would suspend ezoss with "tty output".
//
// We start a real child, then read its pgid from outside. With Setsid the
// child is a process-group leader so getpgid(child) == child.Pid, and the
// pgid differs from the test process's pgid.
func TestDetachFromTerminalStartsChildInOwnSession(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	detachFromTerminal(cmd)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("detachFromTerminal did not set SysProcAttr.Setsid")
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	childPgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid(child): %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if childPgid != cmd.Process.Pid {
		t.Fatalf("child pgid=%d pid=%d; want pgid==pid (child should lead its own group)", childPgid, cmd.Process.Pid)
	}
	if childPgid == syscall.Getpgrp() {
		t.Fatalf("child pgid %d matches test pgid; child is not detached", childPgid)
	}
}
