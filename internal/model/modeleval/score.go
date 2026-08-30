package modeleval

import (
	"encoding/json"
	"sort"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Score describes deterministic agreement between a reference decision and a
// candidate response. Text quality is intentionally not judged semantically.
type Score struct {
	ExpectedKind       string `json:"expected_kind"`
	ActualKind         string `json:"actual_kind"`
	DecisionMatch      bool   `json:"decision_match"`
	ToolNamesMatch     bool   `json:"tool_names_match"`
	ToolArgumentsMatch bool   `json:"tool_arguments_match"`
	ReferenceMatch     bool   `json:"reference_match"`
}

// Evaluate compares response kind and tool calls while ignoring provider-
// assigned correlation IDs and the order of parallel calls.
func Evaluate(expected llm.Message, actual llm.Message) Score {
	expectedKind := decisionKind(expected)
	actualKind := decisionKind(actual)
	score := Score{
		ExpectedKind:  expectedKind,
		ActualKind:    actualKind,
		DecisionMatch: expectedKind == actualKind,
	}
	if expectedKind != "tool_calls" || actualKind != "tool_calls" {
		score.ToolNamesMatch = len(expected.ToolCalls) == 0 && len(actual.ToolCalls) == 0
		score.ToolArgumentsMatch = score.ToolNamesMatch
		score.ReferenceMatch = score.DecisionMatch
		return score
	}

	expectedNames, expectedCalls := canonicalCalls(expected.ToolCalls)
	actualNames, actualCalls := canonicalCalls(actual.ToolCalls)
	score.ToolNamesMatch = equalStrings(expectedNames, actualNames)
	score.ToolArgumentsMatch = equalStrings(expectedCalls, actualCalls)
	score.ReferenceMatch = score.DecisionMatch && score.ToolArgumentsMatch
	return score
}

func decisionKind(message llm.Message) string {
	if len(message.ToolCalls) > 0 {
		return "tool_calls"
	}
	if message.Content != "" {
		return "text"
	}
	return "empty"
}

func canonicalCalls(calls []llm.ToolCall) ([]string, []string) {
	names := make([]string, 0, len(calls))
	full := make([]string, 0, len(calls))
	for _, call := range calls {
		arguments, _ := json.Marshal(call.Function.Arguments)
		names = append(names, call.Function.Name)
		full = append(full, call.Function.Name+"\x00"+string(arguments))
	}
	sort.Strings(names)
	sort.Strings(full)
	return names, full
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
