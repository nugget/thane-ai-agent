package prompts

import "fmt"

// compactionTemplate is the prompt sent to an LLM to summarize a conversation
// during memory compaction. The single format verb is the conversation text.
const compactionTemplate = `Summarize this conversation concisely. Focus on:
1. Key topics discussed
2. Decisions made or preferences expressed
3. Actions taken (tool calls, state changes)
4. Any open items or things to remember

Keep the summary under 500 words. Use bullet points.

Conversation:
%s

Summary:`

// CompactionPrompt returns the fully interpolated prompt for conversation
// compaction. The caller passes the formatted conversation text (role: content
// pairs) to be summarized.
func CompactionPrompt(conversationText string) string {
	return fmt.Sprintf(compactionTemplate, conversationText)
}
