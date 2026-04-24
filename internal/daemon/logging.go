package daemon

import (
	"bufio"
	"fmt"
	"io"
	"time"
)

const timestampFormat = "2006-01-02T15:04:05.000Z"

// pipeTimestamps reads lines from r and writes them to out with a UTC
// timestamp prefix, returning when r reaches EOF. The clock parameter is
// the time source used for each emitted line; pass time.Now in production.
func pipeTimestamps(r io.Reader, out io.Writer, clock func() time.Time) error {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		ts := clock().UTC().Format(timestampFormat)
		if _, err := fmt.Fprintf(out, "%s %s\n", ts, scanner.Text()); err != nil {
			return err
		}
	}
	return scanner.Err()
}
