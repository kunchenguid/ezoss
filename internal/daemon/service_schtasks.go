package daemon

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/ezoss/internal/paths"
)

func installWindowsTask(p *paths.Paths, exe string) error {
	args := []string{
		"/Create",
		"/TN", windowsTaskName(p),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
		"/TR", buildWindowsTaskCommand(exe),
	}
	if _, err := serviceCommandRunner("schtasks", args...); err != nil {
		return fmt.Errorf("schtasks create: %w", err)
	}
	return nil
}

func startWindowsTask(p *paths.Paths) error {
	if _, err := serviceCommandRunner("schtasks", "/Run", "/TN", windowsTaskName(p)); err != nil {
		return fmt.Errorf("schtasks run: %w", err)
	}
	return nil
}

func stopWindowsTask(p *paths.Paths) error {
	if _, err := serviceCommandRunner("schtasks", "/End", "/TN", windowsTaskName(p)); err != nil {
		return fmt.Errorf("schtasks end: %w", err)
	}
	return nil
}

// buildWindowsTaskCommand returns the schtasks /TR argument: the command
// schtasks runs at logon. AM_HOME is propagated from the user's environment
// at the time the task fires (schtasks inherits the calling user's env on
// /SC ONLOGON), so it does not need to be embedded in the command string.
func buildWindowsTaskCommand(exe string) string {
	return strings.Join([]string{quoteWindowsTaskArg(exe), "daemon", "run"}, " ")
}
