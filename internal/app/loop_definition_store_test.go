package app

import (
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func testLoopDefinitionRegistry(t *testing.T) *looppkg.DefinitionRegistry {
	t.Helper()

	reg, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:       "metacog_like",
			Task:       "Observe and reflect.",
			Operation:  looppkg.OperationService,
			Completion: looppkg.CompletionNone,
			Profile: router.LoopProfile{
				Mission: "background",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	return reg
}

func TestLoopDefinitionStoreSaveAndLoadIntoRegistry(t *testing.T) {
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
	store := newLoopDefinitionStore(op)
	registry := testLoopDefinitionRegistry(t)

	updatedAt := time.Date(2026, 4, 4, 18, 0, 0, 0, time.UTC)
	if err := store.Save(looppkg.Spec{
		Name:       "room_monitor",
		Task:       "Monitor the front room and surface noteworthy changes.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionConversation,
		Tags:       []string{"homeassistant"},
		Profile: router.LoopProfile{
			Mission:          "background",
			Instructions:     "Be concise.",
			DelegationGating: "disabled",
		},
	}, updatedAt); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	snap := registry.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	if snap.OverlayDefinitions != 1 {
		t.Fatalf("OverlayDefinitions = %d, want 1", snap.OverlayDefinitions)
	}
	found := false
	for _, def := range snap.Definitions {
		if def.Name != "room_monitor" {
			continue
		}
		found = true
		if def.Source != looppkg.DefinitionSourceOverlay {
			t.Fatalf("Source = %q, want overlay", def.Source)
		}
		if !def.UpdatedAt.Equal(updatedAt) {
			t.Fatalf("UpdatedAt = %v, want %v", def.UpdatedAt, updatedAt)
		}
		if len(def.Spec.Tags) != 1 || def.Spec.Tags[0] != "homeassistant" {
			t.Fatalf("Spec.Tags = %v, want [homeassistant]", def.Spec.Tags)
		}
	}
	if !found {
		t.Fatal("persisted loop definition did not load into registry")
	}
}

func TestLoopDefinitionStoreLoadIntoSkipsInvalidEntries(t *testing.T) {
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
	if err := op.Set(loopDefinitionRegistryNamespace, "room_monitor", "{not-json"); err != nil {
		t.Fatalf("op.Set: %v", err)
	}

	store := newLoopDefinitionStore(op)
	registry := testLoopDefinitionRegistry(t)
	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	snap := registry.Snapshot()
	if snap.OverlayDefinitions != 0 {
		t.Fatalf("OverlayDefinitions = %d, want 0", snap.OverlayDefinitions)
	}
}
