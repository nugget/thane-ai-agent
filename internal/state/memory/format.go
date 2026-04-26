package memory

import (
	"encoding/json"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// SessionView is the JSON-facing projection of an archived session.
// Field shape is stable across calls — empty strings and zero values
// are emitted explicitly rather than omitted, so the model can rely on
// schema invariants when comparing entries across turns.
//
// Started/Ended are signed-second deltas via [promptfmt.FormatDeltaOnly]
// (e.g., "-7200s"). Ended is the empty string when the session is still
// active; Duration is 0 in the same case.
type SessionView struct {
	ID             string   `json:"id"`
	ConversationID string   `json:"conversation_id"`
	Started        string   `json:"started"`
	Ended          string   `json:"ended"`
	Duration       int      `json:"duration"`
	Messages       int      `json:"messages"`
	Title          string   `json:"title"`
	Tags           []string `json:"tags"`
	Summary        string   `json:"summary"`
}

// MessageView is the JSON-facing projection of an archived message.
// T is a signed-second delta via [promptfmt.FormatDeltaOnly]. SessionID
// is included when callers want the model to be able to chain into
// archive_session_transcript for fuller context.
type MessageView struct {
	T         string `json:"t"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	SessionID string `json:"session_id,omitempty"`
}

// SearchResultView is the JSON-facing projection of an archive search hit.
// Match is the message that matched; ContextBefore/ContextAfter are the
// surrounding messages in chronological order, bounded by the configured
// silence threshold.
type SearchResultView struct {
	Match         MessageView   `json:"match"`
	ContextBefore []MessageView `json:"context_before"`
	ContextAfter  []MessageView `json:"context_after"`
	Highlight     string        `json:"highlight"`
}

// FormatSessionsList renders sessions as JSON suitable for tool output
// or system-prompt context blocks. Always emits a non-nil "sessions"
// array and a "truncated" boolean so the model can detect cap-driven
// truncation without parsing prose.
func FormatSessionsList(sessions []*Session, now time.Time, truncated bool) []byte {
	views := make([]SessionView, 0, len(sessions))
	for _, s := range sessions {
		views = append(views, sessionToView(s, now))
	}
	out := struct {
		Sessions  []SessionView `json:"sessions"`
		Truncated bool          `json:"truncated"`
	}{
		Sessions:  views,
		Truncated: truncated,
	}
	data, _ := json.Marshal(out)
	return data
}

// FormatRecentMessages renders messages as JSON for tool output or
// system-prompt context blocks. Each entry includes a delta timestamp
// and the originating session ID, so the model can chain into
// archive_session_transcript when it wants more context around a turn.
func FormatRecentMessages(messages []Message, now time.Time, truncated bool) []byte {
	views := make([]MessageView, 0, len(messages))
	for _, m := range messages {
		views = append(views, messageToView(m, now, true))
	}
	out := struct {
		Messages  []MessageView `json:"messages"`
		Truncated bool          `json:"truncated"`
	}{
		Messages:  views,
		Truncated: truncated,
	}
	data, _ := json.Marshal(out)
	return data
}

// FormatSearchResults renders archive search hits as JSON. Each result
// carries the matched message plus the surrounding context window in
// chronological order. SessionID lives on the match (and is implied for
// surrounding context, which always belongs to the same session).
func FormatSearchResults(results []SearchResult, now time.Time, truncated bool) []byte {
	views := make([]SearchResultView, 0, len(results))
	for _, r := range results {
		match := messageToView(r.Match, now, false)
		match.SessionID = r.SessionID
		views = append(views, SearchResultView{
			Match:         match,
			ContextBefore: messagesToViews(r.ContextBefore, now, false),
			ContextAfter:  messagesToViews(r.ContextAfter, now, false),
			Highlight:     r.Highlight,
		})
	}
	out := struct {
		Results   []SearchResultView `json:"results"`
		Truncated bool               `json:"truncated"`
	}{
		Results:   views,
		Truncated: truncated,
	}
	data, _ := json.Marshal(out)
	return data
}

func sessionToView(s *Session, now time.Time) SessionView {
	view := SessionView{
		ID:             s.ID,
		ConversationID: s.ConversationID,
		Started:        promptfmt.FormatDeltaOnly(s.StartedAt, now),
		Messages:       s.MessageCount,
		Title:          s.Title,
		Tags:           append([]string{}, s.Tags...),
		Summary:        s.Summary,
	}
	if s.EndedAt != nil {
		view.Ended = promptfmt.FormatDeltaOnly(*s.EndedAt, now)
		view.Duration = int(s.EndedAt.Sub(s.StartedAt).Seconds())
	}
	return view
}

func messageToView(m Message, now time.Time, includeSessionID bool) MessageView {
	view := MessageView{
		T:       promptfmt.FormatDeltaOnly(m.Timestamp, now),
		Role:    m.Role,
		Content: m.Content,
	}
	if includeSessionID {
		view.SessionID = m.SessionID
	}
	return view
}

func messagesToViews(messages []Message, now time.Time, includeSessionID bool) []MessageView {
	views := make([]MessageView, 0, len(messages))
	for _, m := range messages {
		views = append(views, messageToView(m, now, includeSessionID))
	}
	return views
}
