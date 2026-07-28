package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

func TestEventHandlerPublishesWarningsAndErrors(t *testing.T) {
	var output bytes.Buffer
	bus := events.New()
	ch := bus.Subscribe(4)
	t.Cleanup(func() { bus.Unsubscribe(ch) })

	logger := slog.New(NewEventHandler(
		slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}),
		bus,
	)).With("component", "iterate", "loop_name", "email-default-handler")

	logger.Info("ordinary progress")
	select {
	case event := <-ch:
		t.Fatalf("INFO unexpectedly published: %+v", event)
	default:
	}

	logger.Warn("illegal tool call", "tool", "email_read")
	event := <-ch
	if event.Source != events.SourceLog || event.Kind != events.KindLogRecord {
		t.Fatalf("event = %+v", event)
	}
	if event.Data["level"] != "WARN" || event.Data["message"] != "illegal tool call" {
		t.Fatalf("event data = %+v", event.Data)
	}
	if event.Data["component"] != "iterate" ||
		event.Data["loop_name"] != "email-default-handler" ||
		event.Data["tool"] != "email_read" {
		t.Fatalf("structured attrs missing: %+v", event.Data)
	}
	if output.Len() == 0 {
		t.Fatal("wrapped log output is empty")
	}
}

func TestEventHandlerPropagatesGroups(t *testing.T) {
	bus := events.New()
	ch := bus.Subscribe(1)
	t.Cleanup(func() { bus.Unsubscribe(ch) })
	handler := NewEventHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), bus)
	logger := slog.New(handler.WithGroup("detail")).With("attempt", 2)

	logger.LogAttrs(context.Background(), slog.LevelError, "failed", slog.String("reason", "timeout"))
	event := <-ch
	if event.Data["detail.attempt"] != int64(2) || event.Data["detail.reason"] != "timeout" {
		t.Fatalf("group attrs = %+v", event.Data)
	}
}

func TestEventHandlerPreservesPromotedKind(t *testing.T) {
	bus := events.New()
	ch := bus.Subscribe(1)
	t.Cleanup(func() { bus.Unsubscribe(ch) })
	logger := slog.New(NewEventHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), bus))

	logger.Warn("request exhausted", "kind", "request_complete")
	event := <-ch
	if event.Data["kind"] != "request_complete" {
		t.Fatalf("kind = %#v, want request_complete", event.Data["kind"])
	}
}

func TestEventHandlerPublishesWarningWhenInnerSinkFails(t *testing.T) {
	bus := events.New()
	ch := bus.Subscribe(1)
	t.Cleanup(func() { bus.Unsubscribe(ch) })
	wantErr := errors.New("dataset unavailable")
	handler := NewEventHandler(errorHandler{err: wantErr}, bus)
	record := slog.NewRecord(time.Now(), slog.LevelWarn, "retention degraded", 0)

	if err := handler.Handle(context.Background(), record); !errors.Is(err, wantErr) {
		t.Fatalf("Handle error = %v, want %v", err, wantErr)
	}
	event := <-ch
	if event.Data["message"] != "retention degraded" {
		t.Fatalf("event = %+v", event)
	}
}

func TestEventHandlerBoundsPublishedProjection(t *testing.T) {
	bus := events.New()
	ch := bus.Subscribe(1)
	t.Cleanup(func() { bus.Unsubscribe(ch) })
	logger := slog.New(NewEventHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), bus))

	args := make([]any, 0, (maxEventAttrs+2)*2)
	for i := range maxEventAttrs + 2 {
		args = append(args, fmt.Sprintf("key_%02d", i), strings.Repeat("x", maxEventStringRunes+10))
	}
	logger.Warn(strings.Repeat("m", maxEventMessageRunes+10), args...)

	event := <-ch
	if event.Data["truncated"] != true {
		t.Fatalf("truncated = %#v, want true", event.Data["truncated"])
	}
	message, _ := event.Data["message"].(string)
	if got := len([]rune(message)); got != maxEventMessageRunes+1 {
		t.Fatalf("message runes = %d, want %d including ellipsis", got, maxEventMessageRunes+1)
	}
	attrCount := 0
	for key, value := range event.Data {
		if strings.HasPrefix(key, "key_") {
			attrCount++
			text, _ := value.(string)
			if got := len([]rune(text)); got > maxEventStringRunes+1 {
				t.Fatalf("%s runes = %d, exceeds cap", key, got)
			}
		}
	}
	if attrCount != maxEventAttrs {
		t.Fatalf("published attrs = %d, want %d", attrCount, maxEventAttrs)
	}
}

func TestTruncateEventStringPreservesUTF8Boundaries(t *testing.T) {
	got, truncated := truncateEventString("a🙂界z", 3)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if got != "a🙂界…" {
		t.Fatalf("got %q, want %q", got, "a🙂界…")
	}
	if got, truncated := truncateEventString("a🙂界", 3); truncated || got != "a🙂界" {
		t.Fatalf("exact-boundary result = %q, %v", got, truncated)
	}
}

type errorHandler struct {
	err error
}

func (h errorHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h errorHandler) Handle(context.Context, slog.Record) error {
	return h.err
}
func (h errorHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h errorHandler) WithGroup(string) slog.Handler      { return h }
