//go:build windows

package shellenv

import "os/exec"

func detachFromTerminal(*exec.Cmd) {}
