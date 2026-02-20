package conditions

import (
	"fmt"
	"strings"
	"time"
)

// ContextUsageInfo holds the data needed to render the context usage line
// in the Current Conditions system prompt section. All fields are
// pre-computed by the caller so that formatting is deterministic
// and free of I/O.
type ContextUsageInfo struct {
	// Model is the default model name (e.g., "claude-opus-4-20250514").
	// The "(routed)" suffix is appended when Routed is true, signaling
	// that the router may select a different model for this turn.
	Model string
	// Routed indicates whether a router is configured (actual model may differ).
	Routed bool
	// TokenCount is the estimated token count of the active conversation.
	TokenCount int
	// ContextWindow is the context window size of the default model.
	ContextWindow int
	// MessageCount is the number of messages in the active conversation.
	MessageCount int
	// SessionAge is how long the current session has been active.
	// Zero means unknown or no active session.
	SessionAge time.Duration
	// CompactionCount is the number of compaction summaries in the conversation.
	CompactionCount int
	// ConversationID is the active conversation identifier.
	ConversationID string
	// SessionID is the active session identifier (short form, 8 chars).
	SessionID string
	// RequestID is the per-turn request identifier.
	RequestID string
}

// FormatContextUsage renders a single-line context usage string for the
// system prompt. Each segment is conditionally included based on available
// data. Returns an empty string when no data fields are populated.
func FormatContextUsage(info ContextUsageInfo) string {
	var parts []string

	// Model segment.
	if info.Model != "" {
		m := info.Model
		if info.Routed {
			m += " (routed)"
		}
		parts = append(parts, m)
	}

	// Token usage segment.
	if info.ContextWindow > 0 {
		pct := float64(info.TokenCount) / float64(info.ContextWindow) * 100
		parts = append(parts, fmt.Sprintf("%s/%s tokens (%.1f%%)",
			formatNumber(info.TokenCount),
			formatNumber(info.ContextWindow),
			pct))
	}

	// Message count.
	if info.MessageCount > 0 {
		parts = append(parts, fmt.Sprintf("%d msgs", info.MessageCount))
	}

	// Session duration.
	if info.SessionAge > 0 {
		parts = append(parts, "session "+formatUptime(info.SessionAge))
	}

	// Compaction status — only shown when at least one other segment
	// contributed data, so a completely empty info struct returns "".
	if len(parts) > 0 {
		switch info.CompactionCount {
		case 0:
			parts = append(parts, "no compaction")
		case 1:
			parts = append(parts, "1 compaction")
		default:
			parts = append(parts, fmt.Sprintf("%d compactions", info.CompactionCount))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	result := "**Context:** " + strings.Join(parts, " | ")

	// Append IDs line when at least one identifier is available.
	var ids []string
	if info.ConversationID != "" {
		ids = append(ids, "conv:"+truncateID(info.ConversationID))
	}
	if info.SessionID != "" {
		ids = append(ids, "session:"+truncateID(info.SessionID))
	}
	if info.RequestID != "" {
		ids = append(ids, "req:"+info.RequestID)
	}
	if len(ids) > 0 {
		result += "\n**IDs:** " + strings.Join(ids, " | ")
	}

	return result
}

// truncateID returns the last 8 characters of an ID for display.
// Returns the full ID if shorter than 8 characters.
func truncateID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// formatNumber formats an integer with comma separators (e.g., 200000 → "200,000").
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var sb strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		sb.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}
