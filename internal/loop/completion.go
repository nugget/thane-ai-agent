package loop

import "context"

// CompletionDelivery is the normalized completion payload emitted by
// detached loops when they deliver a result through a non-return path.
type CompletionDelivery struct {
	Mode           Completion `yaml:"mode,omitempty" json:"mode"`
	ConversationID string     `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	Content        string     `yaml:"content,omitempty" json:"content"`
	LoopID         string     `yaml:"loop_id,omitempty" json:"loop_id"`
	LoopName       string     `yaml:"loop_name,omitempty" json:"loop_name"`
	Response       *Response  `yaml:"response,omitempty" json:"response,omitempty"`
	Status         *Status    `yaml:"status,omitempty" json:"status,omitempty"`
}

// CompletionSink receives detached loop completion deliveries. The app
// layer wires concrete sinks (e.g. conversation injection) so the loop
// package can stay free of channel and memory dependencies.
type CompletionSink func(ctx context.Context, delivery CompletionDelivery) error
