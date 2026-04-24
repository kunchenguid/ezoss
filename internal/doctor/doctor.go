package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kunchenguid/ezoss/internal/agent"
	"github.com/kunchenguid/ezoss/internal/config"
	"github.com/kunchenguid/ezoss/internal/daemon"
	"github.com/kunchenguid/ezoss/internal/db"
	"github.com/kunchenguid/ezoss/internal/ipc"
	"github.com/kunchenguid/ezoss/internal/paths"
	"github.com/kunchenguid/ezoss/internal/shellenv"
	sharedtypes "github.com/kunchenguid/ezoss/internal/types"
)

type Result struct {
	Name   string
	OK     bool
	Warning bool
	Detail string
}

type Runner struct {
	Paths            func() (*paths.Paths, error)
	LoadConfig       func(string) (*config.GlobalConfig, error)
	LookPath         func(string) (string, error)
	ResolveShellEnv  func() ([]string, error)
	CheckGitHubAuth  func(context.Context) error
	ReadDaemonStatus func(string) (daemon.Status, error)
	DialIPC          func(string) (ipcClient, error)
	OpenDB           func(string) (dbCloser, error)
}

type dbCloser interface {
	Close() error
}

type ipcClient interface {
	Call(method string, params interface{}, result interface{}) error
	Close() error
}

func DefaultRunner() Runner {
	return Runner{
		Paths:           paths.New,
		LoadConfig:      config.LoadGlobal,
		LookPath:        exec.LookPath,
		ResolveShellEnv: shellenv.Resolve,
		CheckGitHubAuth: checkGitHubAuth,
		ReadDaemonStatus: func(pidFile string) (daemon.Status, error) {
			return daemon.ReadStatus(pidFile, nil)
		},
		DialIPC: func(socketPath string) (ipcClient, error) {
			return ipc.Dial(socketPath)
		},
		OpenDB: func(path string) (dbCloser, error) {
			return db.Open(path)
		},
	}
}

func (r Runner) Run(ctx context.Context) []Result {
	pathsFn := r.Paths
	if pathsFn == nil {
		pathsFn = paths.New
	}
	lookPathFn := r.LookPath
	if lookPathFn == nil {
		lookPathFn = exec.LookPath
	}
	loadConfigFn := r.LoadConfig
	if loadConfigFn == nil {
		loadConfigFn = config.LoadGlobal
	}
	resolveShellEnvFn := r.ResolveShellEnv
	if resolveShellEnvFn == nil {
		resolveShellEnvFn = shellenv.Resolve
	}
	checkGitHubAuthFn := r.CheckGitHubAuth
	if checkGitHubAuthFn == nil {
		checkGitHubAuthFn = checkGitHubAuth
	}
	readDaemonStatusFn := r.ReadDaemonStatus
	if readDaemonStatusFn == nil {
		readDaemonStatusFn = func(pidFile string) (daemon.Status, error) {
			return daemon.ReadStatus(pidFile, nil)
		}
	}
	dialIPCFn := r.DialIPC
	if dialIPCFn == nil {
		dialIPCFn = func(socketPath string) (ipcClient, error) {
			return ipc.Dial(socketPath)
		}
	}
	openDBFn := r.OpenDB
	if openDBFn == nil {
		openDBFn = func(path string) (dbCloser, error) {
			return db.Open(path)
		}
	}

	results := make([]Result, 0, 7)

	var p *paths.Paths
	p, err := pathsFn()
	if err != nil {
		results = append(results, Result{Name: "state directory", Detail: err.Error()})
	} else if err := p.EnsureDirs(); err != nil {
		results = append(results, Result{Name: "state directory", Detail: err.Error()})
	} else {
		results = append(results, Result{Name: "state directory", OK: true, Detail: p.Root()})
	}

	ghAvailable := false
	ghPath, err := lookPathFn("gh")
	if err != nil {
		results = append(results, Result{Name: "gh CLI", Detail: err.Error()})
	} else {
		ghAvailable = true
		results = append(results, Result{Name: "gh CLI", OK: true, Detail: ghPath})
	}

	env, err := resolveShellEnvFn()
	if err != nil {
		results = append(results, Result{Name: "shell environment", Detail: err.Error()})
	} else {
		results = append(results, Result{Name: "shell environment", OK: true, Detail: fmt.Sprintf("loaded %d variables", len(env))})
	}

	if !ghAvailable {
		results = append(results, Result{Name: "gh auth", Detail: "skipped: gh CLI unavailable"})
	} else if err := checkGitHubAuthFn(ctx); err != nil {
		results = append(results, Result{Name: "gh auth", Detail: err.Error()})
	} else {
		results = append(results, Result{Name: "gh auth", OK: true, Detail: "authenticated"})
	}

	if p == nil {
		results = append(results,
			Result{Name: "agent CLI", Detail: "resolve paths: state directory unavailable"},
			Result{Name: "daemon", Detail: "resolve paths: state directory unavailable"},
			Result{Name: "database", Detail: "resolve paths: state directory unavailable"},
		)
		return results
	}

	cfg, err := loadConfigFn(p.Root() + "/config.yaml")
	if err != nil {
		results = append(results, Result{Name: "agent CLI", Detail: err.Error()})
	} else {
		results = append(results, checkAgent(lookPathFn, cfg.Agent))
	}

	status, err := readDaemonStatusFn(p.PIDPath())
	if err != nil {
		results = append(results, Result{Name: "daemon", Detail: err.Error()})
	} else {
		detail := status.State
		if status.State == daemon.StateRunning {
			client, err := dialIPCFn(p.IPCPath())
			if err != nil {
				results = append(results, Result{Name: "daemon", Detail: fmt.Sprintf("running pid=%d; health check failed: %s", status.PID, err)})
			} else {
				defer client.Close()
				var health ipc.HealthResult
				if err := client.Call(ipc.MethodHealth, ipc.HealthParams{}, &health); err != nil {
					results = append(results, Result{Name: "daemon", Detail: fmt.Sprintf("running pid=%d; health check failed: %s", status.PID, err)})
				} else if strings.TrimSpace(strings.ToLower(health.Status)) != "ok" {
					results = append(results, Result{Name: "daemon", Detail: fmt.Sprintf("running pid=%d; health check returned %q", status.PID, health.Status)})
				} else {
					results = append(results, Result{Name: "daemon", OK: true, Detail: fmt.Sprintf("running pid=%d; ipc ok", status.PID)})
				}
			}
		} else {
			detail = "stopped; run `ezoss daemon start` to resume polling"
			if status.Stale {
				detail = fmt.Sprintf("stopped (stale pid=%d); run `ezoss daemon start` to resume polling", status.PID)
			}
			results = append(results, Result{Name: "daemon", Warning: true, Detail: detail})
		}
	}

	database, err := openDBFn(p.DBPath())
	if err != nil {
		results = append(results, Result{Name: "database", Detail: err.Error()})
	} else {
		_ = database.Close()
		results = append(results, Result{Name: "database", OK: true, Detail: p.DBPath()})
	}

	return results
}

func checkGitHubAuth(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	detail := strings.TrimSpace(string(output))
	if detail != "" {
		return fmt.Errorf("gh auth status: %s", detail)
	}
	if errors := strings.TrimSpace(err.Error()); errors != "" {
		return fmt.Errorf("gh auth status: %s", errors)
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ProcessState != nil {
		return fmt.Errorf("gh auth status: exit %d", exitErr.ProcessState.ExitCode())
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("gh auth status: %s", err)
	}
	return fmt.Errorf("gh auth status failed")
}

func checkAgent(lookPath func(string) (string, error), name sharedtypes.AgentName) Result {
	if name == "" {
		return Result{Name: "agent CLI", OK: true, Detail: "no agent configured"}
	}

	resolvedName, path, err := agent.Resolve(name, lookPath)
	if err != nil {
		return Result{Name: "agent CLI", Detail: err.Error()}
	}
	if name == sharedtypes.AgentAuto {
		return Result{Name: "agent CLI", OK: true, Detail: fmt.Sprintf("%s (auto -> %s)", path, resolvedName)}
	}

	return Result{Name: "agent CLI", OK: true, Detail: path}
}
