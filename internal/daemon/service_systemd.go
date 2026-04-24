package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kunchenguid/ezoss/internal/paths"
)

func installSystemdUserService(p *paths.Paths, exe string) error {
	path := systemdUserServicePath(p)
	home, err := serviceUserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderSystemdUnit(exe, p, home)), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if _, err := serviceCommandRunner("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := serviceCommandRunner("systemctl", "--user", "enable", systemdServiceName(p)); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}
	return nil
}

func startSystemdUserService(p *paths.Paths) error {
	if _, err := serviceCommandRunner("systemctl", "--user", "start", systemdServiceName(p)); err != nil {
		return fmt.Errorf("systemctl start: %w", err)
	}
	return nil
}

func restartSystemdUserService(p *paths.Paths) error {
	if _, err := serviceCommandRunner("systemctl", "--user", "restart", systemdServiceName(p)); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}
	return nil
}

func stopSystemdUserService(p *paths.Paths) error {
	if _, err := serviceCommandRunner("systemctl", "--user", "stop", systemdServiceName(p)); err != nil {
		return fmt.Errorf("systemctl stop: %w", err)
	}
	return nil
}

func systemdUserServicePath(p *paths.Paths) string {
	home, err := serviceUserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, ".config", "systemd", "user", systemdServiceName(p))
}

func renderSystemdUnit(exe string, p *paths.Paths, home string) string {
	command := strings.Join([]string{
		systemdEscapeArg(exe),
		systemdEscapeArg("daemon"),
		systemdEscapeArg("run"),
	}, " ")
	return fmt.Sprintf(`[Unit]
Description=ezoss background daemon

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Environment=%s
Environment=%s
Environment=%s
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, command, systemdEscapeArg(p.Root()), strconv.Quote("HOME="+home), strconv.Quote("PATH="+managedServicePath(home)), strconv.Quote("AM_HOME="+p.Root()))
}
