package mqtt

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDefaultMessageHandler_HAState(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	h := defaultMessageHandler(logger)
	payload := `{"entity_id":"sensor.temperature","state":"22.5"}`
	h("homeassistant/sensor/temperature/state", []byte(payload))

	output := buf.String()
	if !strings.Contains(output, "entity_id=sensor.temperature") {
		t.Errorf("expected entity_id in log output, got: %s", output)
	}
	if !strings.Contains(output, "state=22.5") {
		t.Errorf("expected state in log output, got: %s", output)
	}
	if !strings.Contains(output, "payload_size=") {
		t.Errorf("expected payload_size in log output, got: %s", output)
	}
}

func TestDefaultMessageHandler_Frigate(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	h := defaultMessageHandler(logger)
	payload := `{"type":"new","after":{"id":"abc123"}}`
	h("frigate/events", []byte(payload))

	output := buf.String()
	if !strings.Contains(output, "event_type=new") {
		t.Errorf("expected event_type in log output, got: %s", output)
	}
}

func TestDefaultMessageHandler_PlainText(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	h := defaultMessageHandler(logger)
	// Plain text payload (not JSON) should not panic.
	h("some/topic", []byte("just a string"))

	output := buf.String()
	if !strings.Contains(output, "topic=some/topic") {
		t.Errorf("expected topic in log output, got: %s", output)
	}
	if !strings.Contains(output, "payload_size=13") {
		t.Errorf("expected payload_size=13 in log output, got: %s", output)
	}
}

func TestMessageRateLimiter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rl := newMessageRateLimiter(5, time.Second, logger)

	// First 5 should be allowed.
	for i := range 5 {
		if !rl.allow() {
			t.Errorf("message %d should have been allowed", i)
		}
	}

	// 6th should be dropped.
	if rl.allow() {
		t.Error("message 6 should have been rate-limited")
	}

	if dropped := rl.dropped.Load(); dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

func TestMessageRateLimiter_Concurrent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rl := newMessageRateLimiter(1000, time.Second, logger)

	// Hammer the rate limiter from multiple goroutines.
	done := make(chan struct{})
	for range 10 {
		go func() {
			for range 200 {
				rl.allow()
			}
			done <- struct{}{}
		}()
	}
	for range 10 {
		<-done
	}

	// count tracks all calls to allow(); dropped tracks the subset
	// that exceeded the limit. So count should equal total calls.
	count := rl.count.Load()
	if count != 2000 {
		t.Errorf("count = %d, want 2000", count)
	}
	// With limit 1000 and 2000 calls, exactly 1000 should be dropped.
	dropped := rl.dropped.Load()
	if dropped != 1000 {
		t.Errorf("dropped = %d, want 1000", dropped)
	}
}
