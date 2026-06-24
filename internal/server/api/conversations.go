package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// conversationKindPattern bounds the kind filter to a safe identifier so the
// value can be used as a LIKE prefix without surprises. We deliberately do
// NOT validate against a fixed family allowlist — id prefixes (loop, sched,
// metacog, owu, signal, email, wake, mqtt, media, ...) grow as new producers
// appear, and a hardcoded list drifts out of date. An unknown kind simply
// matches nothing.
var conversationKindPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// handleConversationList serves GET /v1/conversations: a filtered, sorted,
// keyset-paginated view of conversation summaries. It returns cheap summaries
// (no message content) and bounds the result, so cost scales with the page
// and filter selectivity rather than the all-time conversation corpus.
func (s *Server) handleConversationList(w http.ResponseWriter, r *http.Request) {
	if s.memoryStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "memory store not configured")
		return
	}

	q := r.URL.Query()
	var query memory.ConversationQuery

	if ids := splitCSV(q.Get("ids")); len(ids) > 0 {
		if len(ids) > 200 {
			s.errorResponse(w, http.StatusBadRequest, "ids: at most 200 allowed")
			return
		}
		query.IDs = ids
	}

	if kinds := splitCSV(q.Get("kind")); len(kinds) > 0 {
		for _, k := range kinds {
			if !conversationKindPattern.MatchString(k) {
				s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("kind %q: must match [a-z0-9_]+", k))
				return
			}
		}
		query.Kinds = kinds
	}

	query.Channel = strings.TrimSpace(q.Get("channel"))
	query.ContactID = strings.TrimSpace(q.Get("contact"))
	query.Address = strings.TrimSpace(q.Get("address"))
	query.Q = strings.TrimSpace(q.Get("q"))

	for _, tf := range []struct {
		name string
		dst  **time.Time
	}{
		{"updated_after", &query.UpdatedAfter},
		{"updated_before", &query.UpdatedBefore},
		{"created_after", &query.CreatedAfter},
		{"created_before", &query.CreatedBefore},
	} {
		t, err := parseTimeOrAgo(q.Get(tf.name))
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", tf.name, err))
			return
		}
		*tf.dst = t
	}

	minM, err := parseIntPtr(q.Get("min_messages"))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "min_messages: "+err.Error())
		return
	}
	maxM, err := parseIntPtr(q.Get("max_messages"))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "max_messages: "+err.Error())
		return
	}
	if minM != nil && maxM != nil && *minM > *maxM {
		s.errorResponse(w, http.StatusBadRequest, "min_messages must not exceed max_messages")
		return
	}
	query.MinMessages, query.MaxMessages = minM, maxM

	query.Sort = "updated_at"
	if v := strings.TrimSpace(q.Get("sort")); v != "" {
		switch v {
		case "updated_at", "created_at", "message_count":
			query.Sort = v
		default:
			s.errorResponse(w, http.StatusBadRequest, "sort: must be updated_at, created_at, or message_count")
			return
		}
	}

	query.Order = "desc"
	if v := strings.TrimSpace(q.Get("order")); v != "" {
		switch v {
		case "asc", "desc":
			query.Order = v
		default:
			s.errorResponse(w, http.StatusBadRequest, "order: must be asc or desc")
			return
		}
	}

	limit := parseIntParam(r, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	query.Limit = limit

	if c := strings.TrimSpace(q.Get("cursor")); c != "" {
		cur, err := decodeConvCursor(c)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "cursor: "+err.Error())
			return
		}
		if cur.Sort != query.Sort || cur.Order != query.Order {
			s.errorResponse(w, http.StatusBadRequest, "cursor: sort/order mismatch; restart pagination")
			return
		}
		query.Cursor = cur
	}

	page, err := s.memoryStore.QueryConversations(query)
	if err != nil {
		s.logger.Error("conversation query failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "conversation query failed")
		return
	}

	conversations := page.Conversations
	if conversations == nil {
		conversations = []memory.ConversationSummary{}
	}
	var nextCursor any // JSON null on the last page
	if page.NextCursor != nil {
		token, err := encodeConvCursor(page.NextCursor)
		if err != nil {
			s.logger.Error("encode conversation cursor failed", "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "conversation query failed")
			return
		}
		nextCursor = token
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"conversations": conversations,
		"count":         len(conversations),
		"total":         page.Total,
		"next_cursor":   nextCursor,
	}, s.logger)
}

// splitCSV splits a comma-separated query value into trimmed, de-duplicated,
// non-empty tokens (preserving first-seen order). Empty input yields nil.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// parseIntPtr parses an optional non-negative integer query value. Empty
// returns (nil, nil) so callers can distinguish "absent" from an explicit 0 —
// which parseIntParam cannot, since it floors both to its default.
func parseIntPtr(s string) (*int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("must be an integer")
	}
	if n < 0 {
		return nil, fmt.Errorf("must not be negative")
	}
	return &n, nil
}

// parseTimeOrAgo parses an optional time bound expressed either as an RFC3339
// timestamp or as a positive Go duration meaning "that long ago" (e.g. 1h,
// 24h — so updated_after=1h means "active in the last hour"). Empty returns
// (nil, nil).
func parseTimeOrAgo(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return nil, fmt.Errorf("duration must be positive (interpreted as time ago)")
		}
		t := time.Now().UTC().Add(-d)
		return &t, nil
	}
	// database.ParseTimestamp accepts RFC3339/RFC3339Nano (and the SQLite TEXT
	// shapes) — use it rather than a raw time.Parse, per AGENTS.md.
	if t, err := database.ParseTimestamp(s); err == nil {
		return &t, nil
	}
	return nil, fmt.Errorf("must be an RFC3339 timestamp or a duration like 1h or 24h")
}

// encodeConvCursor renders a keyset cursor as an opaque base64url token.
func encodeConvCursor(c *memory.ConvCursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeConvCursor parses a cursor token. Any malformed token is a client
// error (400), never a silent reset to page one.
func decodeConvCursor(s string) (*memory.ConvCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("malformed cursor")
	}
	var c memory.ConvCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("malformed cursor")
	}
	if c.Sort == "" || c.Order == "" || c.ID == "" || c.V == "" {
		return nil, fmt.Errorf("malformed cursor")
	}
	return &c, nil
}
