package loop

import "context"

// CompletionDelivery is the normalized completion payload emitted by
// detached loops when they deliver a result through a non-return path.
type CompletionDelivery struct {
	Mode           Completion `json:"mode"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Content        string     `json:"content"`
	LoopID         string     `json:"loop_id"`
	LoopName       string     `json:"loop_name"`
	Response       *Response  `json:"response,omitempty"`
	Status         *Status    `json:"status,omitempty"`
}

// CompletionSink receives detached loop completion deliveries. The app
// layer wires concrete sinks (e.g. conversation injection) so the loop
// package can stay free of channel and memory dependencies.
type CompletionSink func(ctx context.Context, delivery CompletionDelivery) error
