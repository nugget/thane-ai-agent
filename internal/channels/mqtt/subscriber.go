package mqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// MessageHandler is called for each MQTT message received on a
// subscribed topic. Implementations must be safe for concurrent use.
type MessageHandler func(topic string, payload []byte)

// defaultMessageHandler returns a [MessageHandler] that logs received
// messages at debug level with structured fields. For HA state topics
// it attempts to parse the JSON payload to extract entity_id and
// state. For Frigate topics it extracts the event type. Non-JSON
// payloads are handled gracefully (logged with topic and size only).
func defaultMessageHandler(logger *slog.Logger) MessageHandler {
	return func(topic string, payload []byte) {
		if !logger.Enabled(context.Background(), slog.LevelDebug) {
			return
		}

		fields := []any{
			"topic", topic,
			"payload_size", len(payload),
		}

		// HA state topics typically contain JSON with entity_id and state.
		if strings.Contains(topic, "/state") {
			var state map[string]any
			if err := json.Unmarshal(payload, &state); err == nil {
				if entityID, ok := state["entity_id"]; ok {
					fields = append(fields, "entity_id", entityID)
				}
				if s, ok := state["state"]; ok {
					fields = append(fields, "state", s)
				}
			}
		}

		// Frigate event topics carry JSON with a type field.
		if strings.HasPrefix(topic, "frigate/") {
			var event map[string]any
			if err := json.Unmarshal(payload, &event); err == nil {
				if eventType, ok := event["type"]; ok {
					fields = append(fields, "event_type", eventType)
				}
			}
		}

		logger.Debug("mqtt message received", fields...)
	}
}

// messageRateLimiter tracks inbound message rates and drops messages
// when the rate exceeds the configured threshold. It uses atomic
// counters for lock-free operation on the hot path.
type messageRateLimiter struct {
	count    atomic.Int64
	dropped  atomic.Int64
	limit    int64
	interval time.Duration
	logger   *slog.Logger
}

// newMessageRateLimiter creates a rate limiter that allows limit
// messages per interval. Exceeding the limit causes messages to be
// dropped until the next interval reset.
func newMessageRateLimiter(limit int64, interval time.Duration, logger *slog.Logger) *messageRateLimiter {
	return &messageRateLimiter{
		limit:    limit,
		interval: interval,
		logger:   logger,
	}
}

// start runs the periodic counter reset loop. It blocks until ctx is
// cancelled. At each interval boundary it resets the message counter
// and logs a warning if any messages were dropped.
func (r *messageRateLimiter) start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count := r.count.Swap(0)
			dropped := r.dropped.Swap(0)
			if dropped > 0 {
				r.logger.Warn("mqtt messages dropped due to rate limit",
					"received", count,
					"dropped", dropped,
					"interval", r.interval.String(),
					"limit", r.limit,
				)
			}
		}
	}
}

// allow increments the message counter and returns true if the
// current count is within the limit. If over the limit it increments
// the dropped counter and returns false.
func (r *messageRateLimiter) allow() bool {
	n := r.count.Add(1)
	if n > r.limit {
		r.dropped.Add(1)
		return false
	}
	return true
}
