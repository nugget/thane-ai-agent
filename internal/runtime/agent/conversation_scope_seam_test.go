package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// turnScopeObservation is one prompt build's view of the turn-scoped
// context values a conversation-scoped provider depends on.
type turnScopeObservation struct {
	convID   string
	origin   string
	loopName string
}

// recordingScopeProvider records the conversation ID, message origin,
// and loop_name hint it observed on every invocation.
type recordingScopeProvider struct {
	seen []turnScopeObservation
}

func (p *recordingScopeProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketContinuity
}

func (p *recordingScopeProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	p.seen = append(p.seen, turnScopeObservation{
		convID:   tools.ConversationIDFromContext(ctx),
		origin:   tools.MessageOriginFromContext(ctx),
		loopName: tools.HintsFromContext(ctx)["loop_name"],
	})
	return "scoped", nil
}

// TestConversationScopeReachesContextProvidersEveryIteration is the
// conversation-scope sibling of the bindings seam test: OnIterationStart
// rebuilds the system prompt from the iteration context, so any value
// stamped only on the local promptCtx evaporates from iteration 1
// onward. For conversation ID that failure mode silently emptied every
// conversation-scoped provider (working memory, the message-channel
// timing and catalog blocks) on multi-iteration turns —
// ConversationIDFromContext falls back to "default" and the providers
// decline to render. This test drives a real two-iteration run and
// requires every prompt build to see the conversation ID, the turn's
// message origin, and the routing hints.
func TestConversationScopeReachesContextProvidersEveryIteration(t *testing.T) {
	// Two responses: the first with a tool call so the run continues to
	// a second iteration, which is where the rebuild happens.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:       "test-model",
				Message:     llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{newToolCall("c1", "base_tool")}},
				InputTokens: 100, OutputTokens: 10,
			},
			{
				Model:       "test-model",
				Message:     llm.Message{Role: "assistant", Content: "Done."},
				InputTokens: 100, OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"scoped": {Description: "Scoped", Tools: []string{"base_tool"}, Core: true},
	}, nil)

	provider := &recordingScopeProvider{}
	loop.RegisterTagContextProvider("scoped", provider)

	if _, err := loop.Run(context.Background(), &Request{
		Messages:       []Message{{Role: "user", Content: "go"}},
		ConversationID: "conv-scope-seam",
		InitialTags:    []string{"scoped"},
		RoutingFactors: map[string]string{"loop_name": "annunciator"},
		MessageOrigin:  memory.OriginWake,
	}, nil); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(provider.seen) < 2 {
		t.Fatalf("provider invoked %d times; need at least the initial build and one rebuild to observe the seam", len(provider.seen))
	}
	want := turnScopeObservation{convID: "conv-scope-seam", origin: memory.OriginWake, loopName: "annunciator"}
	for i, got := range provider.seen {
		if got != want {
			t.Errorf("prompt build %d saw %+v, want %+v — a turn-scoped value did not reach this rebuild", i, got, want)
		}
	}
	t.Logf("provider invoked %d times, all fully scoped", len(provider.seen))
}

// TestPerMessageOriginOverridesRequestStamp pins the storage half of
// mixed-provenance turns: a message carrying its own Origin is recorded
// with it, while its siblings inherit the request stamp. Without the
// override, a notify summary riding a Signal mailbox turn persists as
// channel contact and can masquerade as the previous contact in the
// conversation-timing narrative.
func TestPerMessageOriginOverridesRequestStamp(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:       "test-model",
				Message:     llm.Message{Role: "assistant", Content: "Done."},
				InputTokens: 100, OutputTokens: 10,
			},
		},
	}
	loop := buildTestLoop(mock, nil)
	mem := loop.memory.(*mockMem)

	if _, err := loop.Run(context.Background(), &Request{
		Messages: []Message{
			{Role: "user", Content: "notify summary", Origin: memory.OriginWake},
			{Role: "user", Content: "inbound message"},
		},
		ConversationID: "conv-mixed",
		MessageOrigin:  memory.OriginChannel,
	}, nil); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	got := map[string]string{}
	for _, m := range mem.msgs["conv-mixed"] {
		if m.Role == "user" {
			got[m.Content] = m.Origin
		}
	}
	if got["notify summary"] != memory.OriginWake {
		t.Errorf("override row origin = %q, want %q", got["notify summary"], memory.OriginWake)
	}
	if got["inbound message"] != memory.OriginChannel {
		t.Errorf("inheriting row origin = %q, want %q", got["inbound message"], memory.OriginChannel)
	}
}
