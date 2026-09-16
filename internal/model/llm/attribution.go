package llm

import "context"

type attributionKey struct{}

// Attribution identifies the request and loop responsible for model work.
// Empty fields mean that the caller does not know that identity; identifiers
// are never inferred from conversation names or shortened for display.
type Attribution struct {
	RequestID      string `json:"request_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	LoopID         string `json:"loop_id,omitempty"`
	LoopName       string `json:"loop_name,omitempty"`
	ParentLoopID   string `json:"parent_loop_id,omitempty"`
}

// WithAttribution attaches a snapshot of attribution to ctx, replacing any
// prior snapshot. Derive from [AttributionFromContext] to retain parent values
// when only the session or conversation changes. It does not attach an observer.
func WithAttribution(ctx context.Context, attribution Attribution) context.Context {
	return context.WithValue(ctx, attributionKey{}, attribution)
}

// AttributionFromContext returns the attribution snapshot carried by ctx,
// or an empty snapshot when none is attached.
func AttributionFromContext(ctx context.Context) Attribution {
	attribution, _ := ctx.Value(attributionKey{}).(Attribution)
	return attribution
}
