package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunchenguid/ezoss/internal/paths"
)

// Retry parameters for `launchctl bootstrap` after a preceding bootout.
// launchctl bootout is async: it SIGTERMs the existing service and gives
// launchd up to a few seconds to finalize cleanup. During that window,
// bootstrap returns errno 37 EPROGRESS ("Operation already in progress").
// A stop+start sequence collides with this window unless bootstrap is
// retried. Exposed as package vars so tests can shrink the timings.
var (
	launchctlBootstrapRetryTimeout  = 10 * time.Second
	launchctlBootstrapRetryInterval = 200 * time.Millisecond
)

func installLaunchAgent(p *paths.Paths, exe string) error {
	path := launchAgentPath(p)
	home, err := serviceUserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create launch agents directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderLaunchAgent(exe, p, home)), 0o644); err != nil {
		return fmt.Errorf("write launch agent: %w", err)
	}
	return nil
}

func startLaunchAgent(p *paths.Paths) error {
	domain, err := launchdDomainTarget()
	if err != nil {
		return err
	}
	serviceTarget := domain + "/" + launchdServiceLabel(p)
	path := launchAgentPath(p)
	_, _ = serviceCommandRunner("launchctl", "bootout", serviceTarget)
	bootstrapErr := launchctlBootstrapWithRetry(domain, path)
	_, kickstartErr := serviceCommandRunner("launchctl", "kickstart", "-k", serviceTarget)
	if kickstartErr != nil {
		if bootstrapErr != nil {
			return fmt.Errorf("launchctl bootstrap: %v; kickstart: %w", bootstrapErr, kickstartErr)
		}
		return fmt.Errorf("launchctl kickstart: %w", kickstartErr)
	}
	return nil
}

// launchctlBootstrapWithRetry runs `launchctl bootstrap` and retries on
// errno 37 EPROGRESS until the bootout-cleanup window closes. Non-busy
// failures (bad plist, bad permissions) return immediately.
func launchctlBootstrapWithRetry(domain, path string) error {
	deadline := time.Now().Add(launchctlBootstrapRetryTimeout)
	var lastErr error
	for {
		_, err := serviceCommandRunner("launchctl", "bootstrap", domain, path)
		if err == nil {
			return nil
		}
		if !launchctlBootstrapBusy(err) {
			return err
		}
		lastErr = err
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(launchctlBootstrapRetryInterval)
	}
}

func launchctlBootstrapBusy(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "operation already in progress") ||
		strings.Contains(text, "exit status 37")
}

func stopLaunchAgent(p *paths.Paths) error {
	domain, err := launchdDomainTarget()
	if err != nil {
		return err
	}
	output, err := serviceCommandRunner("launchctl", "bootout", domain+"/"+launchdServiceLabel(p))
	if err != nil {
		if launchctlBootoutServiceNotLoaded(err, output) {
			return nil
		}
		return fmt.Errorf("launchctl bootout: %w", err)
	}
	return nil
}

func removeLaunchAgent(p *paths.Paths) error {
	err := os.Remove(launchAgentPath(p))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// launchctlBootoutServiceNotLoaded reports whether a launchctl bootout
// failure is the ESRCH case ("No such process", exit 3) that launchctl
// emits when the service label isn't currently loaded. That is semantically
// a successful stop - the service is already not running.
func launchctlBootoutServiceNotLoaded(err error, output []byte) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(string(output) + " " + err.Error())
	return strings.Contains(combined, "no such process")
}

func launchAgentPath(p *paths.Paths) string {
	home, err := serviceUserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdServiceLabel(p)+".plist")
}

func launchdDomainTarget() (string, error) {
	u, err := serviceCurrentUser()
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}
	if u == nil || u.Uid == "" {
		return "", fmt.Errorf("resolve current user: empty uid")
	}
	return "gui/" + u.Uid, nil
}

func renderLaunchAgent(exe string, p *paths.Paths, home string) string {
	values := []string{exe, "daemon", "run"}
	var args strings.Builder
	for _, value := range values {
		args.WriteString("    <string>")
		args.WriteString(xmlEscaped(value))
		args.WriteString("</string>\n")
	}
	logPath := filepath.Join(p.LogsDir(), "daemon.log")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>WorkingDirectory</key>
  <string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>%s</string>
    <key>PATH</key>
    <string>%s</string>
    <key>AM_HOME</key>
    <string>%s</string>
  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
`, xmlEscaped(launchdServiceLabel(p)), args.String(), xmlEscaped(p.Root()), xmlEscaped(home), xmlEscaped(managedServicePath(home)), xmlEscaped(p.Root()), xmlEscaped(logPath), xmlEscaped(logPath))
}
