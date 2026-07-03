// Package logging installs the process-wide slog handler. Under systemd the
// agent's stdout/stderr already flow to journald, but as flat text: every
// record lands at the default priority with a doubled timestamp and the
// structured attributes squashed into the message. Configure instead writes
// natively to the journald socket so records carry PRIORITY (journalctl -p
// filtering works), SYSLOG_IDENTIFIER, and one queryable journal field per
// slog attribute. Outside systemd it falls back to human-readable text on
// stderr for interactive, dev, and container runs.
package logging

import (
	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/mordilloSan/go-monitoring/internal/logging/journald"
)

// FieldPrefix namespaces journal fields (for example GOMON_PLUGIN),
// mirroring LinuxIO's LINUXIO_* convention.
const FieldPrefix = "GOMON"

// level is the shared handler level; SetLevel adjusts it at runtime.
var level = new(slog.LevelVar)

// Configure installs the default slog handler and routes the standard
// library's log package through it. The initial level comes from the
// LOG_LEVEL environment variable (debug, info, warn, error; default info).
func Configure(identifier string) {
	level.Set(levelFromEnv())

	handler := newHandler(identifier)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// journald stamps every entry itself, so the bridge must not add
	// timestamps of its own.
	log.SetFlags(0)
	log.SetOutput(slog.NewLogLogger(handler, slog.LevelInfo).Writer())
}

// SetLevel adjusts the level of the handler installed by Configure.
func SetLevel(l slog.Level) {
	level.Set(l)
}

// newHandler prefers the native journald handler when the process was
// started by systemd with its output connected to the journal
// (JOURNAL_STREAM is set). Any failure — unset variable, missing socket —
// falls back to text on stderr.
func newHandler(identifier string) slog.Handler {
	if os.Getenv("JOURNAL_STREAM") != "" {
		handler, err := journald.NewHandler(journald.Options{
			Identifier:  identifier,
			Level:       level,
			AddSource:   true,
			FieldPrefix: FieldPrefix,
		})
		if err == nil {
			return handler
		}
	}
	return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
}

func levelFromEnv() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
