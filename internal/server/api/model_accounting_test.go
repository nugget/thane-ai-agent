package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

type apiUsageRunner func(context.Context, looppkg.Request, looppkg.StreamCallback) (*looppkg.Response, error)

func (r apiUsageRunner) Run(ctx context.Context, req looppkg.Request, stream looppkg.StreamCallback) (*looppkg.Response, error) {
	return r(ctx, req, stream)
}

type apiTokenObserver func(int, int)

func (o apiTokenObserver) OnTokens(input, output int) { o(input, output) }

func apiUsageRecords() []usage.Record {
	return []usage.Record{
		{
			Timestamp: time.Now(), Model: "primary/claude-source", UpstreamModel: "claude-source",
			Resource: "primary", Provider: "anthropic", InputTokens: 100, OutputTokens: 10,
			CostUSD: 0.0003, PricingStatus: "priced",
		},
		{
			Timestamp: time.Now(), Model: "recovery/claude-fallback", UpstreamModel: "claude-fallback",
			Resource: "recovery", Provider: "anthropic", InputTokens: 300, OutputTokens: 30,
			CacheCreationInputTokens: 50, CacheCreation5mInputTokens: 20,
			CacheCreation1hInputTokens: 30, CacheReadInputTokens: 40,
			CostUSD: 0.004312, PricingStatus: "priced",
		},
	}
}

func configureAPIUsageRunner(s *Server, runner looppkg.Runner) {
	s.ConfigureChatLoopLauncher(func(ctx context.Context, launch looppkg.Launch) (looppkg.LaunchResult, error) {
		return looppkg.NewRegistry().Launch(ctx, launch, looppkg.Deps{Runner: runner, Logger: testAPILogger()})
	})
}

func TestChatUsageUsesPricedCallsAcrossHandlers(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
	}{
		{"completion", "/v1/chat/completions", `{"messages":[{"role":"user","content":"Inspect the source"}]}`},
		{"streaming", "/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"Inspect the source"}]}`},
		{"simple", "/v1/chat", `{"message":"Inspect the source"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := testAPIUsageStore(t)
			records := apiUsageRecords()
			for _, rec := range records {
				if err := ledger.Record(context.Background(), rec); err != nil {
					t.Fatal(err)
				}
			}
			server := NewServer("", 0, nil, nil, nil, ledger, nil, nil, nil, nil, testAPILogger())
			var observerMu sync.Mutex
			var observed [][2]int
			server.SetTokenObserver(apiTokenObserver(func(input, output int) {
				// Reentry must be safe, and the call must already be visible.
				if server.stats.Snapshot().TotalInputTokens == 0 {
					t.Error("token observer ran before session stats updated")
				}
				observerMu.Lock()
				observed = append(observed, [2]int{input, output})
				observerMu.Unlock()
			}))
			configureAPIUsageRunner(server, apiUsageRunner(func(ctx context.Context, _ looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
				for _, rec := range records {
					usage.Observe(ctx, rec)
				}
				if snap := server.stats.Snapshot(); snap.TotalRequests != 0 || snap.TotalInputTokens != 400 {
					return nil, errors.New("model calls must be visible before request completion")
				}
				return &looppkg.Response{
					Content: "Source inspected.", Model: "recovery/claude-fallback", FinishReason: "stop",
					InputTokens: 400, OutputTokens: 40, CacheCreationInputTokens: 50, CacheReadInputTokens: 40,
				}, nil
			}))
			rr := httptest.NewRecorder()
			handler := server.Handler()
			if tc.path == "/v1/chat/completions" {
				handler = NewOpenAIServer("", 0, "", nil, server, testAPILogger()).Handler()
			}
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
			}
			snap := server.stats.Snapshot()
			if snap.TotalRequests != 1 || server.LastRequest().IsZero() {
				t.Fatalf("successful request accounting = %+v, last request = %v", snap, server.LastRequest())
			}
			start, end := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
			sum, err := ledger.Summary(start, end)
			if err != nil {
				t.Fatal(err)
			}
			if snap.TotalInputTokens != sum.TotalInputTokens || snap.TotalOutputTokens != sum.TotalOutputTokens ||
				snap.PricedRecords != sum.PricedRecords || snap.UnpricedRecords != sum.UnpricedRecords || snap.UnknownPricingRecords != sum.UnknownPricingRecords ||
				snap.TotalCacheCreationInputTokens != sum.TotalCacheCreationInputTokens ||
				snap.TotalCacheReadInputTokens != sum.TotalCacheReadInputTokens ||
				math.Abs(snap.EstimatedCostUSD-sum.TotalCostUSD) > 1e-12 {
				t.Fatalf("live usage = %+v, ledger = %+v", snap, sum)
			}
			groups, err := ledger.SummaryByModel(start, end)
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range groups {
				if got := snap.ByModel[group.Key]; !reflect.DeepEqual(got, group.Summary) {
					t.Errorf("model %s live = %+v, ledger = %+v", group.Key, got, group.Summary)
				}
			}
			if snap.ByProvider["anthropic"].TotalRecords != 2 || snap.ByResource["primary"].TotalInputTokens != 100 ||
				snap.ByResource["recovery"].TotalInputTokens != 300 || snap.ByUpstreamModel["claude-fallback"].TotalCacheReadInputTokens != 40 {
				t.Fatalf("incorrect deployment breakdowns: %+v", snap)
			}
			observerMu.Lock()
			defer observerMu.Unlock()
			if want := [][2]int{{100, 10}, {300, 30}}; !reflect.DeepEqual(observed, want) {
				t.Fatalf("observed tokens = %v, want %v", observed, want)
			}
		})
	}
}

func TestRunChatLoopRetainsUsageOnFailureWithoutLedger(t *testing.T) {
	for _, failed := range []struct {
		name string
		err  error
	}{
		{"provider error", errors.New("provider failed after reporting usage")},
		{"cancellation", context.Canceled},
	} {
		t.Run(failed.name, func(t *testing.T) {
			server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
			configureAPIUsageRunner(server, apiUsageRunner(func(ctx context.Context, _ looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
				if errors.Is(failed.err, context.Canceled) {
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = canceled
				}
				usage.Observe(ctx, apiUsageRecords()[0])
				return nil, failed.err
			}))
			resp, err := server.runChatLoop(context.Background(), &agent.Request{
				ConversationID: "failed-usage", Messages: []agent.Message{{Role: "user", Content: "Inspect the source"}},
			}, nil, "api/failure")
			if err == nil || resp != nil {
				t.Fatalf("failed request returned success: response=%+v error=%v", resp, err)
			}
			snap := server.stats.Snapshot()
			if snap.TotalRequests != 0 || !server.LastRequest().IsZero() || snap.TotalInputTokens != 100 || snap.TotalOutputTokens != 10 || snap.ByModel["primary/claude-source"].TotalRecords != 1 || snap.PricedRecords != 1 || snap.UnpricedRecords != 0 || snap.UnknownPricingRecords != 0 {
				t.Fatalf("failed-call accounting = %+v, last request = %v", snap, server.LastRequest())
			}
		})
	}
}

func TestSessionStatsRecordCallConcurrent(t *testing.T) {
	server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
	const calls = 32
	var workers sync.WaitGroup
	for range calls {
		workers.Go(func() {
			server.stats.RecordCall(apiUsageRecords()[0])
			_ = server.stats.Snapshot()
		})
	}
	workers.Wait()
	snap := server.stats.Snapshot()
	if snap.TotalRequests != 0 || snap.TotalInputTokens != calls*100 || snap.ByModel["primary/claude-source"].TotalRecords != calls || snap.PricedRecords != calls || snap.UnpricedRecords != 0 || snap.UnknownPricingRecords != 0 {
		t.Fatalf("concurrent call totals = %+v", snap)
	}
}
