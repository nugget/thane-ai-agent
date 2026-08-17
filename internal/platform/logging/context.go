// Package logging provides dataset-partitioned JSONL log retention,
// context-propagated structured logging, and a queryable SQLite index
// for Thane.
//
// Structured filesystem retention is written as append-only JSONL
// datasets, partitioned by dataset/date/hour:
//
//	logs/
//	  events/2026-04-21/15.jsonl
//	  requests/2026-04-21/15.jsonl
//	  access/2026-04-21/15.jsonl
//	  loops/2026-04-21/15.jsonl
//	  delegates/2026-04-21/15.jsonl
//	  envelopes/2026-04-21/15.jsonl
//	  logs.db
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
// retention while preserving the dataset files as the canonical
// record.
package logging

import (
	"context"
	"log/slog"
	"strings"
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
	if l, ok := LoggerFrom(ctx); ok {
		return l
	}
	return slog.Default()
}

// LoggerFrom returns the logger stored by [WithLogger] and whether one
// was actually there.
//
// [Logger] answers "give me something I can log to", which is what
// almost every caller wants. This answers "did the caller attach one",
// which is a different question and the only one a component holding its
// own configured logger can act on: without the distinction it cannot
// tell a request carrying trace fields from a bare context, and
// preferring slog.Default over its own handler silently discards the
// configuration it was built with.
func LoggerFrom(ctx context.Context) (*slog.Logger, bool) {
	if ctx == nil {
		return nil, false
	}
	l, ok := ctx.Value(contextKey{}).(*slog.Logger)
	if !ok || l == nil {
		return nil, false
	}
	return l, true
}

// requestIDKey carries the agent's request identifier.
type requestIDKey struct{}

// WithRequestID stores the identifier for the current request→response
// cycle so code far from the entry point can name it.
//
// The context logger already carries request_id as an attribute, which
// is enough to correlate log lines but not to put the identifier
// anywhere else — an slog.Logger will not give its attributes back. A
// provider that wants to send the id upstream as a client request
// header, so a failure can be matched against the server's own logs,
// needs the value rather than a logger that happens to mention it.
func WithRequestID(ctx context.Context, id string) context.Context {
	if strings.TrimSpace(id) == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the current request identifier, or "" when
// the caller set none. Callers should treat the empty string as "do not
// claim an identity" rather than inventing one: a fabricated id that
// matches nothing on either side is worse than an absent header.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
