package daemon

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/ezoss/internal/paths"
)

func TestServiceInstanceSuffixIsStableAndPathScoped(t *testing.T) {
	a := paths.WithRoot("/tmp/foo")
	b := paths.WithRoot("/tmp/foo")
	c := paths.WithRoot("/tmp/bar")

	if serviceInstanceSuffix(a) != serviceInstanceSuffix(b) {
		t.Fatalf("expected stable suffix for the same root")
	}
	if serviceInstanceSuffix(a) == serviceInstanceSuffix(c) {
		t.Fatalf("expected different suffix for different roots")
	}
	if got := serviceInstanceSuffix(a); len(got) != 8 {
		t.Fatalf("suffix length = %d, want 8 hex chars", len(got))
	}
}

func TestLaunchdServiceLabelIsScoped(t *testing.T) {
	p := paths.WithRoot("/tmp/foo")
	label := launchdServiceLabel(p)
	if !strings.HasPrefix(label, launchdServiceLabelBase+".") {
		t.Fatalf("label = %q, want prefix %q", label, launchdServiceLabelBase+".")
	}
	if label == launchdServiceLabelBase {
		t.Fatalf("label = %q must include scoped suffix", label)
	}
}

func TestSystemdServiceNameEndsInDotService(t *testing.T) {
	p := paths.WithRoot("/tmp/foo")
	name := systemdServiceName(p)
	if !strings.HasSuffix(name, ".service") {
		t.Fatalf("name = %q must end in .service", name)
	}
	if !strings.HasPrefix(name, systemdServiceNameBase+"-") {
		t.Fatalf("name = %q must include scoped suffix", name)
	}
}

func TestRenderLaunchAgentEmbedsKeyFields(t *testing.T) {
	originalUserHomeDir := serviceUserHomeDir
	originalCurrentUser := serviceCurrentUser
	t.Cleanup(func() {
		serviceUserHomeDir = originalUserHomeDir
		serviceCurrentUser = originalCurrentUser
	})
	serviceUserHomeDir = func() (string, error) { return "/Users/test", nil }
	serviceCurrentUser = func() (*user.User, error) { return &user.User{Uid: "501"}, nil }

	p := paths.WithRoot("/Users/test/.ezoss")
	rendered := renderLaunchAgent("/usr/local/bin/ezoss", p, "/Users/test")

	for _, want := range []string{
		"<key>Label</key>",
		launchdServiceLabel(p),
		"<key>ProgramArguments</key>",
		"<string>/usr/local/bin/ezoss</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<key>WorkingDirectory</key>",
		"<string>/Users/test/.ezoss</string>",
		"<key>EnvironmentVariables</key>",
		"<key>HOME</key>",
		"<key>PATH</key>",
		"<key>AM_HOME</key>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
		filepath.Join(p.LogsDir(), "daemon.log"),
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderLaunchAgent output missing %q\n--- output ---\n%s", want, rendered)
		}
	}
}

func TestRenderSystemdUnitEmbedsKeyFields(t *testing.T) {
	originalUserHomeDir := serviceUserHomeDir
	t.Cleanup(func() {
		serviceUserHomeDir = originalUserHomeDir
	})
	serviceUserHomeDir = func() (string, error) { return "/home/test", nil }

	p := paths.WithRoot("/home/test/.ezoss")
	rendered := renderSystemdUnit("/usr/local/bin/ezoss", p, "/home/test")

	for _, want := range []string{
		"[Unit]",
		"Description=ezoss background daemon",
		"[Service]",
		"Type=simple",
		"ExecStart=/usr/local/bin/ezoss daemon run",
		"WorkingDirectory=/home/test/.ezoss",
		`Environment="HOME=/home/test"`,
		`Environment="PATH=`,
		`Environment="AM_HOME=/home/test/.ezoss"`,
		"Restart=always",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderSystemdUnit output missing %q\n--- output ---\n%s", want, rendered)
		}
	}
}

func TestBuildWindowsTaskCommandQuotesPathsWithSpaces(t *testing.T) {
	plain := buildWindowsTaskCommand(`C:\Users\test\.ezoss\bin\ezoss.exe`)
	if !strings.HasPrefix(plain, `C:\Users\test\.ezoss\bin\ezoss.exe `) {
		t.Fatalf("plain command should not be quoted: %q", plain)
	}

	withSpace := buildWindowsTaskCommand(`C:\Program Files\ezoss\ezoss.exe`)
	if !strings.HasPrefix(withSpace, `"C:\Program Files\ezoss\ezoss.exe"`) {
		t.Fatalf("path with spaces should be quoted: %q", withSpace)
	}
	if !strings.HasSuffix(withSpace, ` daemon run`) {
		t.Fatalf("command should end with ` daemon run`: %q", withSpace)
	}
}

func TestServiceManagerBypassedUnderTesting(t *testing.T) {
	if !defaultServiceManagerBypassed() {
		t.Fatal("expected service manager to be bypassed under go test")
	}
	if ServiceInstalled(paths.WithRoot(t.TempDir())) {
		t.Fatal("ServiceInstalled must report false under the bypass")
	}
}
