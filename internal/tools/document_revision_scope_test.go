package tools

import (
	"context"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestDocumentRevisionScopeFallsBackToLoopRuntimeKeys pins the seam that
// lets a wake's output-context render and its tool calls share one
// receipt scope: the render runs on the loop runtime's context (its own
// loop and conversation keys), the tool call on the agent's (this
// package's keys), and both must resolve to the same string. This
// package's keys win when present, so a tool call that names a different
// conversation is not silently re-scoped to the wake.
func TestDocumentRevisionScopeFallsBackToLoopRuntimeKeys(t *testing.T) {
	t.Parallel()

	runtimeCtx := looppkg.WithConversationIDForTest(looppkg.WithLoopIDForTest(context.Background(), "loop-1"), "loop-office-3-99")
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "runtime keys only", ctx: runtimeCtx, want: "loop:loop-1/conversation:loop-office-3-99"},
		{name: "runtime loop only", ctx: looppkg.WithLoopIDForTest(context.Background(), "loop-1"), want: "loop:loop-1"},
		{name: "tool-call keys match the runtime's", ctx: WithConversationID(WithLoopID(context.Background(), "loop-1"), "loop-office-3-99"), want: "loop:loop-1/conversation:loop-office-3-99"},
		{name: "tool-call keys win", ctx: WithConversationID(WithLoopID(runtimeCtx, "loop-2"), "conv-other"), want: "loop:loop-2/conversation:conv-other"},
		{name: "default conversation defers to the runtime", ctx: WithConversationID(runtimeCtx, "default"), want: "loop:loop-1/conversation:loop-office-3-99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DocumentRevisionScope(tt.ctx); got != tt.want {
				t.Fatalf("DocumentRevisionScope() = %q, want %q", got, tt.want)
			}
		})
	}
}
