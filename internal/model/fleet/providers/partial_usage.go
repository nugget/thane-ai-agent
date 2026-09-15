package providers

import "github.com/nugget/thane-ai-agent/internal/model/llm"

// Partial failures can still carry provider-reported usage. Preserve only
// accounting metadata: incomplete assistant text or tool calls must not become
// executable output, and an error without reported counters proves no usage.
func usageOnlyResponse(response *llm.ChatResponse) *llm.ChatResponse {
	if response == nil || (response.InputTokens == 0 && response.OutputTokens == 0 &&
		response.CacheCreationInputTokens == 0 && response.CacheReadInputTokens == 0 &&
		response.CacheCreation5mInputTokens == 0 && response.CacheCreation1hInputTokens == 0) {
		return nil
	}
	partial := *response
	partial.Message = llm.Message{}
	partial.Done = false
	return &partial
}
