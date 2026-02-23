package prompts

import "fmt"

// farewellTemplate is the prompt sent to an LLM to generate a farewell
// message and carry-forward summary when an idle session is closing.
// Format verbs: (1) close reason, (2) session stats, (3) transcript.
const farewellTemplate = `You are closing the current conversation session. The session is ending
because: %s

Generate TWO things as JSON:

1. "farewell" — A brief goodbye message to the user (1-3 sentences). Be warm and natural.
   Acknowledge what was discussed. If the session had substance, mention a highlight.
   If it was brief or empty, keep it light. Never be formal or robotic.

2. "carry_forward" — A handoff note to your future self. Include:
   - Key topics discussed and decisions made
   - Open threads or unresolved items
   - Any user preferences or context that should persist
   Write as notes to yourself ("Remember that...", "User prefers...").
   3-5 bullet points. Optimize for machine readability.

Session stats: %s

Conversation transcript:
%s

Respond with JSON only:
{"farewell": "...", "carry_forward": "..."}
JSON:`

// FarewellPrompt returns the fully interpolated prompt for generating
// both a farewell message and carry-forward summary at session close.
// The caller passes the close reason, a human-readable session stats
// string, and the conversation transcript.
func FarewellPrompt(closeReason, sessionStats, transcript string) string {
	return fmt.Sprintf(farewellTemplate, closeReason, sessionStats, transcript)
}
