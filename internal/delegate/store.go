package delegate

import (
	"github.com/nugget/thane-ai-agent/internal/llm"
)

// ExtractToolsCalled scans a message history and returns a map of tool
// names to invocation counts.
func ExtractToolsCalled(messages []llm.Message) map[string]int {
	counts := make(map[string]int)
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "" {
				counts[tc.Function.Name]++
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}
