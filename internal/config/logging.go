package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// LevelTrace is a custom log level below Debug for wire-level forensics.
const LevelTrace = slog.Level(-8)

// ParseLogLevel converts a string to a slog.Level.
// Supported values: trace, debug, info, warn, error (case-insensitive).
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (valid: trace, debug, info, warn, error)", s)
	}
}

// ReplaceLogLevelNames customizes the level name for Trace in log output.
func ReplaceLogLevelNames(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey {
		level, ok := a.Value.Any().(slog.Level)
		if ok && level == LevelTrace {
			a.Value = slog.StringValue("TRACE")
		}
	}
	return a
}
