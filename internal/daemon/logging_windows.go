//go:build windows

package daemon

import "io"

// InstallTimestampedLogPipe is a no-op on Windows; daemon log lines on
// this platform remain untimestamped at process level.
func InstallTimestampedLogPipe(_ io.Writer) (func(), error) {
	return func() {}, nil
}
