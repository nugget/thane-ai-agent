package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// sequenceLLM returns its responses in order.
type sequenceLLM struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	next      int
}

func (s *sequenceLLM) Chat(ctx context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return s.ChatStream(ctx, model, msgs, td, nil)
}

func (s *sequenceLLM) ChatStream(context.Context, string, []llm.Message, []map[string]any, llm.StreamCallback) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.responses) {
		return nil, fmt.Errorf("sequenceLLM: no response for call %d", s.next)
	}
	resp := s.responses[s.next]
	s.next++
	return resp, nil
}

func (s *sequenceLLM) Ping(context.Context) error { return nil }

type callerContextKey struct{}

// TestLoopAdapter_RuntimeToolSeesCallerContext pins the context chain a
// request-scoped runtime tool depends on: a value the caller puts on the
// context it hands loopAdapter.Run must reach the tool's handler through
// compileLoopRuntimeTools, the agent loop, and the iterate engine. The
// Signal bridge's signal_hold_reply works this way: the bridge installs
// its hold recorder on the run context, and a handler run under any
// other context refuses the hold, so the wake's final text is sent.
func TestLoopAdapter_RuntimeToolSeesCallerContext(t *testing.T) {
	const toolName = "probe_caller_context"
	var call llm.ToolCall
	call.ID = "call-probe"
	call.Function.Name = toolName
	call.Function.Arguments = map[string]any{}
	client := &sequenceLLM{responses: []*llm.ChatResponse{
		{Model: "test-model", Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}},
		{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "done"}},
	}}
	agentLoop, err := agent.NewLoop(agent.LoopOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Memory: memory.NewStore(100),
		LLM:    client,
		Model:  "test-model",
	})
	if err != nil {
		t.Fatalf("agent.NewLoop: %v", err)
	}

	seen := make(chan any, 1)
	ctx := context.WithValue(context.Background(), callerContextKey{}, "installed by the caller")
	resp, err := (&loopAdapter{agentLoop: agentLoop}).Run(ctx, looppkg.Request{
		ConversationID: "conv-probe",
		Messages:       []looppkg.Message{{Role: "user", Content: "probe"}},
		RuntimeTools: []looppkg.RuntimeTool{{
			Name:        toolName,
			Description: "Report the caller's context value.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Handler: func(ctx context.Context, _ map[string]any) (string, error) {
				select {
				case seen <- ctx.Value(callerContextKey{}):
				default:
				}
				return `{"ok":true}`, nil
			},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.ToolsUsed[toolName] != 1 {
		t.Fatalf("ToolsUsed = %v, want the runtime tool called once", resp.ToolsUsed)
	}
	select {
	case got := <-seen:
		if got != "installed by the caller" {
			t.Errorf("handler saw caller value %v, want the value put on Run's context", got)
		}
	default:
		t.Fatal("runtime tool handler never ran")
	}
}
