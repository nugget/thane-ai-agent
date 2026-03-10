package openclaw

// MemoryFlushConfig holds settings for the pre-compaction memory flush.
type MemoryFlushConfig struct {
	// SoftThresholdTokens is subtracted from the compaction threshold
	// to determine when the flush fires. The flush runs when:
	// tokenCount >= compactionThreshold - SoftThresholdTokens.
	// Default: 4000 (matching OpenClaw v2026.2.9).
	SoftThresholdTokens int

	// Prompt is the user message sent for the flush turn.
	Prompt string

	// SystemPromptSuffix is appended to the system prompt for flush turns.
	SystemPromptSuffix string
}

// DefaultMemoryFlushConfig returns flush settings matching OpenClaw v2026.2.9.
func DefaultMemoryFlushConfig() MemoryFlushConfig {
	return MemoryFlushConfig{
		SoftThresholdTokens: 4000,
		Prompt: "Pre-compaction memory flush. " +
			"Store durable memories now (use memory/YYYY-MM-DD.md; create memory/ if needed). " +
			"If nothing to store, reply with NO_REPLY.",
		SystemPromptSuffix: "Pre-compaction memory flush turn. " +
			"The session is near auto-compaction; capture durable memories to disk. " +
			"You may reply, but usually NO_REPLY is correct.",
	}
}

// ShouldFlush returns true when context tokens are approaching the
// compaction threshold and a memory flush should run before the next turn.
//
// The formula matches OpenClaw v2026.2.9:
//
//	tokenCount >= compactionThreshold - softThresholdTokens
//
// where compactionThreshold = maxTokens * triggerRatio.
func ShouldFlush(tokenCount, compactionThreshold, softThresholdTokens int) bool {
	if tokenCount <= 0 || compactionThreshold <= 0 {
		return false
	}
	return tokenCount >= compactionThreshold-softThresholdTokens
}
