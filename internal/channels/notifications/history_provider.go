package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// HistoryProvider injects a compact JSON summary of recent
// notifications into the system prompt. This gives the agent (and
// autonomous loops like metacognitive) awareness of what has already
// been sent, preventing duplicate notifications.
//
// Implements [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider.
type HistoryProvider struct {
	records *RecordStore
	nowFunc func() time.Time
	window  time.Duration
	limit   int
	logger  *slog.Logger
}

// HistoryProviderConfig holds dependencies for [NewHistoryProvider].
type HistoryProviderConfig struct {
	Records *RecordStore  // optional; nil disables history output
	Window  time.Duration // lookback window; default 6h
	Limit   int           // max entries; default 30
	Logger  *slog.Logger  // optional; defaults to slog.Default
}

// NewHistoryProvider creates a provider that queries the notification
// record store and formats recent sends for system prompt injection.
func NewHistoryProvider(cfg HistoryProviderConfig) *HistoryProvider {
	window := cfg.Window
	if window <= 0 {
		window = 6 * time.Hour
	}
	limit := cfg.Limit
	if limit <= 0 {
		limit = 30
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &HistoryProvider{
		records: cfg.Records,
		nowFunc: time.Now,
		window:  window,
		limit:   limit,
		logger:  logger,
	}
}

// historySummary is the JSON structure for a single notification in the
// system prompt output.
type historySummary struct {
	Sent      string `json:"sent"`               // delta timestamp: "-3600s"
	Channel   string `json:"channel"`            // provider: "ha_push", "signal"
	Recipient string `json:"recipient"`          // contact name
	Title     string `json:"title,omitempty"`    // notification title
	Snippet   string `json:"message_snip"`       // first ~100 chars of message
	Source    string `json:"source"`             // originator: "metacognitive", "signal/+1234"
	Kind      string `json:"kind"`               // "fire_and_forget" or "actionable"
	Status    string `json:"status,omitempty"`   // HITL status for actionable
	Response  string `json:"response,omitempty"` // action taken for responded
}

// maxSnippetRunes is the maximum length of the message snippet shown
// in the system prompt. Truncated with "…" suffix.
const maxSnippetRunes = 100

// TagContext returns a compact JSON summary of recent notifications
// for injection into the system prompt. Returns empty string when
// no recent notifications exist.
func (p *HistoryProvider) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	if p.records == nil {
		return "", nil
	}

	now := p.nowFunc()
	since := now.Add(-p.window)

	records, err := p.records.Recent(since, p.limit)
	if err != nil {
		p.logger.Warn("failed to query notification history", "error", err)
		return "", nil
	}
	if len(records) == 0 {
		return "", nil
	}

	summaries := make([]historySummary, len(records))
	for i, r := range records {
		s := historySummary{
			Sent:      promptfmt.FormatDeltaOnly(r.CreatedAt, now),
			Channel:   r.Channel,
			Recipient: r.Recipient,
			Title:     r.Title,
			Snippet:   truncateRunes(r.Message, maxSnippetRunes),
			Source:    r.Source,
			Kind:      r.Kind,
		}
		// Enrich actionable notifications with HITL lifecycle state.
		if r.Kind == KindActionable {
			s.Status = r.Status
			if r.Status == StatusResponded {
				s.Response = r.ResponseAction
			}
		}
		summaries[i] = s
	}

	data, err := json.Marshal(summaries)
	if err != nil {
		p.logger.Warn("failed to marshal notification history", "error", err)
		return "", nil
	}

	return "### Recent Notifications\n\n" + string(data) + "\n", nil
}

// truncateRunes truncates s to at most n runes, appending "…" if
// truncated. Uses rune-aware slicing per CLAUDE.md guidance.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
