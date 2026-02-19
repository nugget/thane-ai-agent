package conditions

import (
	"fmt"
	"strings"
	"time"
)

// ContextUsageInfo holds the data needed to render the context usage line
// in the Current Conditions system prompt section.
type ContextUsageInfo struct {
	// Model is the default model name (e.g., "claude-opus-4-20250514").
	Model string
	// Routed indicates whether a router is configured (actual model may differ).
	Routed bool
	// TokenCount is the estimated token count of the active conversation.
	TokenCount int
	// ContextWindow is the context window size of the default model.
	ContextWindow int
	// MessageCount is the number of messages in the active conversation.
	MessageCount int
	// SessionStart is when the current session began. Zero value means unknown.
	SessionStart time.Time
	// CompactionCount is the number of compaction summaries in the conversation.
	CompactionCount int
}

// FormatContextUsage renders a single-line context usage string for the
// system prompt. Each segment is conditionally included based on available
// data. Returns an empty string only if no data is available at all.
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
	if !info.SessionStart.IsZero() {
		parts = append(parts, "session "+formatUptime(time.Since(info.SessionStart)))
	}

	// Compaction status.
	switch info.CompactionCount {
	case 0:
		parts = append(parts, "no compaction")
	case 1:
		parts = append(parts, "1 compaction")
	default:
		parts = append(parts, fmt.Sprintf("%d compactions", info.CompactionCount))
	}

	if len(parts) == 0 {
		return ""
	}
	return "**Context:** " + strings.Join(parts, " | ")
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
