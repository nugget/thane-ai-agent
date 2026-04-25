package app

import (
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func TestModelResourcePolicyStoreSaveAndLoadIntoRegistry(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	store := newModelResourcePolicyStore(op)
	registry := testModelPolicyRegistry(t)

	updatedAt := time.Date(2026, 4, 4, 4, 0, 0, 0, time.UTC)
	if err := store.Save("deepslate", fleet.ResourcePolicy{
		State:     fleet.DeploymentPolicyStateInactive,
		Reason:    "office hours",
		UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	snap := registry.Snapshot()
	found := false
	for _, res := range snap.Resources {
		if res.ID != "deepslate" {
			continue
		}
		found = true
		if res.PolicyState != fleet.DeploymentPolicyStateInactive {
			t.Fatalf("PolicyState = %q, want %q", res.PolicyState, fleet.DeploymentPolicyStateInactive)
		}
		if res.PolicySource != fleet.DeploymentPolicySourceOverlay {
			t.Fatalf("PolicySource = %q, want %q", res.PolicySource, fleet.DeploymentPolicySourceOverlay)
		}
		if res.PolicyReason != "office hours" {
			t.Fatalf("PolicyReason = %q, want %q", res.PolicyReason, "office hours")
		}
	}
	if !found {
		t.Fatal("persisted resource policy did not load into registry")
	}
}

func TestModelResourcePolicyStoreLoadIntoSkipsInvalidEntries(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	if err := op.Set(modelRegistryResourcePolicyNamespace, "deepslate", "{not-json"); err != nil {
		t.Fatalf("op.Set: %v", err)
	}

	store := newModelResourcePolicyStore(op)
	registry := testModelPolicyRegistry(t)
	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	snap := registry.Snapshot()
	for _, res := range snap.Resources {
		if res.ID == "deepslate" && res.PolicySource != fleet.DeploymentPolicySourceDefault {
			t.Fatalf("PolicySource = %q, want default when persisted entry is invalid", res.PolicySource)
		}
	}
}
