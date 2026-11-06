package iterate

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// callSiteRecorder wraps a client and records the call site each model
// call arrived with. A call that carried none is recorded with ok=false.
type callSiteRecorder struct {
	llm.Client
	mu    sync.Mutex
	sites []recordedCallSite
}

type recordedCallSite struct {
	site llm.CallSite
	ok   bool
}

func (r *callSiteRecorder) ChatStream(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any, stream llm.StreamCallback) (*llm.ChatResponse, error) {
	site, ok := llm.CallSiteFromContext(ctx)
	r.mu.Lock()
	r.sites = append(r.sites, recordedCallSite{site: site, ok: ok})
	r.mu.Unlock()
	return r.Client.ChatStream(ctx, model, msgs, tools, stream)
}

func (r *callSiteRecorder) recorded() []recordedCallSite {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedCallSite(nil), r.sites...)
}

func placed(iteration int, kind llm.CallKind) recordedCallSite {
	return recordedCallSite{site: llm.CallSite{Iteration: iteration, Kind: kind}, ok: true}
}

// TestEngine_StampsCallSite pins the call site on every model call the
// engine makes. The force-text call is placed at the index its own
// iteration record receives, so a capture keyed on the call site joins
// to the record that describes it. Tools and callbacks never see a stamp:
// it rides only the model call's context.
func TestEngine_StampsCallSite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []*llm.ChatResponse
		errors    []error
		configure func(*Config, *callSiteRecorder)
		want      []recordedCallSite
		// wantRecords asserts the call sites line up one-to-one with
		// the iteration records. Off for rows where a call made no
		// record of its own.
		wantRecords bool
	}{
		{
			name:        "single text answer",
			responses:   []*llm.ChatResponse{textResponse("done")},
			want:        []recordedCallSite{placed(0, llm.CallKindIteration)},
			wantRecords: true,
		},
		{
			name: "tool call then text",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("search", nil)),
				textResponse("done"),
			},
			want:        []recordedCallSite{placed(0, llm.CallKindIteration), placed(1, llm.CallKindIteration)},
			wantRecords: true,
		},
		{
			name: "nudge after an empty answer is an ordinary iteration",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("search", nil)),
				textResponse(""),
				textResponse("nudged"),
			},
			configure: func(cfg *Config, _ *callSiteRecorder) { cfg.NudgeOnEmpty = true },
			want: []recordedCallSite{
				placed(0, llm.CallKindIteration),
				placed(1, llm.CallKindIteration),
				placed(2, llm.CallKindIteration),
			},
			wantRecords: true,
		},
		{
			name: "max iterations closes with force text at the next index",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("search", nil)),
				toolCallResponse(makeToolCall("search", map[string]any{"q": "again"})),
				textResponse("forced"),
			},
			configure: func(cfg *Config, _ *callSiteRecorder) { cfg.MaxIterations = 2 },
			want: []recordedCallSite{
				placed(0, llm.CallKindIteration),
				placed(1, llm.CallKindIteration),
				placed(2, llm.CallKindForceText),
			},
			wantRecords: true,
		},
		{
			name: "budget spent inside the loop closes with force text at the next index",
			responses: []*llm.ChatResponse{
				toolCallResponse(makeToolCall("search", nil)),
				toolCallResponse(makeToolCall("search", map[string]any{"q": "again"})),
				textResponse("forced"),
			},
			configure: func(cfg *Config, _ *callSiteRecorder) {
				cfg.CheckBudget = func(totalOutput int) bool { return totalOutput >= 20 }
			},
			want: []recordedCallSite{
				placed(0, llm.CallKindIteration),
				placed(1, llm.CallKindIteration),
				placed(2, llm.CallKindForceText),
			},
			wantRecords: true,
		},
		{
			name:      "error handler receives the failed call's site",
			responses: []*llm.ChatResponse{nil, textResponse("recovered")},
			errors:    []error{errors.New("synthetic failure")},
			configure: func(cfg *Config, rec *callSiteRecorder) {
				cfg.OnLLMError = func(ctx context.Context, _ error, model string, msgs []llm.Message, toolDefs []map[string]any, stream llm.StreamCallback) (*llm.ChatResponse, string, error) {
					resp, err := rec.ChatStream(llm.WithCallKind(ctx, llm.CallKindRetry), model, msgs, toolDefs, stream)
					return resp, model, err
				}
			},
			want: []recordedCallSite{
				placed(0, llm.CallKindIteration),
				placed(0, llm.CallKindRetry),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockLLM{responses: tt.responses, errors: tt.errors}
			rec := &callSiteRecorder{Client: mock}
			cfg := baseCfg(mock, &mockExecutor{})
			cfg.LLM = rec
			var leaked []string
			var leakMu sync.Mutex
			noteLeak := func(where string, ctx context.Context) {
				if site, ok := llm.CallSiteFromContext(ctx); ok {
					leakMu.Lock()
					leaked = append(leaked, where+": "+string(site.Kind))
					leakMu.Unlock()
				}
			}
			cfg.OnIterationStart = func(ctx context.Context, _ int, _ string, _ []llm.Message, _ []map[string]any) {
				noteLeak("iteration start", ctx)
			}
			cfg.OnBeforeToolExec = func(ctx context.Context, _ int, _ llm.ToolCall) context.Context {
				noteLeak("tool exec", ctx)
				return ctx
			}
			if tt.configure != nil {
				tt.configure(&cfg, rec)
			}

			result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			got := rec.recorded()
			if len(got) != len(tt.want) {
				t.Fatalf("calls = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("call %d site = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
			if tt.wantRecords {
				if len(result.Iterations) != len(got) {
					t.Fatalf("iteration records = %d, calls = %d", len(result.Iterations), len(got))
				}
				for i, record := range result.Iterations {
					if record.Index != got[i].site.Iteration {
						t.Errorf("record %d index = %d, call site iteration = %d", i, record.Index, got[i].site.Iteration)
					}
				}
			}
			if len(leaked) > 0 {
				t.Errorf("call site leaked outside the model call: %v", leaked)
			}
		})
	}
}
