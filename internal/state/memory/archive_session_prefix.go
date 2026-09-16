package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// sessionIDHexDigits is the number of hex digits in a canonical session
// id. Session ids are UUIDs rendered 8-4-4-4-12, so a full id is 36
// characters once the four hyphens are in place.
const sessionIDHexDigits = 32

// fullSessionIDLength is the length of a canonical session id with its
// hyphens.
const fullSessionIDLength = sessionIDHexDigits + 4

// DefaultSessionPrefixCandidates is how many matching sessions
// [ArchiveStore.ResolveSessionPrefix] returns when its caller passes no
// positive limit. Enough to show a model that a prefix is shared and
// what shares it, small enough to fit inside one tool error.
const DefaultSessionPrefixCandidates = 5

// ErrMalformedSessionID marks a session id, or a leading part of one,
// that cannot begin any canonical session id: it contains a character
// other than a hex digit or hyphen, has no hex digits, or has more hex
// digits than a full id. Callers use errors.Is to tell a bad argument
// from a store failure.
var ErrMalformedSessionID = errors.New("malformed session id")

// sessionPrefixRangeQuery reads the sessions whose ids fall in the
// half-open range [prefix, next(prefix)). The range is served by the
// primary-key index on sessions.id, so it costs the same for the
// oldest session as for the newest.
const sessionPrefixRangeQuery = `
	SELECT id, started_at, title, summary
	FROM sessions
	WHERE id >= ? AND id < ?
	ORDER BY id
	LIMIT ?`

// sessionPrefixCountQuery counts every session in the same range, for
// lookups whose matches outnumber the requested limit.
const sessionPrefixCountQuery = `SELECT COUNT(*) FROM sessions WHERE id >= ? AND id < ?`

// SessionPrefixMatch is one archived session whose id begins with a
// looked-up prefix, carrying what a reader needs to tell it apart from
// the other matches.
type SessionPrefixMatch struct {
	ID        string
	StartedAt time.Time
	Title     string
	Summary   string
}

// SessionPrefixLookup is the bounded result of
// [ArchiveStore.ResolveSessionPrefix].
type SessionPrefixLookup struct {
	// Prefix is the normalized form of the prefix that was looked up.
	Prefix string
	// Matches lists at most the requested number of matching sessions
	// in id order, which for UUIDv7 ids is the order they were minted.
	Matches []SessionPrefixMatch
	// Total counts every archived session whose id begins with Prefix,
	// including those Matches omits because of the limit.
	Total int
}

// Unlisted reports how many matching sessions Matches omits.
func (l SessionPrefixLookup) Unlisted() int {
	return l.Total - len(l.Matches)
}

// NormalizeSessionIDPrefix canonicalizes a full session id or a leading
// part of one. Hyphens in the input are ignored and hex digits are
// lowercased, then hyphens are re-inserted at the canonical 8-4-4-4-12
// positions, so "0190AAAABBBB", "0190aaaa-bbbb" and "0190aaaa-bbbb-"
// all normalize to "0190aaaa-bbbb". The error wraps
// [ErrMalformedSessionID] and names the offending character or count.
func NormalizeSessionIDPrefix(raw string) (string, error) {
	digits := make([]byte, 0, sessionIDHexDigits)
	for i, r := range raw {
		switch {
		case r == '-':
			continue
		case '0' <= r && r <= '9', 'a' <= r && r <= 'f':
			digits = append(digits, byte(r))
		case 'A' <= r && r <= 'F':
			digits = append(digits, byte(r-'A'+'a'))
		default:
			return "", fmt.Errorf("%w %q: character %q at byte %d is not a hex digit or hyphen", ErrMalformedSessionID, raw, r, i+1)
		}
	}
	switch {
	case len(digits) == 0:
		return "", fmt.Errorf("%w %q: it has no hex digits", ErrMalformedSessionID, raw)
	case len(digits) > sessionIDHexDigits:
		return "", fmt.Errorf("%w %q: it has %d hex digits and a full session id has %d", ErrMalformedSessionID, raw, len(digits), sessionIDHexDigits)
	}

	var b strings.Builder
	b.Grow(fullSessionIDLength)
	for i, d := range digits {
		if i == 8 || i == 12 || i == 16 || i == 20 {
			b.WriteByte('-')
		}
		b.WriteByte(d)
	}
	return b.String(), nil
}

// IsFullSessionID reports whether a value returned by
// [NormalizeSessionIDPrefix] is a complete session id rather than a
// leading part of one.
func IsFullSessionID(normalized string) bool {
	return len(normalized) == fullSessionIDLength
}

// ResolveSessionPrefix finds the archived sessions whose ids begin with
// prefix, a full session id or any leading part of one in a form
// [NormalizeSessionIDPrefix] accepts. It searches every archived
// session, returns at most limit of them (or
// [DefaultSessionPrefixCandidates] when limit is not positive), and
// reports the full match count in Total. Zero matches is a successful
// empty lookup, not an error.
//
// Both reads it makes run under ctx, so a caller whose tool call is
// cancelled or past its deadline stops paying for the lookup.
func (s *ArchiveStore) ResolveSessionPrefix(ctx context.Context, prefix string, limit int) (SessionPrefixLookup, error) {
	normalized, err := NormalizeSessionIDPrefix(prefix)
	if err != nil {
		return SessionPrefixLookup{}, err
	}
	if limit <= 0 {
		limit = DefaultSessionPrefixCandidates
	}
	upper := sessionIDPrefixUpperBound(normalized)

	// One row past the limit says whether a count is needed at all, so
	// the common unique-prefix lookup is a single indexed read.
	matches, err := s.readSessionPrefixRange(ctx, normalized, upper, limit+1)
	if err != nil {
		return SessionPrefixLookup{}, err
	}
	lookup := SessionPrefixLookup{Prefix: normalized, Matches: matches, Total: len(matches)}
	if len(matches) <= limit {
		return lookup, nil
	}

	lookup.Matches = matches[:limit]
	if lookup.Total, err = s.countSessionPrefixRange(ctx, normalized, upper); err != nil {
		return SessionPrefixLookup{}, err
	}
	return lookup, nil
}

// countSessionPrefixRange runs [sessionPrefixCountQuery] under ctx.
func (s *ArchiveStore) countSessionPrefixRange(ctx context.Context, lower, upper string) (int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, sessionPrefixCountQuery, lower, upper).Scan(&total); err != nil {
		return 0, fmt.Errorf("count sessions with id prefix %q: %w", lower, err)
	}
	return total, nil
}

// readSessionPrefixRange runs [sessionPrefixRangeQuery] under ctx and
// parses the rows it returns.
func (s *ArchiveStore) readSessionPrefixRange(ctx context.Context, lower, upper string, limit int) ([]SessionPrefixMatch, error) {
	rows, err := s.db.QueryContext(ctx, sessionPrefixRangeQuery, lower, upper, limit)
	if err != nil {
		return nil, fmt.Errorf("find sessions with id prefix %q: %w", lower, err)
	}
	defer rows.Close()

	var matches []SessionPrefixMatch
	for rows.Next() {
		var match SessionPrefixMatch
		var startedAt string
		var title, summary sql.NullString
		if err := rows.Scan(&match.ID, &startedAt, &title, &summary); err != nil {
			return nil, fmt.Errorf("scan session with id prefix %q: %w", lower, err)
		}
		if match.StartedAt, err = database.ParseTimestamp(startedAt); err != nil {
			return nil, fmt.Errorf("parse started_at of session %s: %w", match.ID, err)
		}
		match.Title = title.String
		match.Summary = summary.String
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find sessions with id prefix %q: %w", lower, err)
	}
	return matches, nil
}

// sessionIDPrefixUpperBound returns the smallest string greater than
// every string that begins with prefix. A normalized prefix always ends
// in a hex digit, whose successor byte ('9'+1 is ':', 'f'+1 is 'g') is
// still plain ASCII, so incrementing the final byte is enough.
func sessionIDPrefixUpperBound(prefix string) string {
	last := len(prefix) - 1
	return prefix[:last] + string(prefix[last]+1)
}
