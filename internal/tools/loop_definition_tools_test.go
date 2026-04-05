package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/router"
)

type testLoopDefinitionDeps struct {
	reg              *Registry
	defs             *looppkg.DefinitionRegistry
	persisted        map[string]looppkg.Spec
	persistedUpdated map[string]time.Time
	deleted          []string
	persistedPolicy  map[string]looppkg.DefinitionPolicy
	deletedPolicy    []string
	reconciled       []string
}

func newTestLoopDefinitionDeps(t *testing.T) *testLoopDefinitionDeps {
	t.Helper()

	defs, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:       "metacog_like",
			Enabled:    true,
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

	reg := NewEmptyRegistry()
	deps := &testLoopDefinitionDeps{
		reg:              reg,
		defs:             defs,
		persisted:        make(map[string]looppkg.Spec),
		persistedUpdated: make(map[string]time.Time),
		persistedPolicy:  make(map[string]looppkg.DefinitionPolicy),
	}
	reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry: defs,
		View: func() *looppkg.DefinitionRegistryView {
			return looppkg.BuildDefinitionRegistryView(defs.Snapshot(), map[string]looppkg.DefinitionRuntimeStatus{
				"metacog_like": {
					Running: true,
					LoopID:  "loop-live-1",
					State:   looppkg.StateSleeping,
				},
			})
		},
		PersistSpec: func(spec looppkg.Spec, updatedAt time.Time) error {
			deps.persisted[spec.Name] = spec
			deps.persistedUpdated[spec.Name] = updatedAt
			return nil
		},
		DeleteSpec: func(name string) error {
			deps.deleted = append(deps.deleted, name)
			delete(deps.persisted, name)
			delete(deps.persistedUpdated, name)
			return nil
		},
		PersistPolicy: func(name string, policy looppkg.DefinitionPolicy) error {
			deps.persistedPolicy[name] = policy
			return nil
		},
		DeletePolicy: func(name string) error {
			deps.deletedPolicy = append(deps.deletedPolicy, name)
			delete(deps.persistedPolicy, name)
			return nil
		},
		Reconcile: func(_ context.Context, name string) error {
			deps.reconciled = append(deps.reconciled, name)
			return nil
		},
		LaunchDefinition: func(_ context.Context, name string, launch looppkg.Launch) (looppkg.LaunchResult, error) {
			spec, ok := deps.defs.Get(name)
			if !ok {
				return looppkg.LaunchResult{}, &looppkg.UnknownDefinitionError{Name: name}
			}
			return looppkg.LaunchResult{
				LoopID:    "loop-123",
				Operation: spec.Operation,
				Detached:  spec.Operation != looppkg.OperationRequestReply,
			}, nil
		},
	})
	return deps
}

func TestConfigureLoopDefinitionTools_RegistersTools(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	for _, name := range []string{
		"loop_definition_summary",
		"loop_definition_list",
		"loop_definition_get",
		"loop_definition_set",
		"loop_definition_delete",
		"loop_definition_set_policy",
		"loop_definition_launch",
	} {
		if deps.reg.Get(name) == nil {
			t.Fatalf("%s tool not registered", name)
		}
	}
	if deps.reg.loopDefinitionRegistry != deps.defs {
		t.Fatal("loop definition registry dependency was not stored")
	}
}

func TestLoopDefinitionSetAndDelete(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	out, err := deps.reg.Get("loop_definition_set").Handler(context.Background(), map[string]any{
		"spec": map[string]any{
			"name":       "room_monitor",
			"task":       "Monitor the office and report durable changes.",
			"operation":  "service",
			"completion": "conversation",
			"profile": map[string]any{
				"mission":      "background",
				"initial_tags": []any{"homeassistant"},
			},
			"sleep_min":     "5m",
			"sleep_max":     "30m",
			"sleep_default": "10m",
		},
	})
	if err != nil {
		t.Fatalf("loop_definition_set: %v", err)
	}

	var setResp struct {
		Status     string                 `json:"status"`
		Generation int64                  `json:"generation"`
		Definition looppkg.DefinitionView `json:"definition"`
	}
	if err := json.Unmarshal([]byte(out), &setResp); err != nil {
		t.Fatalf("unmarshal set response: %v", err)
	}
	if setResp.Status != "ok" {
		t.Fatalf("status = %q, want ok", setResp.Status)
	}
	if setResp.Definition.Source != looppkg.DefinitionSourceOverlay {
		t.Fatalf("source = %q, want overlay", setResp.Definition.Source)
	}
	if _, ok := deps.persisted["room_monitor"]; !ok {
		t.Fatal("persist callback was not invoked")
	}

	deleteOut, err := deps.reg.Get("loop_definition_delete").Handler(context.Background(), map[string]any{
		"name": "room_monitor",
	})
	if err != nil {
		t.Fatalf("loop_definition_delete: %v", err)
	}
	var deleteResp struct {
		Status     string `json:"status"`
		Generation int64  `json:"generation"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal([]byte(deleteOut), &deleteResp); err != nil {
		t.Fatalf("unmarshal delete response: %v", err)
	}
	if deleteResp.Name != "room_monitor" {
		t.Fatalf("name = %q, want room_monitor", deleteResp.Name)
	}
	if len(deps.deleted) != 1 || deps.deleted[0] != "room_monitor" {
		t.Fatalf("deleted = %v, want [room_monitor]", deps.deleted)
	}
	if len(deps.reconciled) != 2 || deps.reconciled[0] != "room_monitor" || deps.reconciled[1] != "room_monitor" {
		t.Fatalf("reconciled = %v, want [room_monitor room_monitor]", deps.reconciled)
	}
}

func TestLoopDefinitionListFiltersOverlay(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	if err := deps.defs.Upsert(looppkg.Spec{
		Name:       "room_monitor",
		Task:       "Monitor the office.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionConversation,
		Profile: router.LoopProfile{
			Mission: "background",
		},
	}, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	out, err := deps.reg.Get("loop_definition_list").Handler(context.Background(), map[string]any{
		"source": "overlay",
	})
	if err != nil {
		t.Fatalf("loop_definition_list: %v", err)
	}

	var got struct {
		Count int                      `json:"count"`
		Items []looppkg.DefinitionView `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if got.Count != 1 {
		t.Fatalf("count = %d, want 1", got.Count)
	}
	if got.Items[0].Name != "room_monitor" {
		t.Fatalf("item name = %q, want room_monitor", got.Items[0].Name)
	}
}

func TestLoopDefinitionSetPolicyAndLaunch(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	out, err := deps.reg.Get("loop_definition_set_policy").Handler(context.Background(), map[string]any{
		"name":   "metacog_like",
		"state":  "inactive",
		"reason": "quiet hours",
	})
	if err != nil {
		t.Fatalf("loop_definition_set_policy: %v", err)
	}

	var policyResp struct {
		Status     string                 `json:"status"`
		Generation int64                  `json:"generation"`
		Definition looppkg.DefinitionView `json:"definition"`
	}
	if err := json.Unmarshal([]byte(out), &policyResp); err != nil {
		t.Fatalf("unmarshal policy response: %v", err)
	}
	if policyResp.Definition.PolicyState != looppkg.DefinitionPolicyStateInactive || policyResp.Definition.PolicySource != looppkg.DefinitionPolicySourceOverlay {
		t.Fatalf("policy = %q/%q, want inactive/overlay", policyResp.Definition.PolicyState, policyResp.Definition.PolicySource)
	}
	if deps.persistedPolicy["metacog_like"].Reason != "quiet hours" {
		t.Fatalf("persisted policy = %+v, want quiet hours", deps.persistedPolicy["metacog_like"])
	}
	if policyResp.Definition.Runtime.LoopID != "loop-live-1" {
		t.Fatalf("runtime loop_id = %q, want loop-live-1", policyResp.Definition.Runtime.LoopID)
	}

	launchOut, err := deps.reg.Get("loop_definition_launch").Handler(context.Background(), map[string]any{
		"name": "metacog_like",
	})
	if err != nil {
		t.Fatalf("loop_definition_launch: %v", err)
	}
	var launchResp struct {
		Status string               `json:"status"`
		Result looppkg.LaunchResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(launchOut), &launchResp); err != nil {
		t.Fatalf("unmarshal launch response: %v", err)
	}
	if launchResp.Result.LoopID != "loop-123" {
		t.Fatalf("launch loop_id = %q, want loop-123", launchResp.Result.LoopID)
	}
}

func TestLoopDefinitionListFiltersRuntimeState(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	out, err := deps.reg.Get("loop_definition_list").Handler(context.Background(), map[string]any{
		"runtime_state": "sleeping",
	})
	if err != nil {
		t.Fatalf("loop_definition_list(runtime_state): %v", err)
	}

	var got struct {
		Count int                      `json:"count"`
		Items []looppkg.DefinitionView `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if got.Count != 1 || got.Items[0].Name != "metacog_like" {
		t.Fatalf("items = %+v, want one sleeping metacog_like", got.Items)
	}
}

func TestLoopDefinitionListFiltersEligibility(t *testing.T) {
	deps := newTestLoopDefinitionDeps(t)

	deps.reg.loopDefinitionView = func() *looppkg.DefinitionRegistryView {
		return &looppkg.DefinitionRegistryView{
			Generation: 1,
			Definitions: []looppkg.DefinitionView{
				{
					DefinitionSnapshot: looppkg.DefinitionSnapshot{
						Name: "eligible_watch",
						Spec: looppkg.Spec{
							Name:      "eligible_watch",
							Task:      "watch",
							Operation: looppkg.OperationService,
						},
					},
					Eligibility: looppkg.DefinitionEligibilityStatus{Eligible: true},
				},
				{
					DefinitionSnapshot: looppkg.DefinitionSnapshot{
						Name: "ineligible_watch",
						Spec: looppkg.Spec{
							Name:      "ineligible_watch",
							Task:      "watch later",
							Operation: looppkg.OperationService,
						},
					},
					Eligibility: looppkg.DefinitionEligibilityStatus{
						Eligible: false,
						Reason:   "outside scheduled windows",
					},
				},
			},
		}
	}

	out, err := deps.reg.Get("loop_definition_list").Handler(context.Background(), map[string]any{
		"eligible": "false",
	})
	if err != nil {
		t.Fatalf("loop_definition_list(eligible): %v", err)
	}

	var got struct {
		Count int                      `json:"count"`
		Items []looppkg.DefinitionView `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if got.Count != 1 || got.Items[0].Name != "ineligible_watch" {
		t.Fatalf("items = %+v, want one ineligible_watch definition", got.Items)
	}
}
