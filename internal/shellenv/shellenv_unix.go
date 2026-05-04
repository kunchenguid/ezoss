//go:build !windows

package shellenv

import (
	"os/exec"
	"syscall"
)

// detachFromTerminal makes the spawned shell start in its own session so it
// can't tcsetpgrp() on the parent's controlling terminal and steal foreground.
// Without this, an interactive zsh -i probe leaves the caller as a background
// process group, so the next termios ioctl (e.g. Bubble Tea raw mode) raises
// SIGTTOU and the outer shell suspends ezoss with "tty output".
func detachFromTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
