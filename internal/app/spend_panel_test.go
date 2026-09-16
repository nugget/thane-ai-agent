package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/telemetry"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/state/introspection"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newSpendPanelTestApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	provider := &auxiliaryTestClient{}
	a, db := newAuxiliaryUsageTestApp(t, provider)
	a.cfg.DataDir = t.TempDir()
	var err error
	a.loop, err = agent.NewLoop(agent.LoopOptions{
		Logger: a.logger, Memory: memory.NewStore(100), LLM: provider, Model: "edge/test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.initLoopUsageStores(db, a.logger); err != nil {
		t.Fatal(err)
	}
	// This minimal App has no archive. Wire only its real ledger into the
	// unrelated telemetry collector instead of passing a typed nil archive.
	a.telCollector = telemetry.NewCollector(telemetry.Sources{UsageStore: a.usageStore, Logger: a.logger})
	a.initInspector()
	return a, db
}

func appHealthSpend(t *testing.T, ctx context.Context, a *App) introspection.SpendView {
	t.Helper()
	result, err := a.loop.Tools().Execute(ctx, "system_health", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Spend introspection.SpendView `json:"spend"`
	}
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatal(err)
	}
	return health.Spend
}

func TestSpendPanelAppWiringUsesConfiguredLedger(t *testing.T) {
	a, _ := newSpendPanelTestApp(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, record := range []usage.Record{
		{Timestamp: now.Add(-time.Hour), LoopID: "full-loop-instance-id", LoopName: "metacognitive", Purpose: "compaction", Provider: "anthropic", Role: "autonomous", PricingStatus: "priced", CostUSD: 4, InputTokens: 100},
		{Timestamp: now.Add(-2 * time.Hour), Provider: "anthropic", Role: "interactive", PricingStatus: "priced", CostUSD: 2, InputTokens: 50},
		{Timestamp: now.Add(-3 * time.Hour), Provider: "ollama", Role: "auxiliary", PricingStatus: "priced", InputTokens: 3200000},
		{Timestamp: now.Add(-25 * time.Hour), LoopID: "prior-loop-instance-id", LoopName: "prior", PricingStatus: "priced", CostUSD: 3},
		{Timestamp: now.Add(-49 * time.Hour), PricingStatus: "priced", CostUSD: 99},
	} {
		if err := a.usageStore.Record(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	workers := 0
	for _, worker := range a.pendingWorkers {
		if worker.name == "recorded-spend" {
			workers++
		}
	}
	if workers != 1 {
		t.Fatalf("recorded-spend workers=%d, want one shared lifecycle worker", workers)
	}
	before := appHealthSpend(t, t.Context(), a)
	if before.Status != "pending" || before.Last24Hours != nil || before.Previous24Hours != nil {
		t.Fatalf("before startup spend=%+v, want pending without invented totals", before)
	}
	if err := a.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var got introspection.SpendView
	for {
		got = appHealthSpend(t, t.Context(), a)
		if got.Status == "available" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("registered health tool never received ledger snapshot: %+v", got)
		case <-tick.C:
		}
	}
	if got.Last24Hours == nil || got.Previous24Hours == nil || got.Comparison == nil || got.TopLoops == nil {
		t.Fatalf("available view omitted the configured report: %+v", got)
	}
	if got.Last24Hours.Summary.TotalRecords != 3 || got.Last24Hours.Summary.TotalCostUSD != 6 || got.Last24Hours.Summary.TotalInputTokens != 3200150 || got.Last24Hours.Summary.PricedRecords != 3 || got.Last24Hours.Unattributed.TotalCostUSD != 2 {
		t.Errorf("recent window lost configured ledger records: %+v", got.Last24Hours)
	}
	if got.Previous24Hours.Summary.TotalRecords != 1 || got.Previous24Hours.Summary.TotalCostUSD != 3 {
		t.Errorf("prior window=%+v", got.Previous24Hours)
	}
	if got.Comparison.Status != "available" || got.Comparison.CostChangeUSD == nil || *got.Comparison.CostChangeUSD != 3 || got.Comparison.CostChangePercent == nil || *got.Comparison.CostChangePercent != 100 {
		t.Errorf("comparison=%+v", got.Comparison)
	}
	if got.ByProvider == nil || got.ByProvider.Returned != 2 || got.ByProvider.Groups[0].Key != "anthropic" || got.ByProvider.Groups[0].Summary.TotalCostUSD != 6 || got.ByProvider.Groups[1].Summary.TotalInputTokens != 3200000 {
		t.Fatalf("provider headline lost real ledger usage: %+v", got.ByProvider)
	}
	if got.ByRole == nil || got.ByRole.Matched != 3 || got.ByRole.Returned < 2 || got.ByRole.Groups[1].Key != "interactive" || got.ByRole.Groups[1].Summary.TotalCostUSD != 2 {
		t.Fatalf("role headline lost unattributed usage: %+v", got.ByRole)
	}
	if got.TopLoops.Matched != 1 || got.TopLoops.RecordedCostSharePercent == nil || math.Abs(*got.TopLoops.RecordedCostSharePercent-4.0/6*100) > 1e-9 || got.TopLoops.Returned != len(got.TopLoops.Loops) || got.TopLoops.Truncated != (got.TopLoops.Returned < 1) {
		t.Errorf("loop attribution=%+v", got.TopLoops)
	}
	for _, loop := range got.TopLoops.Loops {
		if loop.LoopID != "full-loop-instance-id" || loop.Summary.TotalCostUSD != 4 {
			t.Errorf("loop detail=%+v", loop)
		}
	}
}

func TestSpendPanelAppCloseCancelsBlockedRefresh(t *testing.T) {
	a, db := newSpendPanelTestApp(t)
	// Warm unrelated telemetry before occupying the sole in-memory DB
	// connection. The spend worker will block acquiring that connection.
	if got := appHealthSpend(t, t.Context(), a); got.Status != "pending" {
		t.Fatalf("initial spend=%+v", got)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waits := db.Stats().WaitCount
	if err := a.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for db.Stats().WaitCount == waits {
		select {
		case <-deadline.C:
			t.Fatal("spend worker never attempted the blocked ledger read")
		case <-tick.C:
		}
	}
	closed := make(chan struct{})
	go func() { defer close(closed); a.Close() }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("App.Close did not cancel and join the blocked spend worker")
	}
	// The startup parent remains live; stopping the worker is App.Close's
	// responsibility, rather than an accidental consequence of test cleanup.
	if err := t.Context().Err(); err != nil {
		t.Fatalf("startup parent was canceled before App.Close finished: %v", err)
	}
}
