package usage

import "context"

type observerKey struct{}

// WithObserver attaches a request-scoped usage observer to ctx. Observe calls
// it synchronously for each priced model-call record, including usage reported
// with an error. The observer must be concurrency-safe and return promptly;
// it must not start model work or rely on ctx still being uncanceled.
// An existing observer is replaced. A nil observer disables observation.
func WithObserver(ctx context.Context, observer func(Record)) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

// Observe publishes a priced model-call record to the observer attached by
// WithObserver. It is a no-op without an observer. Cancellation does not suppress
// publication of usage already incurred, and it performs no detached work.
func Observe(ctx context.Context, record Record) {
	if observer, ok := ctx.Value(observerKey{}).(func(Record)); ok && observer != nil {
		observer(record)
	}
}
