package doctor

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
)

func TestRunReportsSuccessfulChecks(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(filepath.Join(root, "doctor.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return paths.WithRoot(root), nil
		},
		CheckGitHubAuth: func(context.Context) error {
			return nil
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return &config.GlobalConfig{Agent: config.AgentClaude}, nil
		},
		LookPath: func(file string) (string, error) {
			switch file {
			case "gh":
				return "/usr/bin/gh", nil
			case "claude":
				return "/usr/local/bin/claude", nil
			default:
				t.Fatalf("LookPath() file = %q, want gh or claude", file)
			}
			return "", nil
		},
		ResolveShellEnv: func() ([]string, error) {
			return []string{"SHELL=/bin/zsh", "PATH=/usr/bin"}, nil
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{State: daemon.StateRunning, PID: 4242}, nil
		},
		DialIPC: func(path string) (ipcClient, error) {
			if path != filepath.Join(root, "daemon.sock") {
				t.Fatalf("DialIPC() path = %q, want %q", path, filepath.Join(root, "daemon.sock"))
			}
			return stubIPCClient{
				call: func(method string, params interface{}, result interface{}) error {
					health, ok := result.(*ipc.HealthResult)
					if !ok {
						t.Fatalf("Call() result type = %T, want *ipc.HealthResult", result)
					}
					health.Status = "ok"
					return nil
				},
			}, nil
		},
		OpenDB: func(path string) (dbCloser, error) {
			if path != filepath.Join(root, "ezoss.db") {
				t.Fatalf("OpenDB() path = %q, want %q", path, filepath.Join(root, "ezoss.db"))
			}
			return database, nil
		},
	}

	results := runner.Run(context.Background())

	if len(results) != 7 {
		t.Fatalf("len(results) = %d, want 7", len(results))
	}

	for _, result := range results {
		if !result.OK {
			t.Fatalf("check %q unexpectedly failed: %+v", result.Name, result)
		}
	}

	if got := results[0].Name; got != "state directory" {
		t.Fatalf("results[0].Name = %q, want %q", got, "state directory")
	}

	if got := results[1].Name; got != "gh CLI" {
		t.Fatalf("results[1].Name = %q, want %q", got, "gh CLI")
	}

	if got := results[2].Name; got != "shell environment" {
		t.Fatalf("results[2].Name = %q, want %q", got, "shell environment")
	}

	if got := results[3].Name; got != "gh auth" {
		t.Fatalf("results[3].Name = %q, want %q", got, "gh auth")
	}

	if got := results[4].Name; got != "agent CLI" {
		t.Fatalf("results[4].Name = %q, want %q", got, "agent CLI")
	}

	if got := results[5].Name; got != "daemon" {
		t.Fatalf("results[5].Name = %q, want %q", got, "daemon")
	}

	if got := results[6].Name; got != "database" {
		t.Fatalf("results[6].Name = %q, want %q", got, "database")
	}
}

func TestRunCapturesFailuresPerCheck(t *testing.T) {
	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return nil, errors.New("no home directory")
		},
		CheckGitHubAuth: func(context.Context) error {
			return errors.New("gh auth status: not logged in")
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return nil, errors.New("parse global config: bad yaml")
		},
		LookPath: func(string) (string, error) {
			return "", errors.New("executable file not found")
		},
		ResolveShellEnv: func() ([]string, error) {
			return nil, errors.New("shell command timed out")
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{}, errors.New("read pid file: permission denied")
		},
		OpenDB: func(string) (dbCloser, error) {
			return nil, errors.New("open db: disk I/O error")
		},
	}

	results := runner.Run(context.Background())

	if len(results) != 7 {
		t.Fatalf("len(results) = %d, want 7", len(results))
	}

	for _, result := range results {
		if result.OK {
			t.Fatalf("check %q unexpectedly succeeded", result.Name)
		}
		if result.Detail == "" {
			t.Fatalf("check %q detail is empty", result.Name)
		}
	}
}

func TestRunResolvesBinaryWhenAgentIsAuto(t *testing.T) {
	root := t.TempDir()
	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return paths.WithRoot(root), nil
		},
		CheckGitHubAuth: func(context.Context) error {
			return nil
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return &config.GlobalConfig{Agent: config.AgentAuto, PollInterval: time.Minute}, nil
		},
		LookPath: func(file string) (string, error) {
			switch file {
			case "gh":
				return "/usr/bin/gh", nil
			case "claude":
				return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
			case "codex":
				return "/usr/local/bin/codex", nil
			default:
				t.Fatalf("LookPath() file = %q, want gh, claude, or codex", file)
			}
			return "", nil
		},
		ResolveShellEnv: func() ([]string, error) {
			return []string{"PATH=/usr/bin"}, nil
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{State: daemon.StateStopped}, nil
		},
		OpenDB: func(path string) (dbCloser, error) {
			database, err := db.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			return database, nil
		},
	}

	results := runner.Run(context.Background())

	if !results[4].OK {
		t.Fatalf("agent CLI check unexpectedly failed: %+v", results[4])
	}
	if got := results[4].Detail; got != "/usr/local/bin/codex (auto -> codex)" {
		t.Fatalf("results[4].Detail = %q, want %q", got, "/usr/local/bin/codex (auto -> codex)")
	}
}

func TestRunReportsGitHubAuthFailure(t *testing.T) {
	root := t.TempDir()
	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return paths.WithRoot(root), nil
		},
		CheckGitHubAuth: func(context.Context) error {
			return errors.New("gh auth status: no github hosts configured")
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return &config.GlobalConfig{}, nil
		},
		LookPath: func(file string) (string, error) {
			if file != "gh" {
				t.Fatalf("LookPath() file = %q, want %q", file, "gh")
			}
			return "/usr/bin/gh", nil
		},
		ResolveShellEnv: func() ([]string, error) {
			return []string{"PATH=/usr/bin"}, nil
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{State: daemon.StateStopped}, nil
		},
		OpenDB: func(path string) (dbCloser, error) {
			database, err := db.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			return database, nil
		},
	}

	results := runner.Run(context.Background())

	if results[3].OK {
		t.Fatalf("gh auth check unexpectedly succeeded: %+v", results[3])
	}
	if got := results[3].Detail; got != "gh auth status: no github hosts configured" {
		t.Fatalf("results[3].Detail = %q, want %q", got, "gh auth status: no github hosts configured")
	}
}

func TestRunSkipsGitHubAuthWhenGHCLIMissing(t *testing.T) {
	root := t.TempDir()
	authCalls := 0
	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return paths.WithRoot(root), nil
		},
		CheckGitHubAuth: func(context.Context) error {
			authCalls++
			return errors.New("gh auth status: should not run without gh")
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return &config.GlobalConfig{}, nil
		},
		LookPath: func(file string) (string, error) {
			if file != "gh" {
				t.Fatalf("LookPath() file = %q, want %q", file, "gh")
			}
			return "", errors.New("executable file not found")
		},
		ResolveShellEnv: func() ([]string, error) {
			return []string{"PATH=/usr/bin"}, nil
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{State: daemon.StateStopped}, nil
		},
		OpenDB: func(path string) (dbCloser, error) {
			database, err := db.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			return database, nil
		},
	}

	results := runner.Run(context.Background())

	if authCalls != 0 {
		t.Fatalf("CheckGitHubAuth() calls = %d, want 0 when gh CLI is missing", authCalls)
	}
	if results[3].OK {
		t.Fatalf("gh auth check unexpectedly succeeded: %+v", results[3])
	}
	if got := results[3].Detail; got != "skipped: gh CLI unavailable" {
		t.Fatalf("results[3].Detail = %q, want %q", got, "skipped: gh CLI unavailable")
	}
}

func TestRunChecksDaemonIPCHealthWhenRunning(t *testing.T) {
	root := t.TempDir()
	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return paths.WithRoot(root), nil
		},
		CheckGitHubAuth: func(context.Context) error {
			return nil
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return &config.GlobalConfig{}, nil
		},
		LookPath: func(file string) (string, error) {
			if file != "gh" {
				t.Fatalf("LookPath() file = %q, want %q", file, "gh")
			}
			return "/usr/bin/gh", nil
		},
		ResolveShellEnv: func() ([]string, error) {
			return []string{"PATH=/usr/bin"}, nil
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{State: daemon.StateRunning, PID: 4242}, nil
		},
		DialIPC: func(path string) (ipcClient, error) {
			if path != filepath.Join(root, "daemon.sock") {
				t.Fatalf("DialIPC() path = %q, want %q", path, filepath.Join(root, "daemon.sock"))
			}
			return stubIPCClient{
				call: func(method string, params interface{}, result interface{}) error {
					if method != ipc.MethodHealth {
						t.Fatalf("Call() method = %q, want %q", method, ipc.MethodHealth)
					}
					health, ok := result.(*ipc.HealthResult)
					if !ok {
						t.Fatalf("Call() result type = %T, want *ipc.HealthResult", result)
					}
					health.Status = "ok"
					return nil
				},
			}, nil
		},
		OpenDB: func(path string) (dbCloser, error) {
			database, err := db.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			return database, nil
		},
	}

	results := runner.Run(context.Background())

	if !results[5].OK {
		t.Fatalf("daemon check unexpectedly failed: %+v", results[5])
	}
	if !strings.Contains(results[5].Detail, "ipc ok") {
		t.Fatalf("results[5].Detail = %q, want to contain %q", results[5].Detail, "ipc ok")
	}
}

func TestRunSkipsDaemonIPCHealthWhenStopped(t *testing.T) {
	root := t.TempDir()
	dialed := false
	runner := Runner{
		Paths: func() (*paths.Paths, error) {
			return paths.WithRoot(root), nil
		},
		CheckGitHubAuth: func(context.Context) error {
			return nil
		},
		LoadConfig: func(string) (*config.GlobalConfig, error) {
			return &config.GlobalConfig{}, nil
		},
		LookPath: func(file string) (string, error) {
			if file != "gh" {
				t.Fatalf("LookPath() file = %q, want %q", file, "gh")
			}
			return "/usr/bin/gh", nil
		},
		ResolveShellEnv: func() ([]string, error) {
			return []string{"PATH=/usr/bin"}, nil
		},
		ReadDaemonStatus: func(string) (daemon.Status, error) {
			return daemon.Status{State: daemon.StateStopped}, nil
		},
		DialIPC: func(string) (ipcClient, error) {
			dialed = true
			return nil, errors.New("should not dial")
		},
		OpenDB: func(path string) (dbCloser, error) {
			database, err := db.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				_ = database.Close()
			})
			return database, nil
		},
	}

	results := runner.Run(context.Background())

	if dialed {
		t.Fatal("DialIPC() called for stopped daemon")
	}
	if results[5].OK {
		t.Fatalf("daemon check unexpectedly reported ok: %+v", results[5])
	}
	if !results[5].Warning {
		t.Fatalf("daemon check warning = false, want true: %+v", results[5])
	}
	if got := results[5].Detail; got != "stopped; run `ezoss daemon start` to resume polling" {
		t.Fatalf("results[5].Detail = %q, want %q", got, "stopped; run `ezoss daemon start` to resume polling")
	}
}

type stubIPCClient struct {
	call  func(method string, params interface{}, result interface{}) error
	close func() error
}

func (s stubIPCClient) Call(method string, params interface{}, result interface{}) error {
	if s.call != nil {
		return s.call(method, params, result)
	}
	return nil
}

func (s stubIPCClient) Close() error {
	if s.close != nil {
		return s.close()
	}
	return nil
}
