package messages

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Type describes the delivery contract a message expects.
type Type string

const (
	TypeRequest   Type = "request"
	TypeReply     Type = "reply"
	TypeSignal    Type = "signal"
	TypeBroadcast Type = "broadcast"
	TypeStatus    Type = "status"
)

// IdentityKind identifies the sender category.
type IdentityKind string

const (
	IdentityCore     IdentityKind = "core"
	IdentityLoop     IdentityKind = "loop"
	IdentityDelegate IdentityKind = "delegate"
	IdentitySystem   IdentityKind = "system"
)

// DestinationKind identifies how the bus should route the envelope.
type DestinationKind string

const (
	DestinationLoop DestinationKind = "loop"
)

// Selector identifies how a destination target should be interpreted.
type Selector string

const (
	SelectorName Selector = "name"
	SelectorID   Selector = "id"
)

// Priority describes relative delivery urgency.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityUrgent Priority = "urgent"
)

// Duration renders as a Go duration string instead of raw nanoseconds in JSON.
type Duration time.Duration

// IsZero reports whether the duration is unset.
func (d Duration) IsZero() bool { return time.Duration(d) <= 0 }

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts a Go duration string.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	if d == nil {
		return fmt.Errorf("messages: nil duration")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("messages: duration must be a string: %w", err)
	}
	if strings.TrimSpace(s) == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("messages: invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Identity identifies the sender of an envelope.
type Identity struct {
	Kind IdentityKind `json:"kind"`
	Name string       `json:"name,omitempty"`
	ID   string       `json:"id,omitempty"`
}

// Destination identifies where an envelope should be delivered.
type Destination struct {
	Kind     DestinationKind `json:"kind"`
	Target   string          `json:"target,omitempty"`
	Selector Selector        `json:"selector,omitempty"`
}

// Envelope is the shared inter-component message primitive.
type Envelope struct {
	ID        string       `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	From      Identity     `json:"from"`
	To        Destination  `json:"to"`
	Type      Type         `json:"type"`
	Payload   any          `json:"payload,omitempty"`
	ReplyTo   *Destination `json:"reply_to,omitempty"`
	TTL       Duration     `json:"ttl,omitempty"`
	Priority  Priority     `json:"priority,omitempty"`
	Scope     []string     `json:"scope,omitempty"`

	// Cryptographic fields are intentionally present now; signing is wired by #697.
	Signature  []byte `json:"signature,omitempty"`
	SignerCert []byte `json:"signer_cert,omitempty"`
	ChainDepth int    `json:"chain_depth,omitempty"`
}

// Normalize validates the envelope and fills default ID/timestamps.
func (e Envelope) Normalize(now time.Time) (Envelope, error) {
	if e.Type == "" {
		return Envelope{}, fmt.Errorf("messages: type is required")
	}
	switch e.Type {
	case TypeRequest, TypeReply, TypeSignal, TypeBroadcast, TypeStatus:
	default:
		return Envelope{}, fmt.Errorf("messages: unsupported type %q", e.Type)
	}
	if e.From.Kind == "" {
		return Envelope{}, fmt.Errorf("messages: from.kind is required")
	}
	if e.To.Kind == "" {
		return Envelope{}, fmt.Errorf("messages: to.kind is required")
	}
	e.From.Name = strings.TrimSpace(e.From.Name)
	e.From.ID = strings.TrimSpace(e.From.ID)
	target := strings.TrimSpace(e.To.Target)
	if target == "" {
		return Envelope{}, fmt.Errorf("messages: to.target is required")
	}
	e.To.Target = target
	switch e.To.Kind {
	case DestinationLoop:
		if e.To.Selector == "" {
			e.To.Selector = SelectorName
		}
		if e.To.Selector != SelectorName && e.To.Selector != SelectorID {
			return Envelope{}, fmt.Errorf("messages: unsupported loop selector %q", e.To.Selector)
		}
	default:
		return Envelope{}, fmt.Errorf("messages: unsupported destination kind %q", e.To.Kind)
	}
	if e.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return Envelope{}, fmt.Errorf("messages: generate id: %w", err)
		}
		e.ID = id.String()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now.UTC()
	}
	if e.Priority == "" {
		e.Priority = PriorityNormal
	}
	if e.TTL.Std() < 0 {
		return Envelope{}, fmt.Errorf("messages: ttl must be non-negative")
	}
	if len(e.Scope) > 0 {
		e.Scope = append([]string(nil), e.Scope...)
	}
	if len(e.Signature) > 0 {
		e.Signature = append([]byte(nil), e.Signature...)
	}
	if len(e.SignerCert) > 0 {
		e.SignerCert = append([]byte(nil), e.SignerCert...)
	}
	return e, nil
}

// LoopNotifyPayload is the first concrete loop-notification payload shape.
type LoopNotifyPayload struct {
	Message         string `json:"message,omitempty"`
	ForceSupervisor bool   `json:"force_supervisor,omitempty"`
}
