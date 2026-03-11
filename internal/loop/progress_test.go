package loop

import (
	"context"
	"testing"
)

func TestProgressFunc_NilOnPlainContext(t *testing.T) {
	t.Parallel()
	if got := ProgressFunc(context.Background()); got != nil {
		t.Fatal("expected nil, got non-nil function")
	}
}

func TestProgressFunc_ReturnsInjectedFunc(t *testing.T) {
	t.Parallel()
	var called bool
	fn := func(kind string, data map[string]any) {
		called = true
	}
	ctx := context.WithValue(context.Background(), progressFuncKey{}, fn)

	got := ProgressFunc(ctx)
	if got == nil {
		t.Fatal("expected non-nil function")
	}
	got("test_event", nil)
	if !called {
		t.Fatal("injected function was not called")
	}
}
