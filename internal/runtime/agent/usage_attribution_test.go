package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestRun_UsageAttributionReachesModelsToolsAndExtraction(t *testing.T) {
	l, store, _ := newAccountingTestLoop(t)
	conversationID := "unrelated-conversation-name"
	var providerAttributions []llm.Attribution
	toolAttribution := make(chan llm.Attribution, 1)
	extractAttribution := make(chan llm.Attribution, 1)
	l.tools.Register(&tools.Tool{
		Name: "inspect", Description: "Inspect source evidence.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			toolAttribution <- llm.AttributionFromContext(ctx)
			return "source evidence", nil
		},
	})
	l.extractor = memory.NewExtractor(nil, l.logger, 0)
	l.extractor.SetExtractFunc(func(ctx context.Context, _, _ string, _ []memory.Message) (*memory.ExtractionResult, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("extraction lost bounded lifetime")
		}
		extractAttribution <- llm.AttributionFromContext(ctx)
		return &memory.ExtractionResult{}, nil
	})
	l.llm = accountingTestClient{call: func(ctx context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
		providerAttributions = append(providerAttributions, llm.AttributionFromContext(ctx))
		if len(providerAttributions) == 1 {
			return accountingToolResponse(), nil
		}
		return &llm.ChatResponse{Model: model, InputTokens: 200, OutputTokens: 20,
			Message: llm.Message{Role: "assistant", Content: "The source evidence supports the recorded conclusion."}}, nil
	}}
	var observed []usage.Record
	ctx := usage.WithObserver(llm.WithAttribution(t.Context(), llm.Attribution{
		RequestID: "spawning-request", SessionID: "spawning-session", ConversationID: "spawning-conversation",
		LoopID: "spawning-loop", LoopName: "spawning-name", ParentLoopID: "spawning-parent",
	}), func(rec usage.Record) { observed = append(observed, rec) })
	response, err := l.Run(ctx, &Request{
		ConversationID: conversationID, Model: "claude-primary", UsageRole: "autonomous",
		RoutingFactors: map[string]string{"loop_id": "actual-loop", "loop_name": "actual-name", "parent_loop_id": "actual-parent"},
		Messages:       []Message{{Role: "user", Content: "Inspect the source and explain the evidence."}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Attribution{
		RequestID: response.RequestID, SessionID: l.archiver.ActiveSessionID(conversationID), ConversationID: conversationID,
		LoopID: "actual-loop", LoopName: "actual-name", ParentLoopID: "actual-parent",
	}
	if len(want.SessionID) <= 8 {
		t.Fatalf("session identity was shortened: %q", want.SessionID)
	}
	for i, got := range providerAttributions {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("provider attribution %d=%+v, want %+v", i, got, want)
		}
	}
	for name, ch := range map[string]<-chan llm.Attribution{"tool": toolAttribution, "extraction": extractAttribution} {
		select {
		case got := <-ch:
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s attribution=%+v, want %+v", name, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not receive attributed context", name)
		}
	}
	if len(observed) != 2 {
		t.Fatalf("observer calls=%d, want exactly two agent calls", len(observed))
	}
	for _, rec := range observed {
		if rec.LoopID != want.LoopID || rec.LoopName != want.LoopName || rec.ParentLoopID != want.ParentLoopID || rec.SessionID != want.SessionID || rec.RequestID != want.RequestID || rec.Purpose != "agent" || rec.PricingStatus != "priced" || rec.Outcome != "success" || rec.Role != "autonomous" {
			t.Errorf("incorrect canonical usage attribution: %+v", rec)
		}
	}
	var records int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_records WHERE request_id = ? AND session_id = ? AND conversation_id = ? AND loop_id = ? AND loop_name = ? AND parent_loop_id = ? AND purpose = 'agent' AND pricing_status = 'priced' AND outcome = 'success'`, want.RequestID, want.SessionID, conversationID, want.LoopID, want.LoopName, want.ParentLoopID).Scan(&records); err != nil || records != 2 {
		t.Fatalf("persisted attributed records=%d, want 2; error=%v", records, err)
	}
}

func TestMakeUsageRecordExplicitRoleWins(t *testing.T) {
	l, _, _ := newAccountingTestLoop(t)
	for _, tc := range []struct {
		name string
		req  Request
		role string
		task string
	}{
		{"ordinary", Request{}, "interactive", ""},
		{"auxiliary", Request{SkipContext: true}, "auxiliary", ""},
		{"scheduler hints", Request{RoutingFactors: map[string]string{"source": "scheduler", "task": "scheduled-name"}}, "scheduled", "scheduled-name"},
		{"explicit role", Request{SkipContext: true, UsageRole: "delegate"}, "delegate", ""},
		{"explicit overrides hints", Request{UsageRole: "autonomous", UsageTaskName: "named-task", RoutingFactors: map[string]string{"source": "scheduler", "task": "other"}}, "autonomous", "named-task"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := l.makeUsageRecord(&tc.req, usage.Record{Model: "claude-primary", InputTokens: 100})
			if got.Role != tc.role || got.TaskName != tc.task || got.Purpose != "agent" {
				t.Fatalf("role/task/purpose=%q/%q/%q, want %q/%q/agent", got.Role, got.TaskName, got.Purpose, tc.role, tc.task)
			}
		})
	}
}

func TestTriggerCompactionPreservesKnownAttribution(t *testing.T) {
	l, _, _ := newAccountingTestLoop(t)
	conversationID := "manual-compaction-conversation"
	sessionID := l.archiver.EnsureSession(conversationID)
	wantErr := errors.New("compaction failed")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var calls int
	l.compactor = readCheckCompactor{compact: func(callCtx context.Context, id string) error {
		calls++
		want := llm.Attribution{ConversationID: conversationID, SessionID: sessionID}
		if got := llm.AttributionFromContext(callCtx); got != want || id != conversationID {
			t.Errorf("manual compaction attribution=%+v id=%q, want %+v", got, id, want)
		}
		if got, ok := callCtx.Deadline(); !ok || got != deadline {
			t.Error("manual compaction lost the caller deadline")
		}
		return wantErr
	}}
	if err := l.TriggerCompaction(ctx, conversationID); !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("compaction error=%v calls=%d, want original error and one call", err, calls)
	}
}
