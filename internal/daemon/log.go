package daemon

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a slog.Logger that writes text records to w. The
// handler suppresses the time attribute on purpose: the daemon's
// stderr is piped through InstallTimestampedLogPipe which already
// prefixes every line with a UTC timestamp, so a second time= field
// would be redundant. Level is controlled by EZOSS_LOG_LEVEL
// (debug | info | warn | error). Default is info. Passing a nil
// writer falls back to stderr.
func NewLogger(w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: parseLogLevel(os.Getenv("EZOSS_LOG_LEVEL")),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
