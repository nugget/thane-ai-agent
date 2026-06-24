//go:build uiharness

// UI harness: a build-tagged, fake-backed instance of the native /v1 server
// for iterating on the web console without a real Thane, a database, or
// production. It registers a curated subset of the real handlers — so the
// JSON it serves is byte-identical to production — backed by in-memory
// synthetic data. Endpoints not registered here simply 404, which the console
// degrades through (client.tryGet treats 404/503 as "absent").
//
// Build and run via cmd/uiharness (also //go:build uiharness); never compiled
// into the production binary.
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// harnessLoopReg is a static LoopStatusReader seeded with synthetic loops.
type harnessLoopReg struct {
	statuses []looppkg.Status
	byID     map[string]looppkg.Status
}

func (h harnessLoopReg) Statuses() []looppkg.Status { return h.statuses }

func (h harnessLoopReg) StatusByID(id string) (looppkg.Status, bool) {
	st, ok := h.byID[id]
	return st, ok
}

// harnessLogQuerier returns a small synthetic log tail for any queried
// conversation, so the per-loop log views have realistic content.
type harnessLogQuerier struct{}

func (harnessLogQuerier) Query(p logging.QueryParams) ([]logging.LogEntry, error) {
	if p.ConversationID == "" {
		return nil, nil
	}
	now := time.Now()
	entries := []logging.LogEntry{
		{Timestamp: now.Add(-1 * time.Second), Level: "INFO", Msg: "tool ha_get_state completed (light.office: on)", Tool: "ha_get_state", ConversationID: p.ConversationID, Subsystem: "agent"},
		{Timestamp: now.Add(-2 * time.Second), Level: "INFO", Msg: "tool doc_read completed (1.2KB)", Tool: "doc_read", ConversationID: p.ConversationID, Subsystem: "agent"},
		{Timestamp: now.Add(-3 * time.Second), Level: "INFO", Msg: "llm response received (in 5400, out 640)", Model: "claude-opus-4-8", ConversationID: p.ConversationID, Subsystem: "agent"},
		{Timestamp: now.Add(-5 * time.Second), Level: "WARN", Msg: "context window at 78% — consider compaction", ConversationID: p.ConversationID, Subsystem: "session"},
		{Timestamp: now.Add(-9 * time.Second), Level: "INFO", Msg: "iteration started", ConversationID: p.ConversationID, Subsystem: "loop"},
		{Timestamp: now.Add(-20 * time.Second), Level: "ERROR", Msg: "transient upstream 529, retrying", ConversationID: p.ConversationID, Subsystem: "router"},
	}
	if p.Limit > 0 && len(entries) > p.Limit {
		entries = entries[:p.Limit]
	}
	return entries, nil
}

// harnessRequestReader returns a synthetic request detail (a tool-call
// waterfall) for any request id, so the forensics request view has content.
type harnessRequestReader struct{}

func (harnessRequestReader) QueryRequestDetail(requestID string) (*logging.RequestDetail, error) {
	if requestID == "" {
		return nil, nil
	}
	return &logging.RequestDetail{
		RequestID:      requestID,
		Model:          "claude-opus-4-8",
		IterationCount: 1,
		InputTokens:    5400,
		OutputTokens:   640,
		CreatedAt:      time.Now().Add(-4 * time.Second).Format(time.RFC3339),
		ToolsUsed:      map[string]int{"ha_get_state": 1, "doc_read": 1},
		ToolCalls: []logging.ToolDetail{
			{ToolCallID: "call_1", ToolName: "ha_get_state", Arguments: `{"entity_id":"light.office"}`, Result: `{"state":"on","brightness":180,"color_temp":3000}`, IterationIndex: 0},
			{ToolCallID: "call_2", ToolName: "doc_read", Arguments: `{"path":"/notes/office.md"}`, Result: "Office automation notes: lights on motion, blinds at sunset, thermostat 21°C 8a–6p.", IterationIndex: 0},
		},
	}, nil
}

// harnessLoops is the synthetic loop tree: a processing supervisor root with a
// mix of sleeping, event-driven, and errored children, plus a parent/child
// channel pair — enough to exercise the graph's hierarchy, state styling, and
// service-degradation paths.
func harnessLoops() []looppkg.Status {
	now := time.Now()
	boot := now.Add(-6 * time.Hour)
	return []looppkg.Status{
		{
			ID: "supervisor", Name: "supervisor", State: looppkg.StateProcessing,
			StartedAt: boot, LastWakeAt: now.Add(-2 * time.Second),
			Iterations: 1423, Attempts: 1440,
			TotalInputTokens: 9_200_000, TotalOutputTokens: 1_100_000,
			LastInputTokens: 7400, LastOutputTokens: 820, ContextWindow: 200000,
			RecentConvIDs: []string{"conv-supervisor"},
		},
		{
			ID: "curator", Name: "curator", State: looppkg.StateSleeping, ParentID: "supervisor",
			StartedAt: boot, Iterations: 318, Attempts: 320,
			TotalInputTokens: 5_400_000, TotalOutputTokens: 410_000, ContextWindow: 131072,
			RecentConvIDs: []string{"conv-curator"},
		},
		{
			ID: "archivist", Name: "archivist", State: looppkg.StateSleeping, ParentID: "supervisor",
			StartedAt: boot, Iterations: 96, Attempts: 96, ContextWindow: 131072,
		},
		{
			ID: "signal", Name: "signal", State: looppkg.StateWaiting, ParentID: "supervisor",
			StartedAt: boot, EventDriven: true, Iterations: 51, Attempts: 51,
		},
		{
			ID: "signal/aimee", Name: "signal/aimee", State: looppkg.StateProcessing, ParentID: "signal",
			StartedAt: now.Add(-90 * time.Minute), LastWakeAt: now.Add(-1 * time.Second),
			Iterations: 12, Attempts: 12, LastInputTokens: 5200, LastOutputTokens: 640, ContextWindow: 200000,
			RecentConvIDs: []string{"conv-aimee"},
		},
		{
			ID: "mqtt", Name: "mqtt", State: looppkg.StateError, ParentID: "supervisor",
			StartedAt: boot, EventDriven: true,
			LastError: "connection refused: broker unreachable", ConsecutiveErrors: 4,
		},
	}
}

// harnessHealth reports synthetic dependency health, with mqtt degraded so the
// system status reads "degraded" and the mqtt loop renders in its degraded
// styling (the graph matches loop names against service keys).
func harnessHealth() map[string]DependencyStatus {
	stamp := time.Now().Format(time.RFC3339)
	return map[string]DependencyStatus{
		"signal":         {Name: "signal", Ready: true, LastCheck: stamp},
		"home_assistant": {Name: "home_assistant", Ready: true, LastCheck: stamp},
		"mqtt":           {Name: "mqtt", Ready: false, LastCheck: stamp, LastError: "broker unreachable"},
	}
}

// RunUIHarness serves the web console at addr, backed by synthetic /v1 data.
// staticDir is the directory of console assets to serve live (so JS/CSS/HTML
// edits show on reload without rebuilding the harness).
func RunUIHarness(addr, staticDir string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	loops := harnessLoops()
	byID := make(map[string]looppkg.Status, len(loops))
	for _, l := range loops {
		byID[l.ID] = l
	}

	bus := events.New()
	s := &Server{
		logger:        logger,
		loopRegistry:  harnessLoopReg{statuses: loops, byID: byID},
		logQuerier:    harnessLogQuerier{},
		requestReader: harnessRequestReader{},
		eventBus:      bus,
		healthDeps:    harnessHealth,
	}

	// Drive synthetic live activity so the console shows iteration / LLM /
	// mid-flight tool events (args in, result out) without a real Thane.
	go emitSyntheticActivity(bus)

	mux := http.NewServeMux()

	// Curated /v1 surface the node graph consumes. Anything not here 404s and
	// the console degrades through it.
	mux.HandleFunc("GET /v1/system", s.handleSystem)
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /v1/loops", s.handleLoops)
	mux.HandleFunc("GET /v1/loops/events", s.handleLoopEvents)
	mux.HandleFunc("GET /v1/loops/{id}", s.handleLoop)
	mux.HandleFunc("GET /v1/loops/{id}/logs", s.handleLoopLogs)
	mux.HandleFunc("GET /v1/requests/{id}", s.handleRequest)

	// Console static assets, served live from staticDir.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(staticDir))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
	})

	logger.Warn("ui harness listening", "addr", addr, "static", staticDir)
	return http.ListenAndServe(addr, mux)
}

// emitSyntheticActivity drives the "signal/aimee" loop through repeating turns
// — iteration start, LLM call, two mid-flight tool calls (args in, result out),
// then completion and sleep — publishing the same event kinds the runtime does.
// Subscribers that join mid-stream simply pick up from the next event (the bus
// drops to absent subscribers); the snapshot on connect seeds current state.
func emitSyntheticActivity(bus *events.Bus) {
	const id, name = "signal/aimee", "signal/aimee"
	turn := 0
	pub := func(kind string, data map[string]any) {
		data["loop_id"] = id
		data["loop_name"] = name
		bus.Publish(events.Event{Timestamp: time.Now(), Source: events.SourceLoop, Kind: kind, Data: data})
	}
	for {
		turn++
		pub("loop_iteration_start", map[string]any{
			"attempt": turn, "conversation_id": "conv-aimee", "request_id": fmt.Sprintf("req-aimee-%d", turn),
		})
		time.Sleep(500 * time.Millisecond)
		pub("loop_llm_start", map[string]any{
			"model": "claude-opus-4-8", "est_tokens": 5200, "intent": "respond", "complexity": "medium",
		})
		time.Sleep(700 * time.Millisecond)
		pub("loop_tool_start", map[string]any{"tool": "ha_get_state", "args": `{"entity_id":"light.office"}`})
		time.Sleep(900 * time.Millisecond)
		pub("loop_tool_done", map[string]any{"tool": "ha_get_state", "result": `{"state":"on","brightness":180}`})
		time.Sleep(400 * time.Millisecond)
		pub("loop_tool_start", map[string]any{"tool": "doc_read", "args": `{"path":"/notes/office.md"}`})
		time.Sleep(900 * time.Millisecond)
		pub("loop_tool_done", map[string]any{"tool": "doc_read", "result": "Office automation notes: lights, blinds, thermostat schedule…"})
		time.Sleep(500 * time.Millisecond)
		pub("loop_llm_response", map[string]any{"model": "claude-opus-4-8", "input_tokens": 5400, "output_tokens": 640})
		pub("loop_iteration_complete", map[string]any{
			"model": "claude-opus-4-8", "input_tokens": 5400, "output_tokens": 640, "duration_ms": 4200,
			"request_id": fmt.Sprintf("req-aimee-%d", turn), "conversation_id": "conv-aimee",
		})
		pub("loop_state_change", map[string]any{"from": "processing", "to": "sleeping"})
		time.Sleep(3 * time.Second)
		pub("loop_state_change", map[string]any{"from": "sleeping", "to": "processing"})
	}
}
