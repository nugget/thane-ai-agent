package app

import (
	"encoding/json"
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

func TestLoopDefinitionStoreLoadIntoSkipsUnpersistableSpec(t *testing.T) {
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

	// One healthy overlay definition.
	if err := store.Save(looppkg.Spec{
		Name:       "good_loop",
		Task:       "Maintain the dashboard.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{{
			Name: "dash",
			Type: looppkg.OutputTypeMaintainedDocument,
			Mode: looppkg.OutputModeReplace,
			Ref:  "core:good.md",
		}},
	}, time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Save good_loop: %v", err)
	}

	// One already-persisted record corrupted exactly as #1068 left prod:
	// the output ref holds the document body instead of a ref. It is valid
	// JSON (so it decodes), but ValidatePersistable rejects it. Written
	// straight through op.Set to bypass Save's validation, mirroring a row
	// persisted before the grammar check existed.
	corrupt, err := json.Marshal(looppkg.DefinitionRecord{
		Spec: looppkg.Spec{
			Name:       "bad_loop",
			Task:       "Maintain.",
			Operation:  looppkg.OperationService,
			Completion: looppkg.CompletionNone,
			Outputs: []looppkg.OutputSpec{{
				Name: "doc",
				Type: looppkg.OutputTypeMaintainedDocument,
				Mode: looppkg.OutputModeReplace,
				Ref:  "---\ntitle: \"corrupt\"\n---\n\nbody",
			}},
		},
		UpdatedAt: time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Marshal corrupt record: %v", err)
	}
	if err := op.Set(loopDefinitionRegistryNamespace, "bad_loop", string(corrupt)); err != nil {
		t.Fatalf("op.Set: %v", err)
	}

	// LoadInto must NOT fail the whole batch on the one bad record:
	// ReplaceOverlay is all-or-nothing and this error is fatal at startup
	// (new_stores.go), so without the per-record skip a single corrupt
	// definition would block every healthy overlay loop from loading.
	registry := testLoopDefinitionRegistry(t)
	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto returned error (one bad record bricked the batch): %v", err)
	}

	snap := registry.Snapshot()
	if snap.OverlayDefinitions != 1 {
		t.Fatalf("OverlayDefinitions = %d, want 1 (healthy loads, corrupt skipped)", snap.OverlayDefinitions)
	}
	var overlayNames []string
	for _, def := range snap.Definitions {
		if def.Source == looppkg.DefinitionSourceOverlay {
			overlayNames = append(overlayNames, def.Name)
		}
	}
	if len(overlayNames) != 1 || overlayNames[0] != "good_loop" {
		t.Fatalf("overlay definitions = %v, want [good_loop]", overlayNames)
	}
}
