package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func TestLogger_FallbackToDefault(t *testing.T) {
	got := Logger(context.Background())
	if got != slog.Default() {
		t.Error("Logger(background) should return slog.Default()")
	}
}

func TestWithLogger_RoundTrip(t *testing.T) {
	custom := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	ctx := WithLogger(context.Background(), custom)
	got := Logger(ctx)

	if got != custom {
		t.Error("Logger did not return the injected logger")
	}
}

func TestWithLogger_Override(t *testing.T) {
	a := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	b := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	ctx := WithLogger(context.Background(), a)
	ctx = WithLogger(ctx, b)

	if got := Logger(ctx); got != b {
		t.Error("nested WithLogger should return the innermost logger")
	}
}

func TestLogger_NilSafety(t *testing.T) {
	ctx := WithLogger(context.Background(), nil)
	got := Logger(ctx)

	if got != slog.Default() {
		t.Error("Logger should return slog.Default() when nil was stored")
	}
}

func TestWithLogger_FieldsPropagated(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil)).With(
		"request_id", "r_test123",
		"subsystem", SubsystemAgent,
	)

	ctx := WithLogger(context.Background(), logger)
	Logger(ctx).Info("test message", "extra", "value")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse log JSON: %v", err)
	}

	checks := map[string]string{
		"request_id": "r_test123",
		"subsystem":  SubsystemAgent,
		"extra":      "value",
		"msg":        "test message",
	}
	for key, want := range checks {
		got, ok := entry[key].(string)
		if !ok {
			t.Errorf("key %q missing from log entry", key)
			continue
		}
		if got != want {
			t.Errorf("key %q = %q, want %q", key, got, want)
		}
	}
}

// TestLoggerFromDistinguishesAbsence pins the difference Logger cannot
// express. A component holding its own configured logger has to know
// whether the caller attached one; without that it cannot tell a request
// carrying trace fields from a bare context, and substituting
// slog.Default silently discards the handler it was built with.
func TestLoggerFromDistinguishesAbsence(t *testing.T) {
	t.Parallel()

	if _, ok := LoggerFrom(context.Background()); ok {
		t.Error("LoggerFrom reported a logger on a bare context")
	}
	if got := Logger(context.Background()); got == nil {
		t.Error("Logger returned nil instead of a usable default")
	}

	attached := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx := WithLogger(context.Background(), attached)
	got, ok := LoggerFrom(ctx)
	if !ok || got != attached {
		t.Errorf("LoggerFrom = (%v, %t), want the attached logger", got, ok)
	}

	// A nil stored logger is an absence, not a logger.
	if _, ok := LoggerFrom(WithLogger(context.Background(), nil)); ok {
		t.Error("LoggerFrom reported a nil logger as present")
	}
}
