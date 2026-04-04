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
}

func newTestLoopDefinitionDeps(t *testing.T) *testLoopDefinitionDeps {
	t.Helper()

	defs, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
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

	reg := NewEmptyRegistry()
	deps := &testLoopDefinitionDeps{
		reg:              reg,
		defs:             defs,
		persisted:        make(map[string]looppkg.Spec),
		persistedUpdated: make(map[string]time.Time),
	}
	reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry: defs,
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
		Status     string                     `json:"status"`
		Generation int64                      `json:"generation"`
		Definition looppkg.DefinitionSnapshot `json:"definition"`
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
		Count int                          `json:"count"`
		Items []looppkg.DefinitionSnapshot `json:"items"`
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
