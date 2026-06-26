package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

type reparentHarness struct {
	tool           *Tool
	defReg         *looppkg.DefinitionRegistry
	live           *looppkg.Registry
	reconcileCalls *[]string
}

// newReparentHarness builds a fresh graph each call so the mutating happy
// path can't bleed into the validation cases: live containers travel +
// temporal, a service loop trip parented under travel, and a standalone
// service loop trip2. Overlay definitions back each one so the reparent
// handler's FindDefinition/commit path is exercised end to end.
func newReparentHarness(t *testing.T) reparentHarness {
	t.Helper()
	runner := noopLoopRunner{}
	live := looppkg.NewRegistry()

	mk := func(name string, op looppkg.Operation, parentID string) *looppkg.Loop {
		cfg := looppkg.Config{Name: name, Operation: op, ParentID: parentID}
		if op != looppkg.OperationContainer {
			cfg.Task = "do " + name // executing loops require a task; containers never run one
		}
		l, err := looppkg.New(cfg, looppkg.Deps{Runner: runner})
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if err := live.Register(l); err != nil {
			t.Fatalf("Register(%s): %v", name, err)
		}
		return l
	}
	travel := mk("travel", looppkg.OperationContainer, "")
	mk("temporal", looppkg.OperationContainer, "")
	mk("trip", looppkg.OperationService, travel.ID()) // child of travel
	mk("trip2", looppkg.OperationService, "")

	defReg, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	now := time.Now().UTC()
	for _, spec := range []looppkg.Spec{
		{Name: "travel", Operation: looppkg.OperationContainer},
		{Name: "temporal", Operation: looppkg.OperationContainer},
		{Name: "trip", Operation: looppkg.OperationService, Task: "do trip"},
		{Name: "trip2", Operation: looppkg.OperationService, Task: "do trip2"},
	} {
		if err := defReg.Upsert(spec, now); err != nil {
			t.Fatalf("Upsert(%s): %v", spec.Name, err)
		}
	}

	reconcileCalls := &[]string{}
	reg := NewEmptyRegistry()
	reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry:   defReg,
		CommitSpec: upsertCommitSpec(defReg),
		Reconcile: func(_ context.Context, name string) error {
			*reconcileCalls = append(*reconcileCalls, name)
			return nil
		},
	})
	reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		Registry:   defReg,
		CommitSpec: upsertCommitSpec(defReg),
		LaunchDefinition: func(_ context.Context, _ string, _ looppkg.Launch) (looppkg.LaunchResult, error) {
			return looppkg.LaunchResult{}, nil
		},
		LiveRegistry: live,
	})

	tool := reg.Get("loop_reparent")
	if tool == nil {
		t.Fatal("loop_reparent tool not registered")
	}
	return reparentHarness{tool: tool, defReg: defReg, live: live, reconcileCalls: reconcileCalls}
}

func (h reparentHarness) defParentName(t *testing.T, name string) string {
	t.Helper()
	def, ok := looppkg.FindDefinition(h.defReg.Snapshot(), name)
	if !ok {
		t.Fatalf("definition %q not found", name)
	}
	return def.Spec.ParentName
}

func TestLoopReparent_HappyPath(t *testing.T) {
	h := newReparentHarness(t)

	out, err := h.tool.Handler(context.Background(), map[string]any{"name": "trip", "parent_name": "travel"})
	if err != nil {
		t.Fatalf("reparent: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res["status"] != "ok" || res["parent_name"] != "travel" {
		t.Fatalf("result status/parent_name = %v/%v, want ok/travel: %s", res["status"], res["parent_name"], out)
	}
	// The durable structural fix: the persisted spec now names the new parent.
	if got := h.defParentName(t, "trip"); got != "travel" {
		t.Errorf("persisted parent_name = %q, want travel", got)
	}
	// The relaunch: the running loop was stopped (deregistered) and reconcile
	// was asked to bring it back under the new parent.
	if h.live.GetByName("trip") != nil {
		t.Error("trip should have been stopped for relaunch")
	}
	if len(*h.reconcileCalls) == 0 || (*h.reconcileCalls)[len(*h.reconcileCalls)-1] != "trip" {
		t.Errorf("reconcile calls = %v, want trip reconciled", *h.reconcileCalls)
	}
}

func TestLoopReparent_Validations(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"unknown loop", map[string]any{"name": "ghost", "parent_name": "travel"}, "ghost"},
		{"reparent to self", map[string]any{"name": "trip", "parent_name": "trip"}, "itself"},
		{"target not a container", map[string]any{"name": "trip", "parent_name": "trip2"}, "not a container"},
		{"move container with live children", map[string]any{"name": "travel", "parent_name": "temporal"}, "child loop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReparentHarness(t)
			_, err := h.tool.Handler(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
			// A rejected reparent must not have mutated the persisted parent.
			if name, _ := tc.args["name"].(string); name == "trip" {
				if got := h.defParentName(t, "trip"); got != "" {
					t.Errorf("parent_name = %q after rejected reparent, want unchanged empty", got)
				}
			}
		})
	}
}

func TestLoopReparent_NoopWhenAlreadyParented(t *testing.T) {
	h := newReparentHarness(t)
	// Move trip under travel, then ask again — second call is a no-op.
	if _, err := h.tool.Handler(context.Background(), map[string]any{"name": "trip", "parent_name": "travel"}); err != nil {
		t.Fatalf("first reparent: %v", err)
	}
	out, err := h.tool.Handler(context.Background(), map[string]any{"name": "trip", "parent_name": "travel"})
	if err != nil {
		t.Fatalf("second reparent: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res["status"] != "noop" {
		t.Errorf("second reparent should be a noop, got %s", out)
	}
}
