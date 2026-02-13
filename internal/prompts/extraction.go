package prompts

import "fmt"

// factExtractionTemplate is the prompt sent to a local LLM to extract
// noteworthy facts from a single interaction. The three format verbs are
// user message, assistant response, and recent conversation transcript.
const factExtractionTemplate = `Extract noteworthy facts from this interaction that would be useful to
remember for future conversations. Focus on:
- User preferences (temperature, lighting, schedules, routines)
- Home layout (room names, device locations, areas)
- Personal information the user shared (names, relationships)
- Observed patterns (daily routines, habits)
- Device configuration knowledge (which devices are where)
- Architecture/system design knowledge

Valid categories: user, home, device, routine, preference, architecture

Return JSON only. Examples:

{"worth_persisting": true, "facts": [
  {"category": "preference", "key": "bedroom_temperature", "value": "Prefers 68°F at night", "confidence": 0.9}
]}

{"worth_persisting": true, "facts": [
  {"category": "user", "key": "partner_name", "value": "Partner is named Alex", "confidence": 0.85},
  {"category": "home", "key": "office_location", "value": "Office is upstairs, second door on the left", "confidence": 0.8}
]}

If nothing is worth remembering:
{"worth_persisting": false, "facts": []}

User: %s
Assistant: %s

Recent context:
%s

JSON:`

// FactExtractionPrompt returns the fully interpolated prompt for automatic
// fact extraction. The caller passes the current user message, assistant
// response, and a recent conversation transcript for additional context.
func FactExtractionPrompt(userMsg, assistantResp, transcript string) string {
	return fmt.Sprintf(factExtractionTemplate, userMsg, assistantResp, transcript)
}
