package prompts

import "fmt"

// TrustZoneGuidance returns analysis guidance text for a given media
// trust zone. This is shared between the feed polling wake prompt
// (where the zone comes from the feed subscription) and one-off
// media_transcript calls (where the caller passes it explicitly).
// Returns empty string for unrecognized zones — callers should default
// to "unknown" before calling.
func TrustZoneGuidance(trustZone string) string {
	switch trustZone {
	case "trusted":
		return "Extract facts directly with source attribution. Full analysis depth."
	case "known":
		return "Extract as claims requiring corroboration. Summarize key points."
	case "unknown":
		return "Topics and high-level insights only. No fact extraction."
	default:
		return ""
	}
}

// mediaFeedPollWakeTemplate is prepended to the feed poller's wake
// message to give the agent context on how to handle new feed entries.
// Format verbs: %s (trust zone guidance bullets), %s (content summary).
const mediaFeedPollWakeTemplate = `New content detected from followed feeds. Review and act on it.

Each entry shows the feed's trust zone in brackets (e.g., [trusted], [known], [unknown]).
Adapt your analysis depth based on the trust zone:

%s

For each new entry:
1. Check the feed's trust zone shown in brackets after the feed name
2. If the content looks worthwhile, use media_transcript to fetch and analyze it
3. Notify the owner about noteworthy new content with a brief summary

Use your judgment — not every new video or podcast episode needs attention.
Prioritize content that aligns with known interests and preferences.

%s`

// MediaFeedPollWakePrompt returns the feed poll wake prompt with the
// poller's content summary injected. The trust-zone guidance bullets
// are generated from TrustZoneGuidance to keep a single source of truth.
func MediaFeedPollWakePrompt(contentSummary string) string {
	guidance := fmt.Sprintf(
		"- **[trusted]**: %s\n- **[known]**: %s\n- **[unknown]**: %s",
		TrustZoneGuidance("trusted"),
		TrustZoneGuidance("known"),
		TrustZoneGuidance("unknown"),
	)
	return fmt.Sprintf(mediaFeedPollWakeTemplate, guidance, contentSummary)
}
