package app

import (
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestLoopDefinitionPolicyStoreSaveAndLoadIntoRegistry(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	store := newLoopDefinitionPolicyStore(op)
	registry := testLoopDefinitionRegistry(t)

	updatedAt := time.Date(2026, 4, 5, 7, 0, 0, 0, time.UTC)
	if err := store.Save("metacog_like", looppkg.DefinitionPolicy{
		State:     looppkg.DefinitionPolicyStateInactive,
		Reason:    "quiet hours",
		UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	snap := registry.Snapshot()
	def, found := findLoopDefinitionByName(snap, "metacog_like")
	if !found {
		t.Fatal("metacog_like missing from snapshot")
	}
	if def.PolicyState != looppkg.DefinitionPolicyStateInactive || def.PolicySource != looppkg.DefinitionPolicySourceOverlay {
		t.Fatalf("policy = %q/%q, want inactive/overlay", def.PolicyState, def.PolicySource)
	}
	if def.PolicyReason != "quiet hours" {
		t.Fatalf("PolicyReason = %q, want quiet hours", def.PolicyReason)
	}
	if !def.PolicyUpdatedAt.Equal(updatedAt) {
		t.Fatalf("PolicyUpdatedAt = %v, want %v", def.PolicyUpdatedAt, updatedAt)
	}
}
