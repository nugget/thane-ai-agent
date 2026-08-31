package introspection

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestDiagnosticsToolsAreCatalogued mirrors TestLoopToolsAreCatalogued:
// a tool can ship with a working handler yet be invisible to every model
// loop unless it is catalogued AND carries the diagnostics tag that
// gates it into scope.
func TestDiagnosticsToolsAreCatalogued(t *testing.T) {
	provider := NewTools(ToolDeps{})
	if provider.Name() != "introspection.diagnostics" {
		t.Errorf("provider name = %q", provider.Name())
	}
	var problems []string
	for _, tool := range provider.Tools() {
		if tool.Handler == nil {
			problems = append(problems, tool.Name+" — nil handler")
		}
		spec, ok := toolcatalog.LookupBuiltinToolSpec(tool.Name)
		if !ok {
			problems = append(problems, tool.Name+" — no builtin catalog entry")
			continue
		}
		if !slices.Contains(spec.Tags, "diagnostics") {
			problems = append(problems, tool.Name+" — catalogued but missing the \"diagnostics\" tag")
		}
	}
	if len(problems) > 0 {
		t.Errorf("diagnostics tools that will not be offered to a gated model session:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// TestUnwiredDepsReturnErrUnavailable pins the provider contract: the
// tools stay declared while their backing runtime is missing, and only
// invocation fails — with the canonical unavailable error naming the
// gap, never a generic failure.
func TestUnwiredDepsReturnErrUnavailable(t *testing.T) {
	provider := NewTools(ToolDeps{})
	for _, tool := range provider.Tools() {
		_, err := tool.Handler(context.Background(), map[string]any{})
		var unavailable tools.ErrUnavailable
		if !errors.As(err, &unavailable) {
			t.Errorf("%s with no deps returned %v, want tools.ErrUnavailable", tool.Name, err)
		}
	}
}

// TestSystemHealthToolLeadsWithSummary pins the tool's flat envelope:
// the snapshot's sections are top-level keys beside the summary — the
// same shape the metacog panel renders — with no wrapper object between
// the model and the facts.
func TestSystemHealthToolLeadsWithSummary(t *testing.T) {
	insp := NewInspector(HealthSources{
		BusDropped: func() uint64 { return 3 },
	})
	provider := NewTools(ToolDeps{Inspector: insp})
	out, err := provider.handleSystemHealth(context.Background(), nil)
	if err != nil {
		t.Fatalf("system_health: %v", err)
	}
	var result struct {
		Summary     string      `json:"summary"`
		Annunciator []HealthRow `json:"annunciator"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("result not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(result.Summary, "1 of 1 annunciator rows not ok") || !strings.Contains(result.Summary, "event_bus") {
		t.Errorf("summary = %q, want the degraded bus named", result.Summary)
	}
	if len(result.Annunciator) != 1 || result.Annunciator[0].Status != HealthDegraded {
		t.Errorf("annunciator = %+v", result.Annunciator)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if _, nested := raw["health"]; nested {
		t.Errorf("tool result still nests the snapshot under \"health\"; the tool and the panel must share one flat shape")
	}
}

// TestToolAndPanelShareOnePayload pins the anti-drift contract: the
// system_health result and the panel's fenced JSON are the same payload
// from the same projection — same keys, same nesting, same summary —
// so a model reading one surface and then the other never sees the
// identical fact in two shapes.
func TestToolAndPanelShareOnePayload(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	insp := NewInspector(HealthSources{
		BusDropped: func() uint64 { return 7 },
		QueueStats: func(context.Context) ([]loopqueue.ConsumerPending, error) {
			return []loopqueue.ConsumerPending{
				{Consumer: "archivist", Pending: 2, OldestEnqueuedAt: now.Add(-10 * time.Minute)},
			}, nil
		},
	})
	insp.now = func() time.Time { return now }

	out, err := NewTools(ToolDeps{Inspector: insp}).handleSystemHealth(context.Background(), nil)
	if err != nil {
		t.Fatalf("system_health: %v", err)
	}
	var toolPayload map[string]any
	if err := json.Unmarshal([]byte(out), &toolPayload); err != nil {
		t.Fatalf("tool result not JSON: %v\n%s", err, out)
	}

	body, err := NewPanelProvider(insp, nil, nil).TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	panelPayload := panelJSON(t, body)

	// host.goroutines is the one field that is legitimately live per
	// render — runtime.NumGoroutine() can differ between the two calls
	// under parallel test scheduling — so equality is asserted over
	// everything else. (Surfaced as a CI-only flake on #1449.)
	for _, payload := range []map[string]any{toolPayload, panelPayload} {
		if host, ok := payload["host"].(map[string]any); ok {
			delete(host, "goroutines")
		}
	}

	if !reflect.DeepEqual(toolPayload, panelPayload) {
		t.Errorf("tool and panel payloads differ:\ntool:  %v\npanel: %v", toolPayload, panelPayload)
	}
}

func TestQueueStatusToolReportsAuditView(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := queue.Enqueue(ctx, "archivist", "session:done", 0, nil); err != nil {
		t.Fatal(err)
	}
	items, err := queue.Peek(ctx, "archivist", 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("peek queue item: items=%#v err=%v", items, err)
	}
	if outcome, err := queue.Ack(ctx, "archivist", "session:done", items[0].Receipt); err != nil || outcome != loopqueue.Acked {
		t.Fatalf("ack queue item outcome=%q err=%v", outcome, err)
	}
	if err := queue.Enqueue(ctx, "archivist", "session:waiting", 0, nil); err != nil {
		t.Fatal(err)
	}

	provider := NewTools(ToolDeps{Queue: queue})
	out, err := provider.handleQueueStatus(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("queue_status: %v", err)
	}
	var result struct {
		Pending []struct {
			Consumer string `json:"consumer"`
			Pending  int    `json:"pending"`
		} `json:"pending"`
		CompletionStats []struct {
			Consumer string `json:"consumer"`
			Count    int    `json:"count"`
		} `json:"completion_stats"`
		RecentCompletions []struct {
			Subject string `json:"subject"`
		} `json:"recent_completions"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("result not JSON: %v\n%s", err, out)
	}
	if len(result.Pending) != 1 || result.Pending[0].Consumer != "archivist" || result.Pending[0].Pending != 1 {
		t.Errorf("pending = %+v", result.Pending)
	}
	if len(result.CompletionStats) != 1 || result.CompletionStats[0].Count != 1 {
		t.Errorf("completion_stats = %+v", result.CompletionStats)
	}
	if len(result.RecentCompletions) != 1 || result.RecentCompletions[0].Subject != "session:done" {
		t.Errorf("recent_completions = %+v", result.RecentCompletions)
	}
	if !strings.Contains(result.Note, "coalesced") {
		t.Errorf("note missing the coalesce caveat: %q", result.Note)
	}

	if _, err := provider.handleQueueStatus(ctx, map[string]any{"window": "yesterday"}); err == nil || !strings.Contains(err.Error(), "-24h") {
		t.Errorf("bad window error must teach the delta form, got %v", err)
	}
}

func TestLoopActivityToolAggregatesAndLists(t *testing.T) {
	j := newTestJournal(t)
	now := time.Now().UTC()
	mustRecord(t, j,
		LoopEvent{At: now.Add(-2 * time.Hour), LoopName: "archivist", Kind: "loop_iteration_start", WakeReason: "mailbox", WakeSource: "session"},
		LoopEvent{At: now.Add(-2*time.Hour + time.Minute), LoopName: "archivist", Kind: "loop_iteration_complete"},
		LoopEvent{At: now.Add(-time.Hour), LoopName: "ego", Kind: "loop_iteration_start", WakeReason: "timer"},
	)

	provider := NewTools(ToolDeps{Journal: j})
	out, err := provider.handleLoopActivity(t.Context(), map[string]any{"loop_name": "archivist"})
	if err != nil {
		t.Fatalf("loop_activity: %v", err)
	}
	var result struct {
		Aggregate struct {
			Wakes    int            `json:"wakes"`
			ByReason map[string]int `json:"by_reason"`
		} `json:"aggregate"`
		Events []struct {
			AtDelta    string `json:"at_delta"`
			Kind       string `json:"kind"`
			WakeReason string `json:"wake_reason"`
		} `json:"events"`
		LimitReached bool `json:"limit_reached"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("result not JSON: %v\n%s", err, out)
	}
	if result.Aggregate.Wakes != 1 || result.Aggregate.ByReason["mailbox"] != 1 {
		t.Errorf("aggregate = %+v", result.Aggregate)
	}
	if len(result.Events) != 2 || result.Events[0].WakeReason != "mailbox" {
		t.Errorf("events = %+v, want archivist's two, chronological", result.Events)
	}
	if !strings.HasPrefix(result.Events[0].AtDelta, "-") {
		t.Errorf("at_delta = %q, want a signed past delta", result.Events[0].AtDelta)
	}
	if result.LimitReached {
		t.Errorf("limit_reached true on an unclipped result")
	}
}
