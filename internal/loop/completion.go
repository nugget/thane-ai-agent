package loop

import (
	"context"
	"fmt"
	"strings"
)

// CompletionChannelTarget identifies a concrete interactive channel target
// for a detached loop completion. This keeps transport selection in the
// app layer while giving launches and deliveries an honest, structured
// contract for channel-shaped results.
type CompletionChannelTarget struct {
	Channel        string `yaml:"channel,omitempty" json:"channel,omitempty"`
	Recipient      string `yaml:"recipient,omitempty" json:"recipient,omitempty"`
	ConversationID string `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
}

// Validate checks that the channel target is well formed.
func (t *CompletionChannelTarget) Validate() error {
	if t == nil {
		return fmt.Errorf("loop: completion channel target is required")
	}
	channel := strings.TrimSpace(t.Channel)
	if channel == "" {
		return fmt.Errorf("loop: completion channel target channel is required")
	}
	switch channel {
	case "signal":
		if strings.TrimSpace(t.Recipient) == "" {
			return fmt.Errorf("loop: signal completion channel target requires recipient")
		}
	case "owu":
		if strings.TrimSpace(t.ConversationID) == "" {
			return fmt.Errorf("loop: owu completion channel target requires conversation_id")
		}
	default:
		if strings.TrimSpace(t.Recipient) == "" && strings.TrimSpace(t.ConversationID) == "" {
			return fmt.Errorf("loop: completion channel target requires recipient or conversation_id")
		}
	}
	return nil
}

// CloneCompletionChannelTarget returns a shallow copy of the target so
// callers can safely retain it without sharing mutable state.
func CloneCompletionChannelTarget(t *CompletionChannelTarget) *CompletionChannelTarget {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

// CompletionDelivery is the normalized completion payload emitted by
// detached loops when they deliver a result through a non-return path.
type CompletionDelivery struct {
	Mode           Completion               `yaml:"mode,omitempty" json:"mode"`
	ConversationID string                   `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	Channel        *CompletionChannelTarget `yaml:"channel,omitempty" json:"channel,omitempty"`
	Content        string                   `yaml:"content,omitempty" json:"content"`
	LoopID         string                   `yaml:"loop_id,omitempty" json:"loop_id"`
	LoopName       string                   `yaml:"loop_name,omitempty" json:"loop_name"`
	Response       *Response                `yaml:"response,omitempty" json:"response,omitempty"`
	Status         *Status                  `yaml:"status,omitempty" json:"status,omitempty"`
}

// CompletionSink receives detached loop completion deliveries. The app
// layer wires concrete sinks (e.g. conversation injection) so the loop
// package can stay free of channel and memory dependencies.
type CompletionSink func(ctx context.Context, delivery CompletionDelivery) error
