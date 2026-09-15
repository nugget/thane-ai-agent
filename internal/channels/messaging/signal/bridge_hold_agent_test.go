package signal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// scriptedLLM plays the model behind a real agent.Loop. Each ChatStream
// call takes the next step; the last step repeats, so a step that
// returns an error keeps failing through the agent's retries.
type scriptedLLM struct {
	mu    sync.Mutex
	steps []scriptedStep
	tools [][]map[string]any
	msgs  [][]llm.Message
}

type scriptedStep struct {
	resp *llm.ChatResponse
	err  error
}

func (s *scriptedLLM) Chat(ctx context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return s.ChatStream(ctx, model, msgs, td, nil)
}

func (s *scriptedLLM) ChatStream(_ context.Context, _ string, msgs []llm.Message, td []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("scriptedLLM: no steps")
	}
	i := min(len(s.tools), len(s.steps)-1)
	s.tools = append(s.tools, td)
	s.msgs = append(s.msgs, append([]llm.Message(nil), msgs...))
	return s.steps[i].resp, s.steps[i].err
}

func (s *scriptedLLM) Ping(context.Context) error { return nil }

func (s *scriptedLLM) snapshot() ([][]map[string]any, [][]llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]map[string]any(nil), s.tools...), append([][]llm.Message(nil), s.msgs...)
}

func toolCallStep(id, name string, args map[string]any) scriptedStep {
	var call llm.ToolCall
	call.ID = id
	call.Function.Name = name
	call.Function.Arguments = args
	return scriptedStep{resp: &llm.ChatResponse{
		Model:   "test-model",
		Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
	}}
}

func textStep(content string) scriptedStep {
	return scriptedStep{resp: &llm.ChatResponse{
		Model:   "test-model",
		Message: llm.Message{Role: "assistant", Content: content},
	}}
}

// agentLoopRunner hands a loop request to a real agent.Loop, converting
// the fields a Signal turn uses the way internal/app's loopAdapter does.
// The adapter itself lives in internal/app, which imports this package,
// so it cannot run here; TestLoopAdapter_RuntimeToolSeesCallerContext
// pins its half of the context chain there.
type agentLoopRunner struct {
	loop *agent.Loop
}

func (r agentLoopRunner) Run(ctx context.Context, req loop.Request, _ loop.StreamCallback) (*loop.Response, error) {
	msgs := make([]agent.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = agent.Message{Origin: m.Origin, Role: m.Role, Content: m.Content}
	}
	runtimeTools := make([]*tools.Tool, 0, len(req.RuntimeTools))
	for _, t := range req.RuntimeTools {
		runtimeTools = append(runtimeTools, &tools.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
			Handler:     t.Handler,
			Core:        true,
		})
	}
	resp, err := r.loop.Run(ctx, &agent.Request{
		ConversationID:  req.ConversationID,
		Messages:        msgs,
		RoutingFactors:  req.RoutingFactors,
		MessageOrigin:   req.MessageOrigin,
		InitialTags:     req.InitialTags,
		RuntimeTags:     req.RuntimeTags,
		RuntimeTools:    runtimeTools,
		FallbackContent: req.FallbackContent,
	}, nil)
	if err != nil {
		return nil, err
	}
	return &loop.Response{
		Content:      resp.Content,
		Model:        resp.Model,
		FinishReason: resp.FinishReason,
		ToolsUsed:    resp.ToolsUsed,
		RequestID:    resp.RequestID,
		Iterations:   resp.Iterations,
	}, nil
}

func newScriptedAgentLoop(t *testing.T, model llm.Client) *agent.Loop {
	t.Helper()
	l, err := agent.NewLoop(agent.LoopOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Memory: memory.NewStore(100),
		LLM:    model,
		Model:  "test-model",
	})
	if err != nil {
		t.Fatalf("agent.NewLoop: %v", err)
	}
	return l
}

func toolDefNames(defs []map[string]any) []string {
	var names []string
	for _, d := range defs {
		if fn, ok := d["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// wakeSenderLoop starts the sender loop and delivers one core-attention
// notification to it, the way a requesting loop reaches the thread.
func wakeSenderLoop(t *testing.T, h *holdHarness, registry *loop.Registry) string {
	t.Helper()
	loopID, _, ok := h.bridge.ensureSenderLoop(context.Background(), holdTestSender)
	if !ok {
		t.Fatal("sender loop did not start")
	}
	if _, err := registry.NotifyLoop(context.Background(), loopID, coreAttentionNotify(t)); err != nil {
		t.Fatalf("NotifyLoop: %v", err)
	}
	return loopID
}

// TestBridge_WakeHoldThroughAgent drives a core-attention wake through
// the sender loop, the loop's request preparation, and a real agent.Loop
// whose model calls signal_hold_reply and then writes a stage direction.
// The hold only works if the recorder the bridge puts on the run context
// reaches the tool handler through the agent and its iterate engine; if
// the tool ran under any other context, the handler would refuse, and
// the stage direction would be sent.
func TestBridge_WakeHoldThroughAgent(t *testing.T) {
	const (
		reason = "answered garage-watch; sensor glitch, nothing for a person to do"
		stage  = "*(silence — nothing worth waking them for at 00:30)*"
	)
	model := &scriptedLLM{steps: []scriptedStep{
		toolCallStep("call-hold", HoldReplyToolName, map[string]any{"reason": reason}),
		textStep(stage),
	}}
	h, registry := startHoldSenderLoop(t, func(cfg *BridgeConfig) {
		cfg.Runner = agentLoopRunner{loop: newScriptedAgentLoop(t, model)}
	})
	loopID := wakeSenderLoop(t, h, registry)

	select {
	case <-h.notes.added:
	case <-time.After(5 * time.Second):
		t.Fatalf("no note recorded\nlogs:\n%s", h.logs.String())
	}

	if got := h.rpc.sentMessages(); len(got) != 0 {
		t.Errorf("Signal sends = %q, want none on a held wake", got)
	}
	_, notes, _ := h.notes.snapshot()
	if len(notes) != 1 || notes[0].Kind != replyNoteHeld || notes[0].Reason != reason ||
		!notes[0].FinalTextWithheld || !equalBoolPtr(notes[0].SignalMessageSent, boolPtr(false)) {
		t.Errorf("notes = %+v, want one held note with the reason, final_text_withheld, signal_message_sent=false", notes)
	}
	logs := h.logs.String()
	if !logLineWithAll(logs, []string{`"msg":"signal reply held"`, `"loop_id":"` + loopID + `"`, fmt.Sprintf(`"discarded_text_len":%d`, len(stage))}) {
		t.Errorf("held log missing, or not keyed to the loop with the discarded length\nlogs:\n%s", logs)
	}
	if strings.Contains(logs, "signal sending reply") {
		t.Errorf("bridge started a send on a held wake\nlogs:\n%s", logs)
	}

	defs, msgs := model.snapshot()
	if len(defs) == 0 || !containsString(toolDefNames(defs[0]), HoldReplyToolName) {
		t.Fatalf("first model call was not offered %s", HoldReplyToolName)
	}
	// The model's second call sees the hold's result, not the handler's
	// "not a wake turn" refusal.
	if len(msgs) < 2 || !anyMessageContains(msgs[1], `"status":"held"`) {
		t.Errorf("second model call did not carry the held tool result")
	}
}

// TestBridge_WakeTimeoutRecoveryThroughAgent runs a wake whose every model
// call times out through a real agent.Loop. The agent's retries run out
// and it returns its own timeout notice as ordinary content with a nil
// error; the bridge must recognise that text as the runtime's and send
// nothing. It also pins finishReasonTimeoutRecovery, which the signal
// package mirrors from the agent, against the value the agent reports.
// The agent's retry backoff is not configurable from here, so this test
// takes about six seconds.
func TestBridge_WakeTimeoutRecoveryThroughAgent(t *testing.T) {
	model := &scriptedLLM{steps: []scriptedStep{
		{err: fmt.Errorf("llm request: %w", context.DeadlineExceeded)},
	}}
	var finishReason string
	var finishMu sync.Mutex
	inner := agentLoopRunner{loop: newScriptedAgentLoop(t, model)}
	h, registry := startHoldSenderLoop(t, func(cfg *BridgeConfig) {
		cfg.Runner = runnerFunc(func(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error) {
			resp, err := inner.Run(ctx, req, stream)
			if resp != nil {
				finishMu.Lock()
				finishReason = resp.FinishReason
				finishMu.Unlock()
			}
			return resp, err
		})
	})
	wakeSenderLoop(t, h, registry)

	select {
	case <-h.notes.added:
	case <-time.After(20 * time.Second):
		t.Fatalf("no note recorded\nlogs:\n%s", h.logs.String())
	}

	finishMu.Lock()
	gotFinish := finishReason
	finishMu.Unlock()
	if gotFinish != finishReasonTimeoutRecovery {
		t.Fatalf("agent FinishReason = %q, want %q (finishReasonTimeoutRecovery mirrors the agent's value)", gotFinish, finishReasonTimeoutRecovery)
	}
	if got := h.rpc.sentMessages(); len(got) != 0 {
		t.Errorf("Signal sends = %q, want none: the timeout notice is the runtime's, not a reply", got)
	}
	_, notes, _ := h.notes.snapshot()
	if len(notes) != 1 || notes[0].Kind != replyNoteNotSent || !strings.Contains(notes[0].Cause, "timed out") || !notes[0].FinalTextWithheld {
		t.Errorf("notes = %+v, want one not-sent note naming the timeout", notes)
	}
	if logs := h.logs.String(); !logLineWithAll(logs, []string{`"msg":"signal wake reply not sent`, `"finish_reason":"timeout_recovery"`}) {
		t.Errorf("not-sent log missing the finish reason\nlogs:\n%s", logs)
	}
}

type runnerFunc func(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error)

func (f runnerFunc) Run(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error) {
	return f(ctx, req, stream)
}

func anyMessageContains(msgs []llm.Message, want string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}
