package app

import (
	"context"
	"errors"
	"testing"
)

func TestModelResourceRefreshCallbacks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var reasons []string
	refresh := func(_ context.Context, reason string) {
		reasons = append(reasons, reason)
	}

	onReady, onDown := modelResourceRefreshCallbacks(ctx, "pocket", refresh)
	onReady()
	onDown(errors.New("boom"))

	if len(reasons) != 2 {
		t.Fatalf("expected 2 refresh reasons, got %d", len(reasons))
	}
	if reasons[0] != "resource_ready:pocket" {
		t.Fatalf("unexpected ready reason: %q", reasons[0])
	}
	if reasons[1] != "resource_down:pocket" {
		t.Fatalf("unexpected down reason: %q", reasons[1])
	}
}
