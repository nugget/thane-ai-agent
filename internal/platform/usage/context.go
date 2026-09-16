package usage

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

type observerKey struct{}

// ApplyAttribution fills unknown record identifiers from attribution, retaining
// identifiers explicitly supplied by the call. It does not price or persist.
func ApplyAttribution(record Record, attribution llm.Attribution) Record {
	if record.RequestID == "" {
		record.RequestID = attribution.RequestID
	}
	if record.SessionID == "" {
		record.SessionID = attribution.SessionID
	}
	if record.ConversationID == "" {
		record.ConversationID = attribution.ConversationID
	}
	if record.LoopID == "" {
		record.LoopID = attribution.LoopID
	}
	if record.LoopName == "" {
		record.LoopName = attribution.LoopName
	}
	if record.ParentLoopID == "" {
		record.ParentLoopID = attribution.ParentLoopID
	}
	return record
}

// WithObserver attaches a request-scoped usage observer to ctx. Observe calls
// it synchronously for each model-call record, including usage reported
// with an error. The observer must be concurrency-safe and return promptly;
// it must not start model work or rely on ctx still being uncanceled.
// An existing observer is replaced. A nil observer disables observation.
func WithObserver(ctx context.Context, observer func(Record)) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

// Observe publishes a model-call record to the observer attached by
// WithObserver. It is a no-op without an observer. Cancellation does not suppress
// publication of usage already incurred, and it performs no detached work.
func Observe(ctx context.Context, record Record) {
	if observer, ok := ctx.Value(observerKey{}).(func(Record)); ok && observer != nil {
		observer(record)
	}
}
