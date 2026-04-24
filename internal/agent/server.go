package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	managedServerOutputMu sync.Mutex
	managedServerOutput   io.Writer = os.Stderr
)

func SetManagedServerOutput(w io.Writer) {
	managedServerOutputMu.Lock()
	defer managedServerOutputMu.Unlock()
	if w == nil {
		w = os.Stderr
	}
	managedServerOutput = w
}

func currentManagedServerOutput() io.Writer {
	managedServerOutputMu.Lock()
	defer managedServerOutputMu.Unlock()
	return managedServerOutput
}

type managedServer struct {
	cmd     *exec.Cmd
	port    int
	exited  chan struct{}
	waitErr error
}

func getAvailablePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

func startServerWithPort(ctx context.Context, bin string, args []string, cwd string, healthPath string, port int) (*managedServer, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Stdin = nil
	out := currentManagedServerOutput()
	cmd.Stdout = out
	cmd.Stderr = out
	configureManagedServerCmd(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start server %s: %w", bin, err)
	}

	srv := &managedServer{cmd: cmd, port: port, exited: make(chan struct{})}
	go func() {
		srv.waitErr = cmd.Wait()
		close(srv.exited)
	}()

	if err := srv.waitForHealth(ctx, healthPath); err != nil {
		srv.shutdown()
		return nil, err
	}

	return srv, nil
}

func (s *managedServer) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

func (s *managedServer) waitForHealth(ctx context.Context, path string) error {
	url := s.baseURL() + path
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.After(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.exited:
			return fmt.Errorf("server exited before becoming healthy: %w", s.waitErr)
		case <-deadline:
			return fmt.Errorf("server health check timed out after 30s")
		default:
		}

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-s.exited:
			return fmt.Errorf("server exited before becoming healthy: %w", s.waitErr)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *managedServer) shutdown() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}

	select {
	case <-s.exited:
		return
	default:
	}

	_ = signalManagedProcess(s.cmd, false)

	select {
	case <-s.exited:
		return
	case <-time.After(3 * time.Second):
	}

	slog.Warn("server did not exit gracefully, sending SIGKILL", "pid", s.cmd.Process.Pid)
	_ = signalManagedProcess(s.cmd, true)

	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
		slog.Warn("server process did not exit after SIGKILL", "pid", s.cmd.Process.Pid)
	}
}
