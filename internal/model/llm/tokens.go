package llm

// EstimateTokens returns a rough token count estimate for English text.
// Rule of thumb: ~4 characters per token.
func EstimateTokens(text string) int {
	return len(text) / 4
}
