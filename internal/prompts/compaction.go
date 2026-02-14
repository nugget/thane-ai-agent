package prompts

import (
	"fmt"
	"strings"
)

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

// workingMemorySection is appended to the compaction prompt when the agent
// has written working memory for this conversation. It provides the LLM with
// experiential context that should be preserved through compaction.
const workingMemorySection = `

## Agent Working Memory (self-authored context)
%s

Preserve the experiential texture from working memory in your summary — emotional
tone, relationship dynamics, and unresolved threads matter as much as facts.`

// CompactionPrompt returns the fully interpolated prompt for conversation
// compaction. The caller passes the formatted conversation text (role: content
// pairs) to be summarized. An optional working memory string, if non-empty,
// is appended so the summarizer preserves experiential context.
func CompactionPrompt(conversationText string, workingMemory string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(compactionTemplate, conversationText))
	if workingMemory != "" {
		sb.WriteString(fmt.Sprintf(workingMemorySection, workingMemory))
	}
	return sb.String()
}
