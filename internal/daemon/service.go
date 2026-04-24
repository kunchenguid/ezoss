package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/ezoss/internal/paths"
)

// Service identifier bases. The live identifiers returned by
// launchdServiceLabel/systemdServiceName/windowsTaskName include a short
// stable suffix derived from p.Root() so two ezoss installs with different
// AM_HOMEs cannot collide in the global launchctl/systemctl/schtasks
// namespace. See serviceInstanceSuffix for the full rationale.
const (
	launchdServiceLabelBase = "com.kunchenguid.ezoss.daemon"
	systemdServiceNameBase  = "ezoss-daemon"
	windowsTaskNameBase     = "ezoss-daemon"
)

var runtimeGOOS = runtime.GOOS
var serviceUserHomeDir = os.UserHomeDir
var serviceCurrentUser = user.Current
var serviceExecutablePath = os.Executable
var serviceCommandRunner = runServiceCommand
var serviceManagerBypassed = defaultServiceManagerBypassed

// defaultServiceManagerBypassed reports whether managed-service plumbing
// (launchctl/systemctl/schtasks) should be skipped.
//
// It returns true when EZOSS_TEST_START_DAEMON=1 is set (a production escape
// hatch used by demo recordings and similar) or when the process is running
// under `go test`. The test-binary guard is critical because the managed
// service label, plist path, systemd unit path, and schtasks task name are
// all globally scoped under the current user - they do not honor the
// *paths.Paths argument. Without this guard, any daemon test that calls
// Start/Stop with an unstubbed paths.Paths would reach into the developer's
// real ~/Library/LaunchAgents (or systemd user unit dir, or scheduled tasks)
// and tear down a live daemon.
func defaultServiceManagerBypassed() bool {
	if os.Getenv("EZOSS_TEST_START_DAEMON") == "1" {
		return true
	}
	return testing.Testing()
}

// serviceInstanceSuffix returns a short stable suffix derived from p.Root()
// so managed-service artifacts (launchd label + plist filename, systemd unit
// name + path, Windows task name) are scoped per-install instead of sharing
// a single globally unique identifier per user.
//
// Without scoping, a single launchd label is a shared slot. Any ezoss
// process on the machine could `launchctl bootout gui/<uid>/<label>` and
// tear down another install's daemon. Multiple AM_HOMEs (dev vs prod)
// would also collide. By scoping every identifier by sha256(p.Root()),
// each install owns a distinct service instance.
func serviceInstanceSuffix(p *paths.Paths) string {
	root := ""
	if p != nil {
		root = p.Root()
	}
	if root != "" {
		if !filepath.IsAbs(root) {
			if absRoot, err := filepath.Abs(root); err == nil {
				root = absRoot
			}
		}
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	}
	root = filepath.Clean(root)
	if runtimeGOOS == "windows" {
		root = strings.ToLower(root)
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:4])
}

func launchdServiceLabel(p *paths.Paths) string {
	return launchdServiceLabelBase + "." + serviceInstanceSuffix(p)
}

func systemdServiceName(p *paths.Paths) string {
	return systemdServiceNameBase + "-" + serviceInstanceSuffix(p) + ".service"
}

func windowsTaskName(p *paths.Paths) string {
	return windowsTaskNameBase + "-" + serviceInstanceSuffix(p)
}

// ServiceManagerSupported reports whether the current OS has a managed
// service backend (launchd / systemd --user / schtasks).
func ServiceManagerSupported() bool {
	switch runtimeGOOS {
	case "darwin", "linux", "windows":
		return true
	default:
		return false
	}
}

// InstallService registers the ezoss daemon with the OS service manager so it
// starts at login and is restarted by the OS if it crashes. The returned
// `managed` bool is false on unsupported platforms or when the bypass is
// active (tests); callers should fall back to a foreground PID-based launch
// when managed is false.
func InstallService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() {
		return false, nil
	}
	exe, err := serviceExecutablePath()
	if err != nil {
		return false, fmt.Errorf("resolve executable: %w", err)
	}
	return installManagedServiceWithExecutable(p, exe)
}

func installManagedServiceWithExecutable(p *paths.Paths, exe string) (bool, error) {
	switch runtimeGOOS {
	case "darwin":
		return true, installLaunchAgent(p, exe)
	case "linux":
		return true, installSystemdUserService(p, exe)
	case "windows":
		return true, installWindowsTask(p, exe)
	default:
		return false, nil
	}
}

// UninstallService removes the OS service registration if any. It is a no-op
// when no service is registered for this AM_HOME.
func UninstallService(p *paths.Paths) error {
	if serviceManagerBypassed() {
		return nil
	}
	if !managedServiceInstalled(p) {
		return nil
	}
	switch runtimeGOOS {
	case "darwin":
		if err := stopLaunchAgent(p); err != nil {
			return err
		}
		return removeLaunchAgent(p)
	case "linux":
		_, _ = serviceCommandRunner("systemctl", "--user", "stop", systemdServiceName(p))
		_, _ = serviceCommandRunner("systemctl", "--user", "disable", systemdServiceName(p))
		if err := os.Remove(systemdUserServicePath(p)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove systemd unit: %w", err)
		}
		_, _ = serviceCommandRunner("systemctl", "--user", "daemon-reload")
		return nil
	case "windows":
		_, _ = serviceCommandRunner("schtasks", "/End", "/TN", windowsTaskName(p))
		if _, err := serviceCommandRunner("schtasks", "/Delete", "/TN", windowsTaskName(p), "/F"); err != nil {
			return fmt.Errorf("schtasks delete: %w", err)
		}
		return nil
	default:
		return nil
	}
}

// StartService instructs the OS service manager to start (or restart) the
// daemon. Returns (false, nil) when no service is registered or when the
// bypass is active; callers should fall back to a PID-based launch.
func StartService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() || !managedServiceInstalled(p) {
		return false, nil
	}
	switch runtimeGOOS {
	case "darwin":
		return true, startLaunchAgent(p)
	case "linux":
		return true, startSystemdUserService(p)
	case "windows":
		return true, startWindowsTask(p)
	default:
		return false, nil
	}
}

// RestartService is a managed-service restart. On launchd this is a kickstart;
// on systemd a `systemctl --user restart`; on Windows we /End then /Run.
// Returns (false, nil) when no service is registered.
func RestartService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() || !managedServiceInstalled(p) {
		return false, nil
	}
	switch runtimeGOOS {
	case "darwin":
		return true, startLaunchAgent(p)
	case "linux":
		return true, restartSystemdUserService(p)
	case "windows":
		_, _ = serviceCommandRunner("schtasks", "/End", "/TN", windowsTaskName(p))
		return true, startWindowsTask(p)
	default:
		return false, nil
	}
}

// StopService instructs the OS service manager to stop the daemon. Returns
// (false, nil) when no service is registered or when the bypass is active.
func StopService(p *paths.Paths) (bool, error) {
	if serviceManagerBypassed() || !managedServiceInstalled(p) {
		return false, nil
	}
	switch runtimeGOOS {
	case "darwin":
		return true, stopLaunchAgent(p)
	case "linux":
		return true, stopSystemdUserService(p)
	case "windows":
		return true, stopWindowsTask(p)
	default:
		return false, nil
	}
}

// ServiceInstalled reports whether a managed-service registration exists for
// this AM_HOME. Always false under the test bypass.
func ServiceInstalled(p *paths.Paths) bool {
	return managedServiceInstalled(p)
}

func managedServiceInstalled(p *paths.Paths) bool {
	if serviceManagerBypassed() {
		return false
	}
	switch runtimeGOOS {
	case "darwin":
		_, err := os.Stat(launchAgentPath(p))
		return err == nil
	case "linux":
		_, err := os.Stat(systemdUserServicePath(p))
		return err == nil
	case "windows":
		if p == nil {
			return false
		}
		_, err := serviceCommandRunner("schtasks", "/Query", "/TN", windowsTaskName(p))
		return err == nil
	default:
		return false
	}
}
