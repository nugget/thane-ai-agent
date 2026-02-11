package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// LevelTrace is a custom slog level below [slog.LevelDebug], intended for
// wire-level forensics (full JSON request/response payloads). The numeric
// value -8 follows the convention established by OpenTelemetry and other
// Go projects that extend slog with a Trace level.
//
// Use sparingly — Trace output is extremely verbose and should only be
// enabled when diagnosing provider-specific bugs.
const LevelTrace = slog.Level(-8)

// ParseLogLevel converts a case-insensitive string to an [slog.Level].
//
// Accepted values:
//   - "trace" → [LevelTrace] (wire-level payloads)
//   - "debug" → [slog.LevelDebug] (per-request detail)
//   - "info" or "" → [slog.LevelInfo] (normal operation)
//   - "warn" or "warning" → [slog.LevelWarn]
//   - "error" → [slog.LevelError]
//
// Returns an error for unrecognized values. Leading and trailing
// whitespace is trimmed before matching.
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

// ReplaceLogLevelNames is an [slog.HandlerOptions.ReplaceAttr] function
// that renders [LevelTrace] as "TRACE" in log output. Without this,
// slog would render it as "DEBUG-4" since it doesn't know about custom
// levels.
//
// Pass it as the ReplaceAttr field when constructing a handler:
//
//	slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
//	    Level:       config.LevelTrace,
//	    ReplaceAttr: config.ReplaceLogLevelNames,
//	})
func ReplaceLogLevelNames(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey {
		level, ok := a.Value.Any().(slog.Level)
		if ok && level == LevelTrace {
			a.Value = slog.StringValue("TRACE")
		}
	}
	return a
}
