package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// managedServicePath returns a default PATH for daemons started by a service
// manager (launchd, systemd) that would otherwise inherit only the service
// manager's minimal PATH. Home-directory entries are interpolated here
// because neither plist nor systemd Environment= expands $HOME.
//
// Entry order: user-scoped dirs first so user-managed tools (go, cargo,
// ~/.local/bin) win over system packages, then Homebrew and distro defaults.
func managedServicePath(home string) string {
	dirs := []string{
		filepath.Join(home, ".ezoss", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func xmlEscaped(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

func systemdEscapeArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if strings.ContainsAny(arg, " \t\n\r\"'\\") {
		return fmt.Sprintf("%q", arg)
	}
	return arg
}

// quoteWindowsTaskArg wraps a path that contains spaces, tabs, or embedded
// quotes in double quotes for use in the schtasks /TR argument. Backslashes
// are kept literal (Windows path separators are not escape characters), and
// any embedded quote is escaped with a backslash per cmd.exe rules.
func quoteWindowsTaskArg(arg string) string {
	if !strings.ContainsAny(arg, " \t\"") {
		return arg
	}
	return `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
}

func runServiceCommand(name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", path, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
