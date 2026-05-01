package api

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

type owuRecordingRunner struct {
	requests chan loop.Request
}

func (r *owuRecordingRunner) Run(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stream != nil {
		stream(agent.StreamEvent{Kind: agent.KindToken, Token: "hello"})
	}
	select {
	case r.requests <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &loop.Response{
		Content:       "ok",
		Model:         "test-model",
		InputTokens:   10,
		OutputTokens:  5,
		ContextWindow: 8192,
		RequestID:     "req-owu-1",
		ActiveTags:    []string{"owu"},
	}, nil
}

func TestOWUTrackerDispatchRoutesThroughTurnBuilder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := loop.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		registry.ShutdownAll(shutdownCtx)
	})

	runner := &owuRecordingRunner{requests: make(chan loop.Request, 1)}
	tracker, err := NewOWUTracker(ctx, registry, events.New(), runner, slog.Default())
	if err != nil {
		t.Fatalf("NewOWUTracker: %v", err)
	}

	var boundConvID string
	var bound *memory.ChannelBinding
	tracker.UseConversationBindingWriter(func(conversationID string, binding *memory.ChannelBinding) error {
		boundConvID = conversationID
		bound = binding.Clone()
		return nil
	})

	streamed := make(chan agent.StreamEvent, 1)
	req := &agent.Request{
		ConversationID: "owu-chat-1",
		Messages: []agent.Message{{
			Role:    "user",
			Content: "what is in this image?",
			Images:  []llm.ImageContent{{MediaType: "image/png", Data: "abc123"}},
		}},
		Hints: map[string]string{
			"channel": "ollama",
			"source":  "owu",
		},
	}

	resp, err := tracker.Dispatch(ctx, req, func(event agent.StreamEvent) {
		streamed <- event
	}, "Home Chat")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp == nil || resp.Content != "ok" || resp.RequestID != "req-owu-1" {
		t.Fatalf("response = %#v", resp)
	}

	var gotReq loop.Request
	select {
	case gotReq = <-runner.requests:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner request")
	}
	if gotReq.Hints["loop_id"] == "" {
		t.Fatal("loop_id hint is empty; request did not traverse OWU child loop turn preparation")
	}
	if gotReq.Hints["loop_name"] != "owu/Home Chat" {
		t.Fatalf("loop_name = %q, want owu/Home Chat", gotReq.Hints["loop_name"])
	}
	if gotReq.Hints["source"] != "owu" || gotReq.Hints["channel"] != "ollama" {
		t.Fatalf("hints = %#v", gotReq.Hints)
	}
	if gotReq.SkipTagFilter {
		t.Fatal("SkipTagFilter = true, want OWU loop tags to keep capability filtering active")
	}
	if len(gotReq.InitialTags) != 1 || gotReq.InitialTags[0] != "owu" {
		t.Fatalf("InitialTags = %#v, want [owu]", gotReq.InitialTags)
	}
	if gotReq.ChannelBinding == nil || gotReq.ChannelBinding.Channel != "owu" || !gotReq.ChannelBinding.IsOwner {
		t.Fatalf("ChannelBinding = %#v", gotReq.ChannelBinding)
	}
	if req.ChannelBinding == nil || req.ChannelBinding.Channel != "owu" || !req.ChannelBinding.IsOwner {
		t.Fatalf("original request ChannelBinding = %#v", req.ChannelBinding)
	}
	if boundConvID != "owu-chat-1" || bound == nil || bound.Channel != "owu" || !bound.IsOwner {
		t.Fatalf("bound conversation = %q %#v", boundConvID, bound)
	}
	if len(gotReq.Messages) != 1 || len(gotReq.Messages[0].Images) != 1 || gotReq.Messages[0].Images[0].MediaType != "image/png" {
		t.Fatalf("Messages = %#v", gotReq.Messages)
	}

	select {
	case event := <-streamed:
		if event.Kind != agent.KindToken || event.Token != "hello" {
			t.Fatalf("stream event = %#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream event")
	}
}
