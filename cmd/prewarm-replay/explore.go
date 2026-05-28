package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// rawSearch bypasses the prewarm-provider abstraction and calls
// ArchiveStore.Search directly. This is the same code path
// archive_search-the-tool uses, just from outside the agent. The
// goal is exploratory: see what the underlying search actually
// returns before any prewarm trimming or context bucket
// formatting, so we can reason about retrieval quality
// independently of how the prewarm provider presents it.
func rawSearch(arc *memory.ArchiveStore, query string, limit int, excludeConvIDs []string) ([]rawHit, error) {
	results, err := arc.Search(memory.SearchOptions{
		Query: query,
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}

	exclude := make(map[string]struct{}, len(excludeConvIDs))
	for _, id := range excludeConvIDs {
		if id != "" {
			exclude[id] = struct{}{}
		}
	}

	now := time.Now()
	out := make([]rawHit, 0, len(results))
	for i, r := range results {
		if _, drop := exclude[r.Match.ConversationID]; drop {
			continue
		}
		out = append(out, rawHit{
			Rank:           i + 1,
			Role:           r.Match.Role,
			ConversationID: r.Match.ConversationID,
			SessionID:      r.Match.SessionID,
			AgeDelta:       promptfmt.FormatDeltaOnly(r.Match.Timestamp, now),
			Content:        truncateContent(r.Match.Content, 100),
			FullContent:    r.Match.Content,
			Highlight:      r.Highlight,
		})
	}
	return out, nil
}

// rawHit is the structured per-result view rawSearch emits.
// Roles, age, and conversation_id are surfaced as explicit fields
// (not buried in metadata) so experiments can group / count /
// compare by them programmatically. Content is truncated for
// display; FullContent retains the full message text so precision
// metrics like containsPhrase can scan the whole row.
type rawHit struct {
	Rank           int    `json:"rank"`
	Role           string `json:"role"`
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id"`
	AgeDelta       string `json:"age_delta"`
	Content        string `json:"content"`
	FullContent    string `json:"full_content,omitempty"`
	Highlight      string `json:"highlight,omitempty"`
}

// roleHistogram counts hits by role for quick summary stats. Used
// when running a query alongside an excluded-conversation variant
// to surface "how much of the result set was self-reference?"
func roleHistogram(hits []rawHit) map[string]int {
	out := map[string]int{}
	for _, h := range hits {
		out[h.Role]++
	}
	return out
}

// formatRawHitsHuman prints a compact per-hit table plus a role
// histogram, suitable for terminal output during exploration.
func formatRawHitsHuman(label string, query string, hits []rawHit, excluded []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "━━━ %s (%d hits): %q ━━━\n", label, len(hits), query)
	if len(excluded) > 0 {
		fmt.Fprintf(&sb, "  excluded conversations: %s\n", strings.Join(excluded, ", "))
	}
	for _, h := range hits {
		conv := h.ConversationID
		if len(conv) > 28 {
			conv = conv[:25] + "..."
		}
		fmt.Fprintf(&sb, "  [%d] role=%-10s t=%-10s conv=%-28s  %s\n",
			h.Rank, h.Role, h.AgeDelta, conv, h.Content)
	}
	hist := roleHistogram(hits)
	if len(hist) > 0 {
		var keys []string
		for k := range hist {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&sb, "  role histogram: ")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s=%d", k, hist[k])
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func truncateContent(s string, max int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}
