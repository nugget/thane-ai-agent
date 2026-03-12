package loop

import (
	"context"
	"testing"
)

func TestIterationSummary_NilOnPlainContext(t *testing.T) {
	t.Parallel()
	if got := IterationSummary(context.Background()); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestIterationSummary_ReturnsInjectedMap(t *testing.T) {
	t.Parallel()
	m := map[string]any{"foo": 42}
	ctx := context.WithValue(context.Background(), iterSummaryKey{}, m)

	got := IterationSummary(ctx)
	if got == nil {
		t.Fatal("expected non-nil map")
	}
	if got["foo"] != 42 {
		t.Fatalf("expected foo=42, got %v", got["foo"])
	}
}
