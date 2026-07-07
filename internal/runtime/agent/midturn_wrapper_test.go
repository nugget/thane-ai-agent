package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestPullInputWrapper_TagsMidTurnInput pins the feature's reason for existing:
// the Run-side PullInput wrapper must persist a mid-turn-merged message via
// AddMidTurnMessage (mid_turn=true), not the plain AddMessage. Without this,
// reverting the wrapper to AddMessage passes every other test while silently
// defeating #1230 (consumers fall back to substring-matching the arrival
// marker). The mock records which variant was used via Message.MidTurn.
func TestPullInputWrapper_TagsMidTurnInput(t *testing.T) {
	// Two text responses: the first turn finalizes, the closure poll injects a
	// mid-turn message and the turn continues, the second finalizes when the
	// poll comes back empty. A spare third guards against an extra poll.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "ok"}, InputTokens: 10, OutputTokens: 5},
			{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "done"}, InputTokens: 10, OutputTokens: 5},
			{Model: "test-model", Message: llm.Message{Role: "assistant", Content: "done"}, InputTokens: 10, OutputTokens: 5},
		},
	}
	loop := buildTestLoop(mock, nil)
	mm := loop.memory.(*mockMem)

	pulls := 0
	_, err := loop.Run(context.Background(), &Request{
		ConversationID: "conv-1",
		Messages:       []Message{{Role: "user", Content: "run the front-door diagnostic and report back"}},
		PullInput: func(context.Context) []llm.Message {
			pulls++
			if pulls == 1 {
				return []llm.Message{{Role: "user", Content: "mid-turn arrival"}}
			}
			return nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pulls == 0 {
		t.Fatal("PullInput was never polled — the wrapper never fired, so the assertion below is vacuous")
	}

	var found *memory.Message
	for i := range mm.msgs["conv-1"] {
		if mm.msgs["conv-1"][i].Content == "mid-turn arrival" {
			found = &mm.msgs["conv-1"][i]
			break
		}
	}
	if found == nil {
		t.Fatal("mid-turn message was not recorded in the conversation store")
	}
	if !found.MidTurn {
		t.Error("mid-turn message recorded without MidTurn=true — the wrapper used AddMessage, not AddMidTurnMessage")
	}
}
