package prompts

// TrustZoneGuidance returns analysis guidance text for a given media
// trust zone. Shared between the built-in media-default-handler loop's
// task description and one-off media_transcript calls (where the
// caller passes it explicitly). Returns empty string for unrecognized
// zones — callers should default to "unknown" before calling.
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
