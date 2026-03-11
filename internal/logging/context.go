// Package logging provides self-managed log file rotation,
// context-propagated structured logging, and a queryable SQLite
// index for Thane.
//
// The [Rotator] implements [io.WriteCloser] and handles daily log
// rotation with optional gzip compression of previous days' files.
// Log files are organized by date:
//
//	logs/
//	  thane.log                    # current, active
//	  thane-2026-03-09.log.gz     # rotated daily, compressed
//	  thane-2026-03-08.log.gz
//	  ...never deleted...
//
// The [WithLogger] / [Logger] helpers thread a *[slog.Logger] through
// [context.Context] so that every log line in a request chain
// automatically carries trace fields (request_id, session,
// conversation, subsystem, iteration index).
//
// [ShortenSource] strips the module prefix from source file paths
// when slog's AddSource option is enabled, keeping log lines compact.
//
// The [IndexHandler] wraps any [slog.Handler] and simultaneously
// indexes every log record into a SQLite database. Promoted fields
// (request_id, session_id, conversation_id, subsystem, tool, model)
// are extracted into indexed columns for fast queries; remaining
// attributes go into a JSON catch-all. Use [Prune] to manage index
// retention while preserving the raw log files as the canonical
// record.
package logging

import (
	"context"
	"log/slog"
)

// contextKey is an unexported type to avoid collisions with other
// packages that store values in context.
type contextKey struct{}

// Standard subsystem names for structured log filtering.
const (
	SubsystemAgent     = "agent"
	SubsystemDelegate  = "delegate"
	SubsystemSignal    = "signal"
	SubsystemScheduler = "scheduler"
	SubsystemMetacog   = "metacog"
	SubsystemLoop      = "loop"
	SubsystemAPI       = "api"
)

// WithLogger returns a copy of ctx carrying logger. Retrieve it
// with [Logger]. Typically called at request entry points to inject
// a logger pre-enriched with trace fields (request_id, subsystem,
// etc.), then again at iteration boundaries to add the iteration
// index.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// Logger extracts the [*slog.Logger] stored by [WithLogger]. If no
// logger is present (or nil was stored), it returns [slog.Default]
// as a safe fallback so callers never need nil checks.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
