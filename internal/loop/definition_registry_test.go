package loop

import (
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

func TestDefinitionRegistrySnapshotIncludesConfigAndOverlay(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{
		{
			Name:       "metacog_like",
			Enabled:    true,
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
	if snap.Definitions[0].PolicyState != DefinitionPolicyStateActive || snap.Definitions[0].PolicySource != DefinitionPolicySourceDefault {
		t.Fatalf("Definitions[0] policy = %q/%q, want active/default", snap.Definitions[0].PolicyState, snap.Definitions[0].PolicySource)
	}
	if snap.Definitions[1].Name != "room_monitor" || snap.Definitions[1].Source != DefinitionSourceOverlay {
		t.Fatalf("Definitions[1] = %+v, want overlay room_monitor", snap.Definitions[1])
	}
	if snap.Definitions[1].PolicyState != DefinitionPolicyStateInactive || snap.Definitions[1].PolicySource != DefinitionPolicySourceDefault {
		t.Fatalf("Definitions[1] policy = %q/%q, want inactive/default", snap.Definitions[1].PolicyState, snap.Definitions[1].PolicySource)
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
			Enabled:   true,
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

func TestDefinitionRegistryPolicyOverlay(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{
		{
			Name:       "night_watch",
			Enabled:    false,
			Task:       "Observe quietly.",
			Operation:  OperationService,
			Completion: CompletionNone,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	updatedAt := time.Date(2026, 4, 5, 1, 2, 3, 0, time.UTC)
	if err := reg.ApplyPolicy("night_watch", DefinitionPolicy{
		State:     DefinitionPolicyStateActive,
		Reason:    "after hours",
		UpdatedAt: updatedAt,
	}, updatedAt); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}

	snap := reg.Snapshot()
	if snap == nil || len(snap.Definitions) != 1 {
		t.Fatalf("snapshot = %+v, want one definition", snap)
	}
	got := snap.Definitions[0]
	if got.PolicyState != DefinitionPolicyStateActive || got.PolicySource != DefinitionPolicySourceOverlay {
		t.Fatalf("policy = %q/%q, want active/overlay", got.PolicyState, got.PolicySource)
	}
	if got.PolicyReason != "after hours" {
		t.Fatalf("PolicyReason = %q, want after hours", got.PolicyReason)
	}
	if !got.PolicyUpdatedAt.Equal(updatedAt) {
		t.Fatalf("PolicyUpdatedAt = %v, want %v", got.PolicyUpdatedAt, updatedAt)
	}

	if err := reg.ClearPolicy("night_watch", updatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("ClearPolicy: %v", err)
	}
	snap = reg.Snapshot()
	got = snap.Definitions[0]
	if got.PolicyState != DefinitionPolicyStateInactive || got.PolicySource != DefinitionPolicySourceDefault {
		t.Fatalf("policy after clear = %q/%q, want inactive/default", got.PolicyState, got.PolicySource)
	}
}

func TestDefinitionRegistryApplyPolicyRejectsUnsupportedState(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{{
		Name:      "night_watch",
		Enabled:   true,
		Task:      "Observe quietly.",
		Operation: OperationService,
	}})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	err = reg.ApplyPolicy("night_watch", DefinitionPolicy{
		State: DefinitionPolicyState("drifting"),
	}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "state must be one of") {
		t.Fatalf("ApplyPolicy error = %v, want unsupported state", err)
	}
}

func TestDefinitionRegistryReplacePoliciesRejectsUnsupportedState(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{{
		Name:      "night_watch",
		Enabled:   true,
		Task:      "Observe quietly.",
		Operation: OperationService,
	}})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	err = reg.ReplacePolicies(map[string]DefinitionPolicy{
		"night_watch": {
			State: DefinitionPolicyState("drifting"),
		},
	}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "state must be one of") {
		t.Fatalf("ReplacePolicies error = %v, want unsupported state", err)
	}
}

func TestBuildDefinitionRegistryViewIncludesRuntimeState(t *testing.T) {
	t.Parallel()

	reg, err := NewDefinitionRegistry([]Spec{
		{
			Name:       "metacog_like",
			Enabled:    true,
			Task:       "Observe and reflect.",
			Operation:  OperationService,
			Completion: CompletionNone,
		},
		{
			Name:       "paused_watch",
			Enabled:    false,
			Task:       "Stay quiet.",
			Operation:  OperationService,
			Completion: CompletionNone,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	view := BuildDefinitionRegistryView(reg.Snapshot(), map[string]DefinitionRuntimeStatus{
		"metacog_like": {
			Running: true,
			LoopID:  "loop-123",
			State:   StateSleeping,
		},
	})
	if view == nil {
		t.Fatal("BuildDefinitionRegistryView returned nil")
	}
	if view.RunningDefinitions != 1 {
		t.Fatalf("RunningDefinitions = %d, want 1", view.RunningDefinitions)
	}
	if view.ByPolicyState[string(DefinitionPolicyStateActive)] != 1 || view.ByPolicyState[string(DefinitionPolicyStateInactive)] != 1 {
		t.Fatalf("ByPolicyState = %+v, want active=1 inactive=1", view.ByPolicyState)
	}
	if view.ByRuntimeState[string(StateSleeping)] != 1 || view.ByRuntimeState[definitionRuntimeStateNotRunning] != 1 {
		t.Fatalf("ByRuntimeState = %+v, want sleeping=1 not_running=1", view.ByRuntimeState)
	}
	if !view.Definitions[0].Runtime.Running {
		t.Fatal("metacog_like runtime should be running")
	}
	if view.Definitions[1].Runtime.Running {
		t.Fatal("paused_watch runtime should not be running")
	}
}
