package loop

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

func TestDefinitionRegistrySnapshotIncludesConfigAndOverlay(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{
		{
			Name:       "metacog_like",
			Task:       "Observe and reflect.",
			Operation:  OperationService,
			Completion: CompletionNone,
			Profile: router.LoopProfile{
				Mission: "background",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	updatedAt := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	if err := reg.Upsert(Spec{
		Name:       "room_monitor",
		Task:       "Watch the office and report meaningful changes.",
		Operation:  OperationService,
		Completion: CompletionConversation,
		Profile: router.LoopProfile{
			Mission:      "background",
			InitialTags:  []string{"homeassistant"},
			ExcludeTools: []string{"shell_exec"},
		},
	}, updatedAt); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	snap := reg.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	if snap.Generation != 2 {
		t.Fatalf("Generation = %d, want 2", snap.Generation)
	}
	if snap.ConfigDefinitions != 1 {
		t.Fatalf("ConfigDefinitions = %d, want 1", snap.ConfigDefinitions)
	}
	if snap.OverlayDefinitions != 1 {
		t.Fatalf("OverlayDefinitions = %d, want 1", snap.OverlayDefinitions)
	}
	if len(snap.Definitions) != 2 {
		t.Fatalf("len(Definitions) = %d, want 2", len(snap.Definitions))
	}
	if snap.Definitions[0].Name != "metacog_like" || snap.Definitions[0].Source != DefinitionSourceConfig {
		t.Fatalf("Definitions[0] = %+v, want config metacog_like", snap.Definitions[0])
	}
	if snap.Definitions[1].Name != "room_monitor" || snap.Definitions[1].Source != DefinitionSourceOverlay {
		t.Fatalf("Definitions[1] = %+v, want overlay room_monitor", snap.Definitions[1])
	}
	if !snap.Definitions[1].UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", snap.Definitions[1].UpdatedAt, updatedAt)
	}
}

func TestDefinitionRegistryRejectsMutatingConfigDefinitions(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{
		{
			Name:      "metacog_like",
			Task:      "Observe and reflect.",
			Operation: OperationService,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	err = reg.Upsert(Spec{
		Name:      "metacog_like",
		Task:      "Override the config definition.",
		Operation: OperationService,
	}, time.Now())
	if err == nil {
		t.Fatal("Upsert error = nil, want immutable definition error")
	}
	if _, ok := err.(*ImmutableDefinitionError); !ok {
		t.Fatalf("Upsert error = %T, want *ImmutableDefinitionError", err)
	}

	err = reg.Delete("metacog_like", time.Now())
	if err == nil {
		t.Fatal("Delete error = nil, want immutable definition error")
	}
	if _, ok := err.(*ImmutableDefinitionError); !ok {
		t.Fatalf("Delete error = %T, want *ImmutableDefinitionError", err)
	}
}

func TestDefinitionRegistryReplaceOverlayRejectsRuntimeHooks(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	err = reg.ReplaceOverlay(map[string]DefinitionRecord{
		"dynamic": {
			Spec: Spec{
				Name: "dynamic",
				Task: "Dynamic task.",
				Setup: func(*Loop) {
				},
			},
		},
	})
	if err == nil {
		t.Fatal("ReplaceOverlay error = nil, want persistable validation error")
	}
}
