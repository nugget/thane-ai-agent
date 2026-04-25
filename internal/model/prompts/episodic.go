package prompts

// EpisodicHistoryFraming returns the framing text prepended to archived
// conversation history. It warns the model that timestamps and time
// references are historical, not current.
func EpisodicHistoryFraming() string {
	return "⚠️ **ARCHIVED HISTORY** — The messages below are from PAST sessions, " +
		"NOT the current conversation. Timestamps and time references within " +
		"these messages reflect when they were originally said, not the current " +
		"time. Current time is in the \"Current Conditions\" block above."
}
