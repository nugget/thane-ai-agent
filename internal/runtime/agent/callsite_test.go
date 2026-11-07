package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// callSiteCall is what one model call arrived with.
type callSiteCall struct {
	site   llm.CallSite
	placed bool
	// live reports whether the call's context was still usable when the
	// call began, which a recovery on a detached context must be.
	live   bool
	budget int
}

// callSiteRecordingClient wraps a client and records the call scope of
// every model call made through it.
type callSiteRecordingClient struct {
	llm.Client
	mu    sync.Mutex
	calls []callSiteCall
}

func (c *callSiteRecordingClient) Chat(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, model, msgs, tools, nil)
}

func (c *callSiteRecordingClient) ChatStream(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any, stream llm.StreamCallback) (*llm.ChatResponse, error) {
	site, placed := llm.CallSiteFromContext(ctx)
	c.mu.Lock()
	c.calls = append(c.calls, callSiteCall{
		site:   site,
		placed: placed,
		live:   ctx.Err() == nil,
		budget: llm.MaxOutputTokensFromContext(ctx),
	})
	c.mu.Unlock()
	return c.Client.ChatStream(ctx, model, msgs, tools, stream)
}

func (c *callSiteRecordingClient) sites() []llm.CallSite {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]llm.CallSite, 0, len(c.calls))
	for _, call := range c.calls {
		if !call.placed {
			out = append(out, llm.CallSite{Kind: "<unplaced>"})
			continue
		}
		out = append(out, call.site)
	}
	return out
}

func (c *callSiteRecordingClient) last() callSiteCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

func assertCallSites(t *testing.T, got, want []llm.CallSite) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("call sites = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d site = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// toolResultHistory is a turn that already did tool work, which is what
// makes a timed-out turn eligible for the recovery model.
func toolResultHistory() []llm.Message {
	call := llm.ToolCall{ID: "call-1"}
	call.Function.Name = "recall_fact"
	return []llm.Message{
		{Role: "user", Content: "What does Alice prefer for breakfast?"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
		{Role: "tool", ToolCallID: "call-1", Content: "Alice prefers oatmeal."},
	}
}

// TestLLMErrorHandler_StampsCallSite drives the error handler with a call
// already placed at iteration 2 and pins the kind of every call it makes
// in that call's stead. The iteration must survive each of them, the
// timeout recovery included: it runs on a context rebuilt from
// Background so the deadline that just expired cannot cancel it, and
// that rebuild is where the placement used to be lost.
func TestLLMErrorHandler_StampsCallSite(t *testing.T) {
	t.Parallel()

	const iteration = 2
	const budget = 321

	tests := []struct {
		name          string
		err           error
		callErrors    []error
		recoveryModel string
		loopModel     string
		unplaced      bool
		want          []llm.CallSite
		wantRecovery  bool
	}{
		{
			name:       "timeout retry keeps the iteration",
			err:        context.DeadlineExceeded,
			callErrors: []error{context.DeadlineExceeded, nil},
			want: []llm.CallSite{
				{Iteration: iteration, Kind: llm.CallKindRetry},
				{Iteration: iteration, Kind: llm.CallKindRetry},
			},
		},
		{
			name:          "timeout recovery keeps the iteration across the detached context",
			err:           context.DeadlineExceeded,
			callErrors:    []error{context.DeadlineExceeded, context.DeadlineExceeded, nil},
			recoveryModel: "recovery-model",
			want: []llm.CallSite{
				{Iteration: iteration, Kind: llm.CallKindRetry},
				{Iteration: iteration, Kind: llm.CallKindRetry},
				{Iteration: iteration, Kind: llm.CallKindTimeoutRecovery},
			},
			wantRecovery: true,
		},
		{
			name:      "failover keeps the iteration",
			err:       errors.New("synthetic provider failure"),
			loopModel: "fallback-model",
			want:      []llm.CallSite{{Iteration: iteration, Kind: llm.CallKindFailover}},
		},
		{
			name:      "an unplaced failed call is not given a placement",
			err:       errors.New("synthetic provider failure"),
			loopModel: "fallback-model",
			unplaced:  true,
			want:      []llm.CallSite{{Kind: "<unplaced>"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			answer := &llm.ChatResponse{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "Alice prefers oatmeal."}}
			mock := &mockTimeoutLLM{responses: []*llm.ChatResponse{answer}, errors: tt.callErrors}
			rec := &callSiteRecordingClient{Client: mock}
			loop := buildTestLoopWithLLM(rec, nil)
			loop.recoveryModel = tt.recoveryModel
			if tt.loopModel != "" {
				loop.model = tt.loopModel
			}

			// The failed call's context: placed, budgeted, and past its
			// deadline, as a timed-out iteration's context is.
			iterCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			iterCtx = llm.WithMaxOutputTokens(iterCtx, budget)
			if !tt.unplaced {
				iterCtx = llm.WithCallSite(iterCtx, llm.CallSite{Iteration: iteration, Kind: llm.CallKindIteration})
			}

			var outcome llmErrorOutcome
			handler := loop.buildLLMErrorHandler(context.Background(), nil, rec, &Request{}, &outcome)
			resp, _, err := handler(iterCtx, tt.err, "stalled-model", toolResultHistory(), nil, nil)
			if err != nil || resp == nil {
				t.Fatalf("handler = resp %v, err %v; want a response", resp, err)
			}

			assertCallSites(t, rec.sites(), tt.want)
			if tt.wantRecovery {
				last := rec.last()
				if !last.live {
					t.Error("timeout recovery ran on a context that was already done")
				}
				if last.budget != budget {
					t.Errorf("timeout recovery budget = %d, want %d carried across", last.budget, budget)
				}
			}
		})
	}
}

// TestRun_StampsCallSite pins the stamp end to end through Run: the
// engine places the call, and the error handler re-stamps the same
// iteration when it stands in for it. The lite path, which runs no
// engine, places its single call itself.
func TestRun_StampsCallSite(t *testing.T) {
	t.Parallel()

	// Fresh responses per run: the loop annotates the responses it is
	// handed, so parallel rows must not share them.
	toolTurn := func() *llm.ChatResponse {
		call := llm.ToolCall{ID: "call-1"}
		call.Function.Name = "recall_fact"
		return &llm.ChatResponse{Model: "test-model", Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}}
	}
	answer := func() *llm.ChatResponse {
		return &llm.ChatResponse{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "Alice prefers oatmeal."}}
	}

	tests := []struct {
		name      string
		req       Request
		responses func() []*llm.ChatResponse
		errors    []error
		want      []llm.CallSite
	}{
		{
			name:      "tool call then answer",
			req:       Request{Messages: []Message{{Role: "user", Content: "What does Alice prefer?"}}},
			responses: func() []*llm.ChatResponse { return []*llm.ChatResponse{toolTurn(), answer()} },
			want: []llm.CallSite{
				{Iteration: 0, Kind: llm.CallKindIteration},
				{Iteration: 1, Kind: llm.CallKindIteration},
			},
		},
		{
			name:      "timeout in the second iteration is retried in place",
			req:       Request{Messages: []Message{{Role: "user", Content: "What does Alice prefer?"}}},
			responses: func() []*llm.ChatResponse { return []*llm.ChatResponse{toolTurn(), answer()} },
			errors:    []error{nil, context.DeadlineExceeded, nil},
			want: []llm.CallSite{
				{Iteration: 0, Kind: llm.CallKindIteration},
				{Iteration: 1, Kind: llm.CallKindIteration},
				{Iteration: 1, Kind: llm.CallKindRetry},
			},
		},
		{
			name:      "lite request",
			req:       Request{SkipContext: true, Messages: []Message{{Role: "user", Content: "Title this chat with Bob."}}},
			responses: func() []*llm.ChatResponse { return []*llm.ChatResponse{answer()} },
			want:      []llm.CallSite{{Iteration: 0, Kind: llm.CallKindLite}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &callSiteRecordingClient{Client: &mockTimeoutLLM{responses: tt.responses(), errors: tt.errors}}
			loop := buildTestLoopWithLLM(rec, []string{"recall_fact"})

			if _, err := loop.Run(context.Background(), &tt.req, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			assertCallSites(t, rec.sites(), tt.want)
		})
	}
}

// TestRun_StampsContextRecoveryCallSite pins the two context-recovery
// kinds: the retry that keeps its tools after the runner's window was
// refreshed, and the retry that drops them to fit. A turn that offered
// no tools has none to drop, so its retry is the plain kind even though
// it, too, goes out without tools. The loaded instance
// has no id, so the retry returns through the loop's own client where
// the recorder can see it; the direct-instance branch is handed the same
// stamped context. The response line carries the iteration's request
// estimate only when the retry resent that request: a retry that dropped
// the tools answered a smaller one.
func TestRun_StampsContextRecoveryCallSite(t *testing.T) {
	t.Parallel()

	const maxContext = 131072
	const contextErr = `{"error":"The number of tokens to keep from the initial prompt is greater than the context length. Try to load the model with a larger context length, or provide a shorter input"}`

	tests := []struct {
		name string
		// trainedForTools decides whether dropping the tools is a lever.
		trainedForTools bool
		initialContext  int
		// growAfterFirstChat simulates the runner reloading larger on
		// its own, so recovery's refresh sees a changed window.
		growAfterFirstChat bool
		rejectWithTools    bool
		// noTools runs the turn with an empty registry, so it offers
		// the model no tools at all.
		noTools bool
		want    []llm.CallSite
		// responseEstimate says whether the llm response line must
		// carry est_request_tokens.
		responseEstimate bool
	}{
		{
			name:               "refreshed window retries with tools",
			trainedForTools:    true,
			initialContext:     32768,
			growAfterFirstChat: true,
			want: []llm.CallSite{
				{Iteration: 0, Kind: llm.CallKindIteration},
				{Iteration: 0, Kind: llm.CallKindContextRecovery},
			},
			responseEstimate: true,
		},
		{
			name:               "refreshed window on a tool-less turn is not the tool-free kind",
			trainedForTools:    true,
			initialContext:     32768,
			growAfterFirstChat: true,
			noTools:            true,
			want: []llm.CallSite{
				{Iteration: 0, Kind: llm.CallKindIteration},
				{Iteration: 0, Kind: llm.CallKindContextRecovery},
			},
			responseEstimate: true,
		},
		{
			name:            "window at maximum retries without tools",
			initialContext:  maxContext,
			rejectWithTools: true,
			want: []llm.CallSite{
				{Iteration: 0, Kind: llm.CallKindIteration},
				{Iteration: 0, Kind: llm.CallKindContextRecoveryNoTools},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			loadedContext := tt.initialContext
			chatCalls := 0

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/models":
					mu.Lock()
					current := loadedContext
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{
						"models": []map[string]any{{
							"key":                "google/gemma-3-4b",
							"type":               "vlm",
							"architecture":       "gemma3",
							"format":             "mlx",
							"max_context_length": maxContext,
							"capabilities": map[string]any{
								"vision":               true,
								"trained_for_tool_use": tt.trainedForTools,
							},
							"loaded_instances": []map[string]any{{
								"config": map[string]any{"context_length": current},
							}},
						}},
					})
				case "/v1/chat/completions":
					var req struct {
						Tools []map[string]any `json:"tools"`
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Errorf("decode chat request: %v", err)
						http.Error(w, "bad request", http.StatusBadRequest)
						return
					}
					mu.Lock()
					current := loadedContext
					chatCalls++
					if tt.growAfterFirstChat && chatCalls == 1 {
						loadedContext = maxContext
					}
					mu.Unlock()
					if current < maxContext || (tt.rejectWithTools && len(req.Tools) > 0) {
						http.Error(w, contextErr, http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"model": "deepslate/google/gemma-3-4b",
						"choices": []map[string]any{{
							"index":   0,
							"message": map[string]any{"role": "assistant", "content": "ok"},
						}},
					})
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			cfg := &config.Config{
				Models: config.ModelsConfig{
					Default: "gpt-oss:20b",
					Resources: map[string]config.ModelServerConfig{
						"spark":     {URL: "http://spark.example", Provider: "ollama"},
						"deepslate": {URL: srv.URL, Provider: "lmstudio", APIKey: "synthetic-token"},
					},
					Available: []config.ModelConfig{{
						Name:          "gpt-oss:20b",
						Resource:      "spark",
						SupportsTools: true,
						ContextWindow: 8192,
					}},
				},
			}
			cat, err := fleet.BuildCatalog(cfg)
			if err != nil {
				t.Fatalf("fleet.BuildCatalog: %v", err)
			}
			runtime, err := fleet.NewRuntime(context.Background(), cat, cfg, nil)
			if err != nil {
				t.Fatalf("fleet.NewRuntime: %v", err)
			}
			rec := &callSiteRecordingClient{Client: runtime.Client()}
			loop := buildTestLoopWithLLM(rec, []string{"ha_get_state"})
			if tt.noTools {
				loop.tools = tools.NewEmptyRegistry()
			}
			loop.UseModelRegistry(runtime.Registry())
			loop.UseModelRuntime(runtime)

			var logs lockedBuffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
			resp, err := loop.Run(ctx, &Request{
				Model: "deepslate/google/gemma-3-4b",
				Messages: []Message{{
					Role:    "user",
					Content: "Reply with exactly ok after checking the image.",
					Images:  []llm.ImageContent{{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"}},
				}},
			}, nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if strings.TrimSpace(resp.Content) != "ok" {
				t.Fatalf("resp.Content = %q, want ok", resp.Content)
			}
			assertCallSites(t, rec.sites(), tt.want)

			call, ok := logs.records(t, "llm call")[0]
			if !ok {
				t.Fatal("no llm call line for iteration 0")
			}
			response, ok := logs.records(t, "llm response")[0]
			if !ok {
				t.Fatal("no llm response line for iteration 0")
			}
			_, hasEstimate := response["est_request_tokens"]
			switch {
			case tt.responseEstimate && !hasEstimate:
				t.Error("llm response line lacks est_request_tokens for a retry that resent the request")
			case tt.responseEstimate:
				if got, want := intAttr(t, response, "est_request_tokens"), intAttr(t, call, "est_request_tokens"); got != want {
					t.Errorf("response est_request_tokens = %d, want the llm call line's %d", got, want)
				}
			case hasEstimate:
				t.Errorf("response est_request_tokens = %v, want none for a retry that dropped the tools",
					response["est_request_tokens"])
			}
		})
	}
}
