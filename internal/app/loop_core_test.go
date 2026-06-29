package app

import (
	"context"
	"log/slog"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestEnsureChannelsContainer verifies the channels grouping container is
// spawned as an inert container under core and that the call is idempotent.
func TestEnsureChannelsContainer(t *testing.T) {
	t.Parallel()

	a := &App{loopRegistry: looppkg.NewRegistry(), logger: slog.Default()}
	ctx := context.Background()

	if err := a.ensureChannelsContainer(ctx); err != nil {
		t.Fatalf("ensureChannelsContainer: %v", err)
	}
	c := a.loopRegistry.GetByName(looppkg.ChannelsContainerName)
	if c == nil {
		t.Fatal("channels container not created")
	}
	if got := c.Status().Config.Operation; got != looppkg.OperationContainer {
		t.Errorf("channels Operation = %q, want %q", got, looppkg.OperationContainer)
	}

	// Idempotent: a second call is a no-op, not a duplicate.
	if err := a.ensureChannelsContainer(ctx); err != nil {
		t.Fatalf("second ensureChannelsContainer: %v", err)
	}
	if got := len(a.loopRegistry.FindByName(looppkg.ChannelsContainerName)); got != 1 {
		t.Errorf("channels container count = %d, want 1", got)
	}
}
