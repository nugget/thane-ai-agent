package memory

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// FitPrefix returns the largest count k in [0, n] such that render(k)
// is within byteCap. render must produce monotonically non-decreasing
// output as k grows. Used by prefix-fit clipping (e.g., search results,
// where the tail entries are lower-relevance and are the right ones to
// drop). Output is always rendered with truncated=true when k < n.
func FitPrefix(n, byteCap int, render func(k int) []byte) []byte {
	if n == 0 {
		return render(0)
	}
	full := render(n)
	if len(full) <= byteCap {
		return full
	}
	low, high := 0, n
	for low < high {
		mid := (low + high + 1) / 2
		if len(render(mid)) <= byteCap {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return render(low)
}

// FitSuffix returns the smallest count k in [0, n] such that render(k)
// is within byteCap. render must produce monotonically non-increasing
// output as k grows (k is the number of items dropped from the front).
// Used by suffix-fit clipping where older entries are dropped first to
// preserve the most-recent tail.
func FitSuffix(n, byteCap int, render func(drop int) []byte) []byte {
	if n == 0 {
		return render(0)
	}
	full := render(0)
	if len(full) <= byteCap {
		return full
	}
	low, high := 0, n
	for low < high {
		mid := (low + high) / 2
		if len(render(mid)) <= byteCap {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return render(low)
}

// SessionView is the JSON-facing projection of an archived session.
// Field shape is stable across calls — empty strings and zero values
// are emitted explicitly rather than omitted, so the model can rely on
// schema invariants when comparing entries across turns.
//
// Started/Ended are signed-second deltas via [promptfmt.FormatDeltaOnly]
// (e.g., "-7200s"). Ended is the empty string when the session is still
// active; DurationSeconds is 0 in the same case.
type SessionView struct {
	ID              string   `json:"id"`
	ConversationID  string   `json:"conversation_id"`
	Started         string   `json:"started"`
	Ended           string   `json:"ended"`
	DurationSeconds int      `json:"duration_seconds"`
	Messages        int      `json:"messages"`
	Title           string   `json:"title"`
	Tags            []string `json:"tags"`
	Summary         string   `json:"summary"`
}

// maxMessageContentBytes caps per-message content in JSON output. Beyond
// this size, content is truncated and ContentTruncated is set to true so
// the model knows there is more available via archive_session_transcript.
// The cap protects against single huge messages blowing through the
// tool's overall byte budget and forcing the handler to drop everything.
const maxMessageContentBytes = 2000

// maxMessageMetadataValueBytes bounds each transport metadata value in
// archive-facing JSON and stored-history headers. Metadata is small
// provenance, not corpus; bounding it prevents unusual channel labels from
// dominating byte-capped archive tool output.
const maxMessageMetadataValueBytes = 256

const maxMessageMetadataKeyBytes = 64

// MessageView is the JSON-facing projection of an archived message.
// T is a signed-second delta via [promptfmt.FormatDeltaOnly]. SessionID
// is always emitted (empty string when unknown) so the model sees a
// stable schema across calls. ContentTruncated signals when Content was
// clipped to [maxMessageContentBytes]. Metadata carries transport
// provenance separated from the literal message body when a known channel
// envelope is detected. Metadata string values are individually capped
// so one malformed envelope cannot force byte-capped tools to drop all
// messages; keys in this projection are fixed literals, not capped.
//
// Metadata is map[string]any so nested objects can appear under
// metadata.sender for Signal-like envelopes that resolve a structured
// {address, name} pair. The model reads metadata.sender.address as a
// stable handle that cross-references the Session Origin Context block.
// metadata.sender.name is included only for cross-conversation archive
// search hits where the model may need to identify a third-party sender
// (see [FormatSearchResults]); the live-message path
// ([FormatRecentMessages]) emits address-only to avoid duplicating PII
// the origin context already carries.
type MessageView struct {
	T                string         `json:"t"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ContentTruncated bool           `json:"content_truncated"`
	SessionID        string         `json:"session_id"`
	Metadata         map[string]any `json:"metadata,omitempty"`
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
	// Score is the relevance signal (higher = better), rounded for
	// model consumption. MatchType ("phrase" | "terms") records which
	// pass produced the hit — scores are comparable within a MatchType
	// but not across the two passes. Results are already ordered
	// best-first regardless.
	Score     float64 `json:"score"`
	MatchType string  `json:"match_type,omitempty"`
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
		views = append(views, messageToView(m, now))
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

// maxSearchContextPerSide caps the number of context messages emitted
// on each side of a search match. The archive's context-expansion
// query bounds context by silence-gap and a generous per-direction
// max (default 50), which made each search result potentially huge:
// 1 match + up to 100 context × maxMessageContentBytes can blow well
// past the tool's overall byte cap. With unbounded per-result size,
// the byte-cap fitter could not seat even a single hit and would
// return `{"results":[],"truncated":true}` — empty output, signal
// lost. This per-side cap keeps each rendered result bounded so the
// fitter can always fit at least one hit; the model can pull the
// fuller window via archive_session_transcript when it wants more.
const maxSearchContextPerSide = 5

// FormatSearchResults renders archive search hits as JSON. Each result
// carries the matched message plus the surrounding context window in
// chronological order. SessionID is emitted on every message; context
// messages may belong to a different session than the match because
// context expansion is bounded by silence gaps, not session edges.
// Context lists are trimmed to the [maxSearchContextPerSide] messages
// closest to the match on each side — for context_before that's the
// last N, for context_after the first N.
//
// Single-surface form: used internally by [FormatMultiKindResults]
// and (still) by paths that haven't migrated to the multi-surface
// envelope. New callers should prefer FormatMultiKindResults so the
// model sees the same shape from prewarm and tool calls alike.
func FormatSearchResults(results []SearchResult, now time.Time, truncated bool) []byte {
	views := buildSearchResultViews(results, now)
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

// roundScoreSignificant rounds a relevance score to sigFigs significant
// figures so the signal survives regardless of magnitude. BM25 values
// can be tiny on small or sparse indexes (a one-row exact phrase scores
// around 1e-06 after negation); fixed-decimal rounding would flatten
// those to zero and blank the model-facing score field, whereas
// significant-figure rounding keeps the leading digits at any scale.
func roundScoreSignificant(v float64, sigFigs int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	power := float64(sigFigs) - math.Ceil(math.Log10(math.Abs(v)))
	mag := math.Pow(10, power)
	return math.Round(v*mag) / mag
}

// buildSearchResultViews is the projection step shared by the
// single-surface and multi-surface formatters. Keeps the sender-
// projection / context-trimming logic in one place.
func buildSearchResultViews(results []SearchResult, now time.Time) []SearchResultView {
	views := make([]SearchResultView, 0, len(results))
	for _, r := range results {
		// Search hits cross conversation boundaries — the matching
		// session may not be the current run's origin, so the model
		// cannot rely on Session Origin Context to resolve who the
		// sender is. Emit the richer sender projection here so
		// metadata.sender.name is available as best-effort fallback.
		match := messageToViewWithSender(r.Match, now, senderRich)
		if match.SessionID == "" {
			match.SessionID = r.SessionID
		}
		views = append(views, SearchResultView{
			Match:         match,
			ContextBefore: searchContextViews(tailMessages(r.ContextBefore, maxSearchContextPerSide), now),
			ContextAfter:  searchContextViews(headMessages(r.ContextAfter, maxSearchContextPerSide), now),
			Highlight:     r.Highlight,
			Score:         roundScoreSignificant(r.Score, 4),
			MatchType:     r.MatchType,
		})
	}
	return views
}

// SessionMatchView is the JSON-facing projection of a sessions_fts
// hit. Compact compared to a raw-message search result — the
// summary is already distilled, so no context window is needed.
// session_id pairs with archive_session_transcript for full
// drilldown when the match looks worth reading. Tags is a structured
// list of strings, matching SessionMatch.Tags and Session.Tags shape
// (caller doesn't have to parse JSON).
type SessionMatchView struct {
	SessionID      string   `json:"session_id"`
	ConversationID string   `json:"conversation_id"`
	Started        string   `json:"started"`
	Ended          string   `json:"ended,omitempty"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Highlight      string   `json:"highlight,omitempty"`
}

// WorkingMemoryMatchView is the JSON-facing projection of a
// working_memory_fts hit. Working memory is one row per conversation
// — the conversation_id identifies which thread's living
// distillation matched, and content carries the full snapshot.
type WorkingMemoryMatchView struct {
	ConversationID string `json:"conversation_id"`
	Updated        string `json:"updated"`
	Content        string `json:"content"`
	Highlight      string `json:"highlight,omitempty"`
}

// FormatMultiKindResults renders the unified multi-surface search
// envelope: raw-message hits with context windows, session summary
// hits, and working-memory hits, all in one document.
//
// Every kind is always present in the JSON (as an array, possibly
// empty) so the model sees a consistent shape — an empty list is the
// explicit signal "no hits on this surface," distinguishable from
// "this surface wasn't queried." Truncated propagates from the
// raw-message search (distilled surfaces don't truncate within a
// single call).
func FormatMultiKindResults(b *SearchBundle, now time.Time, truncated bool) []byte {
	if b == nil {
		b = &SearchBundle{}
	}

	msgViews := buildSearchResultViews(b.Messages, now)
	if msgViews == nil {
		msgViews = []SearchResultView{}
	}

	sessViews := make([]SessionMatchView, 0, len(b.Sessions))
	for _, s := range b.Sessions {
		v := SessionMatchView{
			SessionID:      s.SessionID,
			ConversationID: s.ConversationID,
			Started:        promptfmt.FormatDeltaOnly(s.StartedAt, now),
			Title:          s.Title,
			Summary:        s.Summary,
			Tags:           append([]string(nil), s.Tags...),
			Highlight:      s.Highlight,
		}
		if !s.EndedAt.IsZero() {
			v.Ended = promptfmt.FormatDeltaOnly(s.EndedAt, now)
		}
		sessViews = append(sessViews, v)
	}

	wmViews := make([]WorkingMemoryMatchView, 0, len(b.WorkingMemory))
	for _, w := range b.WorkingMemory {
		wmViews = append(wmViews, WorkingMemoryMatchView{
			ConversationID: w.ConversationID,
			Updated:        promptfmt.FormatDeltaOnly(w.UpdatedAt, now),
			Content:        w.Content,
			Highlight:      w.Highlight,
		})
	}

	out := struct {
		Messages       []SearchResultView       `json:"messages"`
		Sessions       []SessionMatchView       `json:"sessions"`
		WorkingMemory  []WorkingMemoryMatchView `json:"working_memory"`
		Truncated      bool                     `json:"truncated"`
		TotalEstimated int                      `json:"total_estimated,omitempty"`
	}{
		Messages:       msgViews,
		Sessions:       sessViews,
		WorkingMemory:  wmViews,
		Truncated:      truncated || b.Truncated,
		TotalEstimated: b.TotalMessages,
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
		view.DurationSeconds = int(s.EndedAt.Sub(s.StartedAt).Seconds())
	}
	return view
}

// archiveAssistantRole is the display label used for assistant
// messages in archive-derived JSON the model reads. The neutral
// "assistant" role is anodyne and reads as a third party — but
// archived assistant messages are *the model's own past output*.
// Surfacing them as "past you" gives the model a personal hook into
// its own history. This is a cosmetic label only: stored roles, live
// conversation history, API responses, and tool wire formats are
// unaffected.
const archiveAssistantRole = "past you"

// senderProjection controls how much of the parsed sender appears in
// metadata.sender for a rendered MessageView.
type senderProjection int

const (
	// senderMinimal emits metadata.sender = {address}. Use for
	// messages that belong to the current run's session origin —
	// the model already has the contact's name in the Session
	// Origin Context block and duplicating it as message metadata
	// is gratuitous PII.
	senderMinimal senderProjection = iota

	// senderRich emits metadata.sender = {address, name}. Use for
	// cross-conversation archive search hits where the model may
	// need the name to identify a third-party sender it does not
	// have origin context for.
	senderRich
)

func messageToView(m Message, now time.Time) MessageView {
	return messageToViewWithSender(m, now, senderMinimal)
}

// messageToViewWithSender is the parameterized form used when the
// caller knows whether the projection is for a live message or for a
// cross-conversation search hit.
func messageToViewWithSender(m Message, now time.Time, sp senderProjection) MessageView {
	body, metadata := splitTransportEnvelope(m.Content)
	if sp == senderMinimal {
		dropSenderName(metadata)
	}
	content, truncated := clipContent(body, maxMessageContentBytes)
	role := m.Role
	if role == "assistant" {
		role = archiveAssistantRole
	}
	return MessageView{
		T:                promptfmt.FormatDeltaOnly(m.Timestamp, now),
		Role:             role,
		Content:          content,
		ContentTruncated: truncated,
		SessionID:        m.SessionID,
		Metadata:         metadata,
	}
}

// dropSenderName removes the "name" subfield from a
// metadata.sender sub-object so the minimal projection emits only the
// stable address. If sender ends up empty after the drop, the whole
// "sender" key is removed.
func dropSenderName(metadata map[string]any) {
	sender, ok := metadata["sender"].(map[string]any)
	if !ok {
		return
	}
	delete(sender, "name")
	if len(sender) == 0 {
		delete(metadata, "sender")
	}
}

// StoredHistoryMessage is a provider-neutral message entry built from
// stored conversation history. Role is the provider role to use in
// messages[]. Content is the model-facing body for that role.
type StoredHistoryMessage struct {
	Role    string
	Content string
}

// FormatStoredHistoryMessage renders one memory row for provider
// messages[]. It keeps provider-native roles as the primary role signal
// and separates curation/transport metadata from literal corpus content.
func FormatStoredHistoryMessage(m Message, now time.Time) (StoredHistoryMessage, bool) {
	role := strings.TrimSpace(m.Role)
	if role == "" && strings.TrimSpace(m.Content) == "" {
		return StoredHistoryMessage{}, false
	}

	providerRole := role
	kind := "stored conversation history"
	var metadata []metadataPart
	switch role {
	case "user", "assistant":
		// Native provider role carries the speaker identity.
	case "system":
		providerRole = "assistant"
		kind = "stored conversation memory note"
		metadata = append(metadata,
			metadataPart{key: "original_role", value: "system"},
			metadataPart{key: "not_active_instruction", value: "true"},
		)
	case "tool":
		providerRole = "assistant"
		kind = "stored historical tool result"
		metadata = append(metadata,
			metadataPart{key: "original_role", value: "tool"},
			metadataPart{key: "context_only", value: "true"},
		)
	default:
		providerRole = "user"
		kind = "stored historical message"
		if role != "" {
			metadata = append(metadata, metadataPart{key: "original_role", value: role})
		}
	}
	if providerRole == "" {
		providerRole = "user"
	}

	body, transportMetadata := splitTransportEnvelope(m.Content)
	if !m.Timestamp.IsZero() && !now.IsZero() {
		metadata = append(metadata, metadataPart{key: "age_delta", value: promptfmt.FormatDeltaOnly(m.Timestamp, now)})
	}
	metadata = append(metadata, storedHistoryTransportMetadataParts(transportMetadata)...)

	return StoredHistoryMessage{
		Role:    providerRole,
		Content: renderStoredHistoryContent(kind, metadata, body),
	}, true
}

type metadataPart struct {
	key   string
	value string
}

func renderStoredHistoryContent(kind string, metadata []metadataPart, content string) string {
	parts := []string{kind}
	for _, part := range metadata {
		key := sanitizeMetadataToken(part.key, maxMessageMetadataKeyBytes)
		value := sanitizeMetadataToken(part.value, maxMessageMetadataValueBytes)
		if key == "" || value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	var sb strings.Builder
	sb.WriteString("[")
	sb.WriteString(strings.Join(parts, "; "))
	sb.WriteString("]\n")
	sb.WriteString("<conversation_message>\n")
	sb.WriteString(content)
	sb.WriteString("\n</conversation_message>")
	return sb.String()
}

func sanitizeMetadataValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, ";", ",")
	return strings.Join(strings.Fields(value), " ")
}

func sanitizeMetadataToken(value string, maxBytes int) string {
	value = sanitizeMetadataValue(value)
	value, _ = clipContent(value, maxBytes)
	return value
}

// storedHistoryTransportMetadataParts flattens the structured
// transport metadata into the key=value pairs that the stored-history
// text envelope carries. Sender appears only in group conversations
// (solo DMs already have the sender obvious from the channel context),
// and is rendered using the stable address — falling back to the
// display name only when no address is available.
func storedHistoryTransportMetadataParts(metadata map[string]any) []metadataPart {
	if len(metadata) == 0 {
		return nil
	}
	parts := make([]metadataPart, 0, 3)
	if channel, _ := metadata["channel"].(string); channel != "" {
		parts = append(parts, metadataPart{key: "channel", value: channel})
	}
	if groupID, _ := metadata["group_id"].(string); groupID != "" {
		if sender := flattenSenderForText(metadata); sender != "" {
			parts = append(parts, metadataPart{key: "sender", value: sender})
		}
		parts = append(parts, metadataPart{key: "group_id", value: groupID})
	}
	return parts
}

// flattenSenderForText picks the most useful single string from a
// structured metadata.sender for the stored-history flat-text path.
// Prefers the stable address; falls back to the display name when no
// address resolved. Returns "" when neither is present.
func flattenSenderForText(metadata map[string]any) string {
	sender, ok := metadata["sender"].(map[string]any)
	if !ok || len(sender) == 0 {
		return ""
	}
	if address, _ := sender["address"].(string); address != "" {
		return address
	}
	if name, _ := sender["name"].(string); name != "" {
		return name
	}
	return ""
}

var signalMessageEnvelopeRE = regexp.MustCompile(`(?s)^Signal message from (.+?)(?: in group (.+?))? \[ts:([^\]]+)\]:\n\n(.*)$`)

// signalSenderRE matches a Signal-envelope sender of the shape
// "Display Name (+E164)" and captures the name and the address
// separately. Bare phone numbers (no display name) and bare names
// (no address) are handled out-of-band by [parseSignalSender].
var signalSenderRE = regexp.MustCompile(`^(.+?)\s+\((\+[0-9]+)\)\s*$`)

// parseSignalSender splits a Signal envelope's sender slug into a
// best-effort (name, address) pair. The four observed shapes are
// handled deterministically:
//
//	"David McNett (+15124232707)"  → ("David McNett", "+15124232707")
//	"+15124232707"                 → ("", "+15124232707")
//	"David McNett"                 → ("David McNett", "")
//	""                             → ("", "")
//
// Output values are not yet length-clipped — the caller clips when it
// stores them into the metadata map, so the clip-limit is consistent
// across all metadata fields.
func parseSignalSender(raw string) (name, address string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if m := signalSenderRE.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	}
	if strings.HasPrefix(raw, "+") {
		return "", raw
	}
	return raw, ""
}

// splitTransportEnvelope detects a known transport envelope on a
// message's stored Content and returns the literal body plus a
// structured metadata map suitable for both the JSON projection path
// (where metadata.sender is a nested {address, name?} sub-object) and
// the stored-history flat-text envelope path (which flattens via
// [storedHistoryTransportMetadataParts]).
//
// Returns the original content and nil metadata when no envelope
// matches — the caller treats that as a non-channel message.
func splitTransportEnvelope(content string) (string, map[string]any) {
	match := signalMessageEnvelopeRE.FindStringSubmatch(content)
	if match == nil {
		return content, nil
	}
	name, address := parseSignalSender(strings.TrimSpace(match[1]))

	metadata := map[string]any{}
	setMetadataString(metadata, "channel", "signal")
	setMetadataString(metadata, "transport_ts", strings.TrimSpace(match[3]))
	if group := strings.TrimSpace(match[2]); group != "" {
		setMetadataString(metadata, "group_id", group)
	}

	sender := map[string]any{}
	setMetadataString(sender, "address", address)
	setMetadataString(sender, "name", name)
	if len(sender) > 0 {
		metadata["sender"] = sender
	}

	if len(metadata) == 0 {
		return match[4], nil
	}
	return match[4], metadata
}

// setMetadataString writes a sanitized, clipped string value to a
// metadata map. The sanitizer in [sanitizeMetadataToken] trims
// whitespace, collapses runs of whitespace to single spaces,
// converts newlines and semicolons to safer characters, then clips
// to maxMessageMetadataValueBytes; values that come out empty after
// that pass are dropped so the caller can rely on "key present
// implies non-empty string." It does not strip arbitrary
// non-printable runes — values that survive the whitespace pass and
// are non-empty are written through as-is.
func setMetadataString(m map[string]any, key, value string) {
	v := sanitizeMetadataToken(value, maxMessageMetadataValueBytes)
	if v == "" {
		return
	}
	m[key] = v
}

// searchContextViews renders context messages around a search hit
// with the rich sender projection (matching the hit itself), since
// the search result may belong to a different conversation than the
// current run's origin.
func searchContextViews(messages []Message, now time.Time) []MessageView {
	views := make([]MessageView, 0, len(messages))
	for _, m := range messages {
		views = append(views, messageToViewWithSender(m, now, senderRich))
	}
	return views
}

// tailMessages returns the last n messages from msgs, or all of them
// when the slice is shorter than n. Used to keep the context window
// closest to the match (search result before-context is in
// chronological order, so the closest message is at the tail).
func tailMessages(msgs []Message, n int) []Message {
	if n <= 0 || len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

// headMessages returns the first n messages from msgs, or all of them
// when the slice is shorter than n. Used for after-context, where the
// chronologically-nearest message is at the head.
func headMessages(msgs []Message, n int) []Message {
	if n <= 0 || len(msgs) <= n {
		return msgs
	}
	return msgs[:n]
}

// clipContent clips s to at most maxBytes, returning the clipped
// string and a flag indicating whether truncation occurred. The cut
// respects UTF-8 boundaries so the result is always valid UTF-8.
func clipContent(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	for maxBytes > 0 && (s[maxBytes]&0xC0) == 0x80 {
		maxBytes--
	}
	return s[:maxBytes], true
}
