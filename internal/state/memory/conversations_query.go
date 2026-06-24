package memory

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// Conversation timestamp columns are TEXT in mixed shapes: the message
// write path binds a bare time.Now() (local-zone, space-separated, see
// [database.SQLiteTimestampLayout]) while the metadata write path stores
// RFC3339Nano UTC. Raw-column ordering and keyset comparison are therefore
// silently wrong across zones. These strftime expressions collapse every
// shape to one fixed-width, millisecond-precision, UTC, lexically-sortable
// form; the matching expression indexes (idx_conversations_{updated,created}_norm
// in schema.go) make ORDER BY + keyset seeks index-driven. normConvTime
// renders a Go bound in the byte-identical form so cursor comparisons round-trip.
const (
	convUpdatedNorm = `strftime('%Y-%m-%dT%H:%M:%fZ', c.updated_at)`
	convCreatedNorm = `strftime('%Y-%m-%dT%H:%M:%fZ', c.created_at)`
	convCountExpr   = `(SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.status = 'active')`

	// convSummarySelect is the inner column list shared by both query forms
	// (flat and outer-wrapped). Aliased so the outer min/max or message_count
	// wrapper can reference the columns; scanning is positional either way.
	convSummarySelect = "c.id AS id, " + convCountExpr + " AS message_count, " +
		convCreatedNorm + " AS created_norm, " + convUpdatedNorm + " AS updated_norm, c.metadata AS metadata"
)

func normConvTime(t time.Time) string {
	// Round to milliseconds to match SQLite strftime('%f'), which ROUNDS the
	// fractional seconds; a plain .000 format truncates, which would disagree
	// with the column at the sub-millisecond edge of a range bound.
	return t.UTC().Round(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

// ConversationQuery describes a filtered, sorted, keyset-paginated query
// over the conversation store. The handler validates and normalizes inputs;
// the store also defends itself — defaulting empty sort/order, clamping limit,
// and rejecting an unknown sort — so it is safe to call directly (e.g. tests).
type ConversationQuery struct {
	IDs   []string // exact id membership (c.id IN ...)
	Kinds []string // id-prefix families WITHOUT the trailing '-' (e.g. "signal")

	Channel   string // metadata.channel_binding.channel
	ContactID string // metadata.channel_binding.contact_id
	Address   string // metadata.channel_binding.address
	Q         string // substring over id + contact_name + address (metadata only)

	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
	CreatedAfter  *time.Time
	CreatedBefore *time.Time

	MinMessages *int
	MaxMessages *int

	Sort   string // "updated_at" (default) | "created_at" | "message_count"
	Order  string // "asc" | "desc" (default)
	Limit  int    // clamped to [1,200]
	Cursor *ConvCursor
}

// ConvCursor is an opaque keyset position: the sort value of the last row
// of a page plus its id tiebreak. The handler encodes/decodes it to a token;
// the store consumes the decoded struct.
type ConvCursor struct {
	Sort  string `json:"s"`
	Order string `json:"d"`
	V     string `json:"v"` // normalized time string, or message count as a decimal string
	ID    string `json:"i"`
}

// ConversationSummary is a lightweight conversation descriptor — identity,
// active message count, timestamps, and channel binding — with no message
// content. message_count is the TRUE active count (uncapped), unlike the
// legacy list which reported min(active, maxMessages).
type ConversationSummary struct {
	ID             string          `json:"id"`
	MessageCount   int             `json:"message_count"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ChannelBinding *ChannelBinding `json:"channel_binding,omitempty"`
}

// ConversationPage is one page of summaries plus the total matching the
// filter (stable across pages — it ignores the cursor) and the cursor for
// the next page (nil on the last page).
type ConversationPage struct {
	Conversations []ConversationSummary
	Total         int
	NextCursor    *ConvCursor
}

// convFilters holds the shared WHERE fragments. inner clauses run against
// conversations c; minMax clauses reference the message_count alias and so
// must run in an outer wrapper. The cursor is applied separately (only the
// page query carries it) so Total stays cursor-independent.
type convFilters struct {
	inner      []string
	innerArgs  []any
	minMax     []string
	minMaxArgs []any
}

func (s *SQLiteStore) conversationFilters(q ConversationQuery) convFilters {
	var f convFilters

	// Membership group: ids OR kind-prefixes, OR'd together then AND'd with the rest.
	var member []string
	if len(q.IDs) > 0 {
		member = append(member, "c.id IN ("+convPlaceholders(len(q.IDs))+")")
		for _, id := range q.IDs {
			f.innerArgs = append(f.innerArgs, id)
		}
	}
	for _, k := range q.Kinds {
		member = append(member, `c.id LIKE ? ESCAPE '\'`)
		f.innerArgs = append(f.innerArgs, escapeLikePattern(k)+"-%")
	}
	if len(member) > 0 {
		f.inner = append(f.inner, "("+strings.Join(member, " OR ")+")")
	}

	// Metadata filters — each guarded by json_valid so a row with invalid
	// metadata (an expected on-disk state) is excluded rather than aborting
	// the whole query.
	if q.Channel != "" {
		f.inner = append(f.inner, `(json_valid(c.metadata) AND json_extract(c.metadata,'$.channel_binding.channel') = ?)`)
		f.innerArgs = append(f.innerArgs, q.Channel)
	}
	if q.ContactID != "" {
		f.inner = append(f.inner, `(json_valid(c.metadata) AND json_extract(c.metadata,'$.channel_binding.contact_id') = ?)`)
		f.innerArgs = append(f.innerArgs, q.ContactID)
	}
	if q.Address != "" {
		f.inner = append(f.inner, `(json_valid(c.metadata) AND json_extract(c.metadata,'$.channel_binding.address') = ?)`)
		f.innerArgs = append(f.innerArgs, q.Address)
	}
	if q.Q != "" {
		like := "%" + escapeLikePattern(q.Q) + "%"
		f.inner = append(f.inner, `(c.id LIKE ? ESCAPE '\' OR (json_valid(c.metadata) AND (COALESCE(json_extract(c.metadata,'$.channel_binding.contact_name'),'') LIKE ? ESCAPE '\' OR COALESCE(json_extract(c.metadata,'$.channel_binding.address'),'') LIKE ? ESCAPE '\')))`)
		f.innerArgs = append(f.innerArgs, like, like, like)
	}

	// Time ranges, compared against the normalized expression (index-friendly).
	if q.UpdatedAfter != nil {
		f.inner = append(f.inner, convUpdatedNorm+" >= ?")
		f.innerArgs = append(f.innerArgs, normConvTime(*q.UpdatedAfter))
	}
	if q.UpdatedBefore != nil {
		f.inner = append(f.inner, convUpdatedNorm+" < ?")
		f.innerArgs = append(f.innerArgs, normConvTime(*q.UpdatedBefore))
	}
	if q.CreatedAfter != nil {
		f.inner = append(f.inner, convCreatedNorm+" >= ?")
		f.innerArgs = append(f.innerArgs, normConvTime(*q.CreatedAfter))
	}
	if q.CreatedBefore != nil {
		f.inner = append(f.inner, convCreatedNorm+" < ?")
		f.innerArgs = append(f.innerArgs, normConvTime(*q.CreatedBefore))
	}

	if q.MinMessages != nil {
		f.minMax = append(f.minMax, "message_count >= ?")
		f.minMaxArgs = append(f.minMaxArgs, *q.MinMessages)
	}
	if q.MaxMessages != nil {
		f.minMax = append(f.minMax, "message_count <= ?")
		f.minMaxArgs = append(f.minMaxArgs, *q.MaxMessages)
	}

	return f
}

// QueryConversations returns one filtered, sorted, keyset-paginated page of
// conversation summaries plus the total matching the filter. It never loads
// message content: the active count is a correlated COUNT over the
// idx_messages_status index, so cost scales with the page size and filter
// selectivity, not the all-time conversation corpus. GetAllConversations is
// left untouched for the checkpointer, which needs full message bodies.
func (s *SQLiteStore) QueryConversations(q ConversationQuery) (*ConversationPage, error) {
	sort := "updated_at"
	switch q.Sort {
	case "", "updated_at":
	case "created_at", "message_count":
		sort = q.Sort
	default:
		return nil, fmt.Errorf("invalid sort %q", q.Sort)
	}
	order := "desc"
	cmp := "<"
	if q.Order == "asc" {
		order, cmp = "asc", ">"
	}
	dir := strings.ToUpper(order)

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	sortIsCount := sort == "message_count"
	innerSortExpr := convUpdatedNorm
	outerSortCol := "updated_norm"
	if sort == "created_at" {
		innerSortExpr, outerSortCol = convCreatedNorm, "created_norm"
	}

	f := s.conversationFilters(q)

	total, err := s.countConversations(f)
	if err != nil {
		return nil, err
	}

	// Page query. The keyset cursor lives in the inner WHERE for time sorts
	// (so the expression index drives the seek) and in the outer WHERE for the
	// count sort (where the alias is referenceable).
	inner := append([]string(nil), f.inner...)
	innerArgs := append([]any(nil), f.innerArgs...)
	outer := append([]string(nil), f.minMax...)
	outerArgs := append([]any(nil), f.minMaxArgs...)

	if q.Cursor != nil {
		if sortIsCount {
			cv, err := strconv.Atoi(q.Cursor.V)
			if err != nil {
				return nil, fmt.Errorf("invalid count cursor %q: %w", q.Cursor.V, err)
			}
			outer = append(outer, "(message_count "+cmp+" ? OR (message_count = ? AND id "+cmp+" ?))")
			outerArgs = append(outerArgs, cv, cv, q.Cursor.ID)
		} else {
			inner = append(inner, "("+innerSortExpr+" "+cmp+" ? OR ("+innerSortExpr+" = ? AND c.id "+cmp+" ?))")
			innerArgs = append(innerArgs, q.Cursor.V, q.Cursor.V, q.Cursor.ID)
		}
	}

	needOuter := sortIsCount || len(f.minMax) > 0

	var sb strings.Builder
	var args []any
	if !needOuter {
		sb.WriteString("SELECT " + convSummarySelect + " FROM conversations c")
		if len(inner) > 0 {
			sb.WriteString(" WHERE " + strings.Join(inner, " AND "))
		}
		sb.WriteString(" ORDER BY " + innerSortExpr + " " + dir + ", c.id " + dir + " LIMIT ?")
		args = append(args, innerArgs...)
		args = append(args, limit)
	} else {
		var innerSQL strings.Builder
		innerSQL.WriteString("SELECT " + convSummarySelect + " FROM conversations c")
		if len(inner) > 0 {
			innerSQL.WriteString(" WHERE " + strings.Join(inner, " AND "))
		}

		sb.WriteString("SELECT id, message_count, created_norm, updated_norm, metadata FROM (")
		sb.WriteString(innerSQL.String())
		sb.WriteString(")")
		if len(outer) > 0 {
			sb.WriteString(" WHERE " + strings.Join(outer, " AND "))
		}
		orderCol := outerSortCol
		if sortIsCount {
			orderCol = "message_count"
		}
		sb.WriteString(" ORDER BY " + orderCol + " " + dir + ", id " + dir + " LIMIT ?")
		args = append(args, innerArgs...)
		args = append(args, outerArgs...)
		args = append(args, limit)
	}

	rows, err := s.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query conversations: %w", err)
	}
	defer rows.Close()

	page := &ConversationPage{Total: total}
	var lastID, lastNorm string
	var lastCount int
	for rows.Next() {
		var id string
		// strftime returns NULL for any value it cannot parse; scan into
		// NullString so one odd row degrades to a zero timestamp (mirroring
		// GetAllConversations' tolerate-and-continue) rather than 500-ing the
		// whole page.
		var createdNorm, updatedNorm, metadata sql.NullString
		var msgCount int
		if err := rows.Scan(&id, &msgCount, &createdNorm, &updatedNorm, &metadata); err != nil {
			return nil, fmt.Errorf("scan conversation summary: %w", err)
		}
		sum := ConversationSummary{ID: id, MessageCount: msgCount}
		if createdNorm.Valid {
			if t, err := database.ParseTimestamp(createdNorm.String); err == nil {
				sum.CreatedAt = t
			}
		}
		if updatedNorm.Valid {
			if t, err := database.ParseTimestamp(updatedNorm.String); err == nil {
				sum.UpdatedAt = t
			}
		}
		if metadata.Valid {
			if meta, err := parseConversationMetadata(metadata.String); err != nil {
				s.logger.Warn("conversation metadata invalid during query",
					"conversation_id", id, "error", err)
			} else if meta != nil {
				sum.ChannelBinding = meta.ChannelBinding
			}
		}
		page.Conversations = append(page.Conversations, sum)
		lastID = id
		lastCount = msgCount
		if sort == "created_at" {
			lastNorm = createdNorm.String
		} else {
			lastNorm = updatedNorm.String
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}

	if len(page.Conversations) == limit {
		cur := &ConvCursor{Sort: sort, Order: order, ID: lastID}
		if sortIsCount {
			cur.V = strconv.Itoa(lastCount)
		} else {
			cur.V = lastNorm
		}
		page.NextCursor = cur
	}
	return page, nil
}

// countConversations returns the number of conversations matching the filter,
// independent of any pagination cursor — so "N matches" stays stable as the
// caller pages. It avoids the message_count subquery entirely unless a
// min/max-messages filter actually needs it.
func (s *SQLiteStore) countConversations(f convFilters) (int, error) {
	var sb strings.Builder
	var args []any
	if len(f.minMax) == 0 {
		sb.WriteString("SELECT COUNT(*) FROM conversations c")
		if len(f.inner) > 0 {
			sb.WriteString(" WHERE " + strings.Join(f.inner, " AND "))
		}
		args = append(args, f.innerArgs...)
	} else {
		sb.WriteString("SELECT COUNT(*) FROM (SELECT " + convCountExpr + " AS message_count FROM conversations c")
		if len(f.inner) > 0 {
			sb.WriteString(" WHERE " + strings.Join(f.inner, " AND "))
		}
		sb.WriteString(") WHERE " + strings.Join(f.minMax, " AND "))
		args = append(args, f.innerArgs...)
		args = append(args, f.minMaxArgs...)
	}

	var total int
	if err := s.db.QueryRow(sb.String(), args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count conversations: %w", err)
	}
	return total, nil
}

func convPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// escapeLikePattern escapes the LIKE metacharacters %, _, and the escape
// character itself so a literal value matches literally. Pair with
// ESCAPE '\' in the query.
func escapeLikePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
