package loop

import (
	"context"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestLoopUsageAttributionOwnsRuntimeIdentity(t *testing.T) {
	var captured Request
	var attribution llm.Attribution
	runner := &callbackRunner{fn: func(ctx context.Context, req RunRequest) (*RunResponse, error) {
		captured = req
		attribution = llm.AttributionFromContext(ctx)
		return &RunResponse{Content: "completed"}, nil
	}}
	l, err := New(Config{
		Name: "actual-service", ParentID: "actual-parent", Operation: OperationService,
		Task: "Inspect the source.", MaxIter: 1, SleepMin: time.Millisecond,
		SleepMax: time.Millisecond, SleepDefault: time.Millisecond, Jitter: Float64Ptr(0),
		RoutingFactors: map[string]string{"source": "service-specific", "loop_id": "inherited-id", "loop_name": "inherited-name", "parent_loop_id": "inherited-parent"},
	}, Deps{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	l.requestOverride = Request{
		ConversationID: "arbitrary-conversation-without-loop-prefix",
		RoutingFactors: map[string]string{"loop_id": "override-id", "loop_name": "override-name", "parent_loop_id": "override-parent"},
	}
	if err := l.Start(llm.WithAttribution(t.Context(), llm.Attribution{
		LoopID: "spawning-loop", LoopName: "spawning-name", ParentLoopID: "spawning-parent",
		RequestID: "spawning-request", SessionID: "spawning-session", ConversationID: "spawning-conversation",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not complete")
	}
	for key, want := range map[string]string{
		"loop_id": l.ID(), "loop_name": "actual-service", "parent_loop_id": "actual-parent", "source": "service-specific",
	} {
		if got := captured.RoutingFactors[key]; got != want {
			t.Errorf("routing %s=%q, want %q", key, got, want)
		}
	}
	if captured.UsageRole != "autonomous" || captured.ConversationID != "arbitrary-conversation-without-loop-prefix" {
		t.Fatalf("usage attribution changed by conversation shape: role=%q conversation=%q", captured.UsageRole, captured.ConversationID)
	}
	wantAttribution := llm.Attribution{LoopID: l.ID(), LoopName: "actual-service", ParentLoopID: "actual-parent", ConversationID: captured.ConversationID}
	if attribution != wantAttribution {
		t.Errorf("runner attribution=%+v, want %+v", attribution, wantAttribution)
	}
}

func TestLoopUsageRolePreservesExecutionKind(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation Operation
		origin    string
		requested string
		override  string
		want      string
	}{
		{"service", OperationService, "", "", "", "autonomous"},
		{"event-driven", OperationEventDriven, "", "", "", "autonomous"},
		{"background", OperationBackgroundTask, "", "", "", "autonomous"},
		{"interactive", OperationRequestReply, "", "", "", ""},
		{"legacy interactive", "", "", "", "", ""},
		{"channel service", OperationService, memory.OriginChannel, "", "", ""},
		{"channel event-driven", OperationEventDriven, memory.OriginChannel, "", "", ""},
		{"delegate", OperationBackgroundTask, "", "delegate", "", "delegate"},
		{"scheduler", OperationRequestReply, "", "scheduler", "", "scheduler"},
		{"launch override", OperationService, "", "delegate", "custom", "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &Loop{id: "own-loop", config: Config{Name: "loop", Operation: tc.operation}, requestOverride: Request{UsageRole: tc.override}}
			req, err := l.prepareAgentTurnRequest(Request{UsageRole: tc.requested, MessageOrigin: tc.origin}, "conversation", false)
			if err != nil {
				t.Fatal(err)
			}
			if req.UsageRole != tc.want {
				t.Errorf("usage role=%q, want %q", req.UsageRole, tc.want)
			}
		})
	}
}

func TestHandlerOnlyLoopUsageAttribution(t *testing.T) {
	var got llm.Attribution
	l, err := New(Config{
		Name: "session-worker", ParentID: "supervisor", Operation: OperationService,
		MaxIter: 1, SleepMin: time.Millisecond, SleepMax: time.Millisecond,
		SleepDefault: time.Millisecond, Jitter: Float64Ptr(0),
		Handler: func(ctx context.Context, _ any) error {
			got = llm.AttributionFromContext(ctx)
			return nil
		},
	}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Start(llm.WithAttribution(t.Context(), llm.Attribution{
		LoopID: "parent", RequestID: "spawning-request", SessionID: "spawning-session",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("handler loop did not complete")
	}
	if got.LoopID != l.ID() || got.LoopName != "session-worker" || got.ParentLoopID != "supervisor" || got.ConversationID == "" || got.RequestID != "" || got.SessionID != "" {
		t.Fatalf("handler inherited incorrect attribution: %+v", got)
	}
}
