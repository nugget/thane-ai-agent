package modeleval

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestEvaluateToolCallsIgnoresParallelOrderAndIDs(t *testing.T) {
	t.Parallel()

	expected := llm.Message{ToolCalls: []llm.ToolCall{
		toolCall("reference-a", "first", map[string]any{"value": 1}),
		toolCall("reference-b", "second", map[string]any{"value": 2}),
	}}
	actual := llm.Message{ToolCalls: []llm.ToolCall{
		toolCall("candidate-x", "second", map[string]any{"value": 2}),
		toolCall("candidate-y", "first", map[string]any{"value": 1}),
	}}
	score := Evaluate(expected, actual)
	if !score.DecisionMatch || !score.ToolNamesMatch || !score.ToolArgumentsMatch || !score.ReferenceMatch {
		t.Fatalf("score = %#v", score)
	}
}

func TestEvaluateDistinguishesNamesAndArguments(t *testing.T) {
	t.Parallel()

	expected := llm.Message{ToolCalls: []llm.ToolCall{toolCall("a", "search", map[string]any{"q": "one"})}}
	wrongArgs := Evaluate(expected, llm.Message{ToolCalls: []llm.ToolCall{toolCall("b", "search", map[string]any{"q": "two"})}})
	if !wrongArgs.ToolNamesMatch || wrongArgs.ToolArgumentsMatch || wrongArgs.ReferenceMatch {
		t.Fatalf("wrong-args score = %#v", wrongArgs)
	}
	wrongDecision := Evaluate(expected, llm.Message{Content: "I would search."})
	if wrongDecision.DecisionMatch || wrongDecision.ReferenceMatch {
		t.Fatalf("wrong-decision score = %#v", wrongDecision)
	}
}

func toolCall(id, name string, arguments map[string]any) llm.ToolCall {
	call := llm.ToolCall{ID: id}
	call.Function.Name = name
	call.Function.Arguments = arguments
	return call
}
