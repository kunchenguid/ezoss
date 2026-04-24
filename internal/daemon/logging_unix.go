//go:build !windows

package daemon

import (
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// InstallTimestampedLogPipe redirects this process's stdout and stderr
// (fds 1 and 2) through a pipe whose contents are read line-by-line in
// the background and written to out with a UTC timestamp prefix. Child
// processes inherit the redirected fds so their output is also
// timestamped via this pipe.
//
// The returned cleanup func is a best-effort noop placeholder; the
// goroutine drains naturally when fds 1 and 2 close at process exit.
func InstallTimestampedLogPipe(out io.Writer) (cleanup func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create log pipe: %w", err)
	}

	if err := unix.Dup2(int(w.Fd()), int(os.Stdout.Fd())); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, fmt.Errorf("redirect stdout: %w", err)
	}
	if err := unix.Dup2(int(w.Fd()), int(os.Stderr.Fd())); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, fmt.Errorf("redirect stderr: %w", err)
	}
	// fds 1 and 2 hold dups of the pipe writer; close our local handle so
	// the reader sees EOF once fds 1 and 2 close (process exit).
	_ = w.Close()

	go func() {
		_ = pipeTimestamps(r, out, func() time.Time { return time.Now().UTC() })
	}()

	return func() {}, nil
}
