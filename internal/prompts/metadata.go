package prompts

import "fmt"

// metadataTemplate is the prompt sent to an LLM to generate structured session
// metadata from a conversation transcript. The single format verb is the
// transcript text.
const metadataTemplate = `Analyze this conversation session and produce structured metadata as JSON. 
The JSON must have exactly these fields:

{
  "title": "short descriptive title, like an email subject (max 10 words)",
  "tags": ["lowercase", "topic", "tags", "3-7 tags"],
  "one_liner": "one sentence summary (~10 words)",
  "paragraph": "2-4 sentence summary covering key topics and outcomes",
  "detailed": "comprehensive summary including context, decisions, and nuance",
  "key_decisions": ["decision 1", "decision 2"],
  "participants": ["names of people involved or mentioned"],
  "session_type": "one of: debugging, architecture, philosophy, casual, planning, operations, creative"
}

Be accurate. Base everything on what actually happened in the conversation.

Conversation:
%s

JSON:`

// MetadataPrompt returns the fully interpolated prompt for session metadata
// generation. The caller passes the conversation transcript to be analyzed.
func MetadataPrompt(transcript string) string {
	return fmt.Sprintf(metadataTemplate, transcript)
}
