package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestThaneLoopCreateCannotEscapeCallerBinding(t *testing.T) {
	t.Parallel()

	boundCtx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "omitted binding is inherited",
			args: map[string]any{
				"name":      "bound_service",
				"intent":    "Watch pull requests.",
				"operation": "service",
				"sleep_min": "5m",
				"sleep_max": "15m",
				"dry_run":   true,
			},
		},
		{
			name: "declared replacement loses to caller",
			args: map[string]any{
				"name":      "bound_container",
				"intent":    "Group forge watchers.",
				"operation": "container",
				"bindings": map[string]any{
					looppkg.BindingForgeAccount: "github-primary",
				},
				"dry_run": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newCurateTestRig(t)
			out, err := rig.tool.Handler(boundCtx, tt.args)
			if err != nil {
				t.Fatalf("thane_loop_create: %v", err)
			}
			var result struct {
				Spec looppkg.Spec `json:"spec"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if got := result.Spec.Bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
				t.Errorf("created binding = %q, want caller's boundary", got)
			}
		})
	}
}

func TestThaneLoopCreateRejectsInvalidBindingShape(t *testing.T) {
	t.Parallel()
	rig := newCurateTestRig(t)
	_, err := rig.tool.Handler(context.Background(), map[string]any{
		"name":      "invalid_binding",
		"intent":    "Watch pull requests.",
		"operation": "service",
		"sleep_min": "5m",
		"sleep_max": "15m",
		"bindings": map[string]any{
			looppkg.BindingForgeAccount: 42,
		},
		"dry_run": true,
	})
	if err == nil || !strings.Contains(err.Error(), `bindings["forge_account"] must be a string`) {
		t.Fatalf("err = %v, want the invalid binding named", err)
	}
}

func TestThaneLoopCreateSchemaOffersBindings(t *testing.T) {
	t.Parallel()
	props := schemaProperties(t, thaneLoopCreateSchema())
	bindings, ok := props["bindings"].(map[string]any)
	if !ok {
		t.Fatal("thane_loop_create schema does not offer bindings")
	}
	if got := bindings["type"]; got != "object" {
		t.Fatalf("bindings.type = %v, want object", got)
	}
}

func TestLoopDefinitionSetCannotEscapeCallerBinding(t *testing.T) {
	t.Parallel()
	deps := newTestLoopDefinitionDeps(t)
	ctx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})

	_, err := deps.reg.Get("loop_definition_set").Handler(ctx, map[string]any{
		"spec": map[string]any{
			"name":      "authored_child",
			"task":      "Watch pull requests.",
			"operation": "service",
			"bindings": map[string]any{
				looppkg.BindingForgeAccount: "github-primary",
			},
		},
	})
	if err != nil {
		t.Fatalf("loop_definition_set: %v", err)
	}
	if got := deps.persisted["authored_child"].Bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
		t.Errorf("persisted binding = %q, want caller's boundary", got)
	}

	if err := deps.defs.Upsert(looppkg.Spec{
		Name:      "primary_container",
		Enabled:   true,
		Operation: looppkg.OperationContainer,
		Bindings: map[string]string{
			looppkg.BindingForgeAccount: "github-primary",
		},
	}, time.Now()); err != nil {
		t.Fatalf("Upsert primary_container: %v", err)
	}
	_, err = deps.reg.Get("loop_definition_set").Handler(ctx, map[string]any{
		"spec": map[string]any{
			"name":        "nested_escape",
			"task":        "Watch pull requests.",
			"operation":   "service",
			"parent_name": "primary_container",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "outer container resolves differently") {
		t.Fatalf("err = %v, want incompatible parent refusal", err)
	}
	if _, ok := deps.persisted["nested_escape"]; ok {
		t.Fatal("incompatible nested definition was persisted")
	}
}

func TestStoredDefinitionActionsCannotEscapeCallerBinding(t *testing.T) {
	t.Parallel()
	deps := newTestLoopDefinitionDeps(t)
	for _, spec := range []looppkg.Spec{
		{
			Name:      "readonly_container",
			Enabled:   true,
			Operation: looppkg.OperationContainer,
			Bindings: map[string]string{
				looppkg.BindingForgeAccount: "github-readonly",
			},
		},
		{
			Name:       "matching_child",
			Enabled:    true,
			Task:       "Watch pull requests.",
			Operation:  looppkg.OperationService,
			ParentName: "readonly_container",
		},
		{
			Name:      "unbound_child",
			Enabled:   true,
			Task:      "Watch pull requests.",
			Operation: looppkg.OperationService,
		},
		{
			Name:      "other_account_child",
			Enabled:   true,
			Task:      "Watch pull requests.",
			Operation: looppkg.OperationService,
			Bindings: map[string]string{
				looppkg.BindingForgeAccount: "github-primary",
			},
		},
	} {
		if err := deps.defs.Upsert(spec, time.Now()); err != nil {
			t.Fatalf("Upsert(%s): %v", spec.Name, err)
		}
	}

	boundCtx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})
	if _, err := deps.reg.Get("loop_definition_launch").Handler(boundCtx, map[string]any{
		"name": "matching_child",
	}); err != nil {
		t.Fatalf("matching inherited binding should launch: %v", err)
	}

	for _, name := range []string{"unbound_child", "other_account_child"} {
		t.Run(name, func(t *testing.T) {
			deps.lastLaunch = looppkg.Launch{}
			_, err := deps.reg.Get("loop_definition_launch").Handler(boundCtx, map[string]any{"name": name})
			if err == nil || !strings.Contains(err.Error(), "must resolve binding forge_account=\"github-readonly\"") {
				t.Fatalf("err = %v, want incompatible binding refusal", err)
			}
			if deps.lastLaunch.Task != "" || len(deps.lastLaunch.Metadata) > 0 {
				t.Fatalf("lastLaunch = %#v, incompatible definition was dispatched", deps.lastLaunch)
			}
		})
	}

	if _, err := deps.reg.Get("loop_definition_update").Handler(boundCtx, map[string]any{
		"name": "unbound_child",
		"task": "Use whichever account is available.",
	}); err == nil {
		t.Fatal("bound caller updated an incompatible stored definition")
	}
	if _, err := deps.reg.Get("loop_definition_set_policy").Handler(boundCtx, map[string]any{
		"name":  "unbound_child",
		"state": "active",
	}); err == nil {
		t.Fatal("bound caller activated an incompatible stored definition")
	}
	if _, err := deps.reg.Get("loop_definition_set_policy").Handler(boundCtx, map[string]any{
		"name":  "unbound_child",
		"state": "paused",
	}); err != nil {
		t.Fatalf("bound caller should still be able to stop an incompatible definition: %v", err)
	}
}
