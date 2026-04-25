package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
)

// MQTTPublisher is the subset of [mqtt.Publisher] used by the
// telemetry publisher. Defined as an interface for testability.
type MQTTPublisher interface {
	PublishDynamicState(ctx context.Context, entitySuffix, state string, attrJSON []byte) error
	RegisterSensors(sensors []mqtt.DynamicSensor)
}

// Publisher bridges collected metrics to MQTT state publishing. On
// each call to [Publisher.Publish], it collects fresh metrics and
// publishes state values for every registered telemetry sensor.
// New loops discovered at publish time are registered as dynamic
// sensors automatically.
type Publisher struct {
	collector *Collector
	mqtt      MQTTPublisher
	builder   *SensorBuilder
	logger    *slog.Logger

	mu         sync.Mutex
	knownLoops map[string]bool
}

// NewPublisher creates a telemetry publisher that collects metrics via
// the given collector and publishes them through the MQTT publisher.
func NewPublisher(collector *Collector, mqttPub MQTTPublisher, builder *SensorBuilder, logger *slog.Logger) *Publisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{
		collector:  collector,
		mqtt:       mqttPub,
		builder:    builder,
		logger:     logger,
		knownLoops: make(map[string]bool),
	}
}

// Publish collects a fresh metrics snapshot and publishes all sensor
// states to MQTT. Called by the mqtt-telemetry loop handler on each
// iteration.
func (p *Publisher) Publish(ctx context.Context) error {
	m := p.collector.Collect(ctx)

	// Track publish errors but don't abort — publish as many as possible.
	var errs int

	// System health — DB sizes.
	for _, db := range []struct{ name, suffix string }{
		{"main", "db_main_size"},
		{"logs", "db_logs_size"},
		{"usage", "db_usage_size"},
		{"attachments", "db_attachments_size"},
	} {
		val := m.DBSizes[db.name]
		if err := p.publish(ctx, db.suffix, strconv.FormatInt(val, 10)); err != nil {
			errs++
		}
	}

	// Token usage.
	p.publishInt64(ctx, "tokens_24h_input", m.TokensInput, &errs)
	p.publishInt64(ctx, "tokens_24h_output", m.TokensOutput, &errs)

	costStr := strconv.FormatFloat(m.TokensCost, 'f', 4, 64)
	if len(m.TokensByModel) > 0 {
		// Publish state + per-model breakdown as attributes in one call.
		attrs, err := json.Marshal(m.TokensByModel)
		if err != nil {
			p.logger.Error("telemetry: marshal tokens by model", "error", err)
			errs++
		} else if err := p.publishWithAttrs(ctx, "tokens_24h_cost", costStr, attrs); err != nil {
			errs++
		}
	} else {
		if err := p.publish(ctx, "tokens_24h_cost", costStr); err != nil {
			errs++
		}
	}

	// Sessions & context.
	p.publishInt(ctx, "active_sessions", m.ActiveSessions, &errs)

	utilStr := strconv.FormatFloat(m.ContextUtilization, 'f', 1, 64)
	if err := p.publish(ctx, "context_utilization", utilStr); err != nil {
		errs++
	}

	// Request performance.
	p.publishInt(ctx, "requests_24h", m.Requests24h, &errs)
	p.publishInt(ctx, "errors_24h", m.Errors24h, &errs)
	p.publishFloat(ctx, "request_latency_p50", m.LatencyP50Ms, &errs)
	p.publishFloat(ctx, "request_latency_p95", m.LatencyP95Ms, &errs)

	// Loop aggregates.
	p.publishInt(ctx, "loops_active", m.LoopsActive, &errs)
	p.publishInt(ctx, "loops_sleeping", m.LoopsSleeping, &errs)
	p.publishInt(ctx, "loops_errored", m.LoopsErrored, &errs)
	p.publishInt(ctx, "loops_total", m.LoopsTotal, &errs)

	// Per-loop sensors.
	p.publishLoopDetails(ctx, m.LoopDetails, &errs)

	// Attachment store.
	p.publishInt64(ctx, "attachments_total", m.AttachmentsTotal, &errs)
	p.publishInt64(ctx, "attachments_total_bytes", m.AttachmentsTotalBytes, &errs)
	p.publishInt64(ctx, "attachments_unique_files", m.AttachmentsUnique, &errs)

	if errs > 0 {
		return fmt.Errorf("telemetry: %d publish errors", errs)
	}
	return nil
}

// publishLoopDetails publishes per-loop state and iteration sensors.
// New loops are detected and their sensors registered dynamically.
// Loop names are sanitized for use in MQTT topic paths.
func (p *Publisher) publishLoopDetails(ctx context.Context, details []LoopMetric, errs *int) {
	// Determine newly seen loops under lock, then release before I/O.
	var newLoops []string

	p.mu.Lock()
	for _, lm := range details {
		if !p.knownLoops[lm.Name] {
			p.knownLoops[lm.Name] = true
			newLoops = append(newLoops, lm.Name)
		}
	}
	p.mu.Unlock()

	// Register sensors for newly discovered loops (may do MQTT I/O).
	for _, loopName := range newLoops {
		sensors := p.builder.LoopSensors(loopName)
		p.mqtt.RegisterSensors(sensors)
		p.logger.Info("telemetry: registered loop sensors",
			"loop", loopName, "sensors", len(sensors))
	}

	// Publish per-loop state and iteration counts.
	for _, lm := range details {
		slug := sanitizeLoopName(lm.Name)
		stateSuffix := "loop_" + slug + "_state"
		iterSuffix := "loop_" + slug + "_iterations"

		if err := p.publish(ctx, stateSuffix, lm.State); err != nil {
			*errs++
		}
		if err := p.publish(ctx, iterSuffix, strconv.Itoa(lm.Iterations)); err != nil {
			*errs++
		}
	}
}

// publish publishes a state value for the given entity suffix.
func (p *Publisher) publish(ctx context.Context, entity, state string) error {
	if err := p.mqtt.PublishDynamicState(ctx, entity, state, nil); err != nil {
		p.logger.Debug("telemetry publish failed", "entity", entity, "error", err)
		return err
	}
	return nil
}

// publishWithAttrs publishes state and JSON attributes.
func (p *Publisher) publishWithAttrs(ctx context.Context, entity, state string, attrs []byte) error {
	return p.mqtt.PublishDynamicState(ctx, entity, state, attrs)
}

func (p *Publisher) publishInt(ctx context.Context, entity string, val int, errs *int) {
	if err := p.publish(ctx, entity, strconv.Itoa(val)); err != nil {
		*errs++
	}
}

func (p *Publisher) publishInt64(ctx context.Context, entity string, val int64, errs *int) {
	if err := p.publish(ctx, entity, strconv.FormatInt(val, 10)); err != nil {
		*errs++
	}
}

func (p *Publisher) publishFloat(ctx context.Context, entity string, val float64, errs *int) {
	if err := p.publish(ctx, entity, strconv.FormatFloat(val, 'f', 1, 64)); err != nil {
		*errs++
	}
}
