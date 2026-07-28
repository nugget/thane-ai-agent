package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

const (
	logAlertPartitionPrefix = "log-alert:"
	logAlertDebounce        = 15 * time.Second
	logAlertMaxWait         = time.Minute
)

// logAlertWakeFeeder turns live WARN/ERROR log events into durable,
// debounced metacognitive wakes. The log index remains the query surface; the
// wake is an attention signal carrying enough bounded context to decide
// whether a deeper logs_query investigation is warranted.
type logAlertWakeFeeder struct {
	bus        *events.Bus
	events     <-chan events.Event
	dispatcher *queuedWakeDispatcher
	logger     *slog.Logger
	done       chan struct{}
	once       sync.Once
}

func newLogAlertWakeFeeder(queue *loopqueue.Store, messageBus *messages.Bus, bus *events.Bus, logger *slog.Logger) *logAlertWakeFeeder {
	if queue == nil || messageBus == nil || bus == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "log_alert_wake")
	feeder := &logAlertWakeFeeder{
		bus:    bus,
		events: bus.Subscribe(1024),
		logger: logger,
		done:   make(chan struct{}),
	}
	feeder.dispatcher = newQueuedWakeDispatcher(
		queue,
		messageBus,
		logAlertPartitionPrefix,
		"log_alert",
		nil,
		logger,
	)
	feeder.dispatcher.register(feeder.partition(), logAlertDebounce, logAlertMaxWait)
	return feeder
}

func (f *logAlertWakeFeeder) run(ctx context.Context) {
	defer close(f.done)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-f.events:
			if !ok {
				return
			}
			f.ingest(event)
		}
	}
}

func (f *logAlertWakeFeeder) ingest(event events.Event) {
	if event.Source != events.SourceLog || event.Kind != events.KindLogRecord {
		return
	}
	if dataString(event.Data, "component") == "log_alert_wake" {
		return
	}
	if dataString(event.Data, "loop_name") == metacognitive.DefinitionName {
		// Keep metacognition's own failures on the operator/index surfaces,
		// but do not let a failing metacognitive turn recursively wake itself.
		return
	}
	level := dataString(event.Data, "level")
	if level != "WARN" && level != "ERROR" {
		return
	}
	message := dataString(event.Data, "message")
	source := dataString(event.Data, "source_file")
	loopName := dataString(event.Data, "loop_name")
	summary := promptfmt.MarshalCompact(map[string]any{
		"level":       level,
		"message":     message,
		"source":      source,
		"source_line": event.Data["source_line"],
		"component":   dataString(event.Data, "component"),
		"subsystem":   dataString(event.Data, "subsystem"),
		"loop_name":   loopName,
		"request_id":  dataString(event.Data, "request_id"),
	})
	record := queuedWakeRecord{
		Target: messages.LoopWakeTarget{
			Name:         metacognitive.DefinitionName,
			Instructions: "A production warning or error occurred. Assess impact, use logs_query with the supplied correlation fields when useful, and record or act on anything that needs attention.",
		},
		Event: messages.LoopEventPayload{
			Source:     "thane_log",
			Type:       strings.ToLower(level),
			ID:         fmt.Sprintf("log-%d", event.Timestamp.UnixNano()),
			Title:      message,
			Summary:    summary,
			ObservedAt: event.Timestamp,
		},
	}
	priority := 1
	if level == "ERROR" {
		priority = 2
	}
	if err := f.dispatcher.enqueue(f.partition(), logAlertDedupKey(level, message, source, loopName), priority, record); err != nil {
		f.logger.Warn("log alert enqueue failed",
			"alert_level", level,
			"alert_message", message,
			"error", err,
		)
	}
}

func (f *logAlertWakeFeeder) partition() string {
	return logAlertPartitionPrefix + "name:" + metacognitive.DefinitionName
}

func (f *logAlertWakeFeeder) Sweep(ctx context.Context) {
	f.dispatcher.Sweep(ctx)
}

func (f *logAlertWakeFeeder) close() {
	f.once.Do(func() {
		f.bus.Unsubscribe(f.events)
		<-f.done
	})
}

func logAlertDedupKey(level, message, source, loopName string) string {
	sum := sha256.Sum256([]byte(level + "\x00" + message + "\x00" + source + "\x00" + loopName))
	return "alert:" + hex.EncodeToString(sum[:8])
}

func dataString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}
