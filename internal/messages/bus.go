package messages

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DeliveryStatus describes the outcome of one routed envelope.
type DeliveryStatus string

const (
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryQueued    DeliveryStatus = "queued"
)

// DeliveryResult summarizes one routed envelope.
type DeliveryResult struct {
	Envelope Envelope       `json:"envelope"`
	Route    string         `json:"route"`
	Status   DeliveryStatus `json:"status"`
	Details  any            `json:"details,omitempty"`
}

// HandlerFunc delivers one normalized envelope for a destination kind.
type HandlerFunc func(context.Context, Envelope) (DeliveryResult, error)

// AuditFunc records attempted bus deliveries for later inspection.
type AuditFunc func(context.Context, Envelope, *DeliveryResult, error)

func loggingAuditFunc(logger *slog.Logger) AuditFunc {
	return func(_ context.Context, env Envelope, result *DeliveryResult, err error) {
		if logger == nil {
			return
		}
		fields := []any{
			"envelope_id", env.ID,
			"type", env.Type,
			"from_kind", env.From.Kind,
			"from_name", env.From.Name,
			"from_id", env.From.ID,
			"to_kind", env.To.Kind,
			"to_target", env.To.Target,
			"to_selector", env.To.Selector,
			"priority", env.Priority,
		}
		if result != nil {
			fields = append(fields,
				"route", result.Route,
				"delivery_status", result.Status,
			)
		}
		if err != nil {
			fields = append(fields, "error", err)
			logger.Warn("message envelope delivery failed", fields...)
			return
		}
		msg := "message envelope delivered"
		if result != nil && result.Status == DeliveryQueued {
			msg = "message envelope queued"
		}
		logger.Info(msg, fields...)
	}
}

// Bus routes normalized envelopes to destination-specific handlers.
type Bus struct {
	mu       sync.RWMutex
	handlers map[DestinationKind]HandlerFunc
	logger   *slog.Logger
	audit    AuditFunc
	now      func() time.Time
}

// NewBus constructs a message bus with a logging-backed audit sink.
func NewBus(logger *slog.Logger) *Bus {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bus{
		handlers: make(map[DestinationKind]HandlerFunc),
		logger:   logger,
		audit:    loggingAuditFunc(logger.With("component", "message_bus")),
		now:      time.Now,
	}
}

// RegisterRoute installs a destination-specific delivery handler.
func (b *Bus) RegisterRoute(kind DestinationKind, handler HandlerFunc) {
	if b == nil || handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[kind] = handler
}

// Send validates and routes one envelope.
func (b *Bus) Send(ctx context.Context, env Envelope) (DeliveryResult, error) {
	if b == nil {
		return DeliveryResult{}, fmt.Errorf("message bus is not configured")
	}
	env, err := env.Normalize(b.now())
	if err != nil {
		return DeliveryResult{}, err
	}

	b.mu.RLock()
	handler := b.handlers[env.To.Kind]
	audit := b.audit
	b.mu.RUnlock()
	if handler == nil {
		err := fmt.Errorf("no message route registered for destination kind %q", env.To.Kind)
		if audit != nil {
			audit(ctx, env, nil, err)
		}
		return DeliveryResult{}, err
	}

	result, err := handler(ctx, env)
	if err == nil {
		result.Envelope = env
	}
	if audit != nil {
		audit(ctx, env, &result, err)
	}
	return result, err
}
