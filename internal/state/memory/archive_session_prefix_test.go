package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeSessionIDPrefix(t *testing.T) {
	const full = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "eight hex digits", raw: "0190aaaa", want: "0190aaaa"},
		{name: "one hex digit", raw: "0", want: "0"},
		{name: "partial with hyphen", raw: "0190aaaa-bbbb", want: "0190aaaa-bbbb"},
		{name: "partial without hyphens", raw: "0190aaaabbbb7c", want: "0190aaaa-bbbb-7c"},
		{name: "trailing hyphen dropped", raw: "0190aaaa-bbbb-", want: "0190aaaa-bbbb"},
		{name: "misplaced hyphen ignored", raw: "0190-aaaa", want: "0190aaaa"},
		{name: "uppercase lowered", raw: "0190AAAA-BBBB", want: "0190aaaa-bbbb"},
		{name: "full id unchanged", raw: full, want: full},
		{name: "full id without hyphens", raw: strings.ReplaceAll(full, "-", ""), want: full},
		{name: "empty", raw: "", wantErr: "has no hex digits"},
		{name: "hyphens only", raw: "---", wantErr: "has no hex digits"},
		{name: "non-hex letter", raw: "0190aaaz", wantErr: `character 'z' at byte 8 is not a hex digit or hyphen`},
		{name: "separator from a citation", raw: "session:0190aaaa", wantErr: `character 's' at byte 1`},
		{name: "too many digits", raw: full + "0", wantErr: "it has 33 hex digits and a full session id has 32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSessionIDPrefix(tt.raw)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrMalformedSessionID) {
					t.Fatalf("NormalizeSessionIDPrefix(%q) error = %v, want ErrMalformedSessionID", tt.raw, err)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NormalizeSessionIDPrefix(%q) error = %q, want it to contain %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeSessionIDPrefix(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeSessionIDPrefix(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if IsFullSessionID(got) != (got == full) {
				t.Fatalf("IsFullSessionID(%q) = %v", got, IsFullSessionID(got))
			}
		})
	}
}

func TestResolveSessionPrefix(t *testing.T) {
	store := newTestArchiveStore(t)
	base := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	seed := []struct {
		id    string
		title string
	}{
		{"0190aaa9-0000-7000-8000-000000000001", "just below the shared prefix"},
		{"0190aaaa-0000-7000-8000-000000000003", "Carol's third"},
		{"0190aaaa-0000-7000-8000-000000000001", "Alice's first"},
		{"0190aaaa-0000-7000-8000-000000000002", "Bob's second"},
		{"0190aaab-0000-7000-8000-000000000001", "just above the shared prefix"},
		{"0190aaaf-0000-7000-8000-000000000001", "last before the digit rolls"},
		{"0190aab0-0000-7000-8000-000000000001", "first after the digit rolls"},
	}
	for i, s := range seed {
		if _, err := store.db.Exec(`INSERT INTO sessions (id, conversation_id, started_at, title, summary) VALUES (?, 'conv', ?, ?, ?)`,
			s.id, base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339Nano), s.title, "summary of "+s.title); err != nil {
			t.Fatalf("seed session %s: %v", s.id, err)
		}
	}

	tests := []struct {
		name      string
		prefix    string
		limit     int
		wantIDs   []string
		wantTotal int
	}{
		{
			name:   "shared prefix within limit, in id order",
			prefix: "0190aaaa", limit: 5,
			wantIDs: []string{
				"0190aaaa-0000-7000-8000-000000000001",
				"0190aaaa-0000-7000-8000-000000000002",
				"0190aaaa-0000-7000-8000-000000000003",
			},
			wantTotal: 3,
		},
		{
			name:   "shared prefix beyond limit counts the rest",
			prefix: "0190aaaa", limit: 2,
			wantIDs: []string{
				"0190aaaa-0000-7000-8000-000000000001",
				"0190aaaa-0000-7000-8000-000000000002",
			},
			wantTotal: 3,
		},
		{
			name:   "non-positive limit uses the default",
			prefix: "0190aa", limit: 0,
			wantIDs: []string{
				"0190aaa9-0000-7000-8000-000000000001",
				"0190aaaa-0000-7000-8000-000000000001",
				"0190aaaa-0000-7000-8000-000000000002",
				"0190aaaa-0000-7000-8000-000000000003",
				"0190aaab-0000-7000-8000-000000000001",
			},
			wantTotal: 7,
		},
		{
			name:      "prefix ending in 9 stops before a",
			prefix:    "0190aaa9",
			wantIDs:   []string{"0190aaa9-0000-7000-8000-000000000001"},
			wantTotal: 1,
		},
		{
			name:      "prefix ending in f stops before the next digit",
			prefix:    "0190aaaf",
			wantIDs:   []string{"0190aaaf-0000-7000-8000-000000000001"},
			wantTotal: 1,
		},
		{
			name:      "hyphenless uppercase full id",
			prefix:    "0190AAAA000070008000000000000002",
			wantIDs:   []string{"0190aaaa-0000-7000-8000-000000000002"},
			wantTotal: 1,
		},
		{
			name:      "full id matches only itself",
			prefix:    "0190aaaa-0000-7000-8000-000000000003",
			wantIDs:   []string{"0190aaaa-0000-7000-8000-000000000003"},
			wantTotal: 1,
		},
		{
			name:      "no match is an empty lookup",
			prefix:    "0fff",
			wantIDs:   nil,
			wantTotal: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup, err := store.ResolveSessionPrefix(t.Context(), tt.prefix, tt.limit)
			if err != nil {
				t.Fatalf("ResolveSessionPrefix(%q, %d): %v", tt.prefix, tt.limit, err)
			}
			var gotIDs []string
			for _, m := range lookup.Matches {
				gotIDs = append(gotIDs, m.ID)
			}
			if !slices.Equal(gotIDs, tt.wantIDs) {
				t.Fatalf("matches = %v, want %v", gotIDs, tt.wantIDs)
			}
			if lookup.Total != tt.wantTotal {
				t.Fatalf("Total = %d, want %d", lookup.Total, tt.wantTotal)
			}
			if got, want := lookup.Unlisted(), tt.wantTotal-len(tt.wantIDs); got != want {
				t.Fatalf("Unlisted() = %d, want %d", got, want)
			}
		})
	}

	t.Run("match carries start time, title and summary", func(t *testing.T) {
		lookup, err := store.ResolveSessionPrefix(t.Context(), "0190aaaa-0000-7000-8000-000000000001", 1)
		if err != nil {
			t.Fatalf("ResolveSessionPrefix: %v", err)
		}
		if len(lookup.Matches) != 1 {
			t.Fatalf("matches = %d, want 1", len(lookup.Matches))
		}
		m := lookup.Matches[0]
		if !m.StartedAt.Equal(base.Add(2*time.Hour)) || m.Title != "Alice's first" || m.Summary != "summary of Alice's first" {
			t.Fatalf("match = %+v", m)
		}
		if lookup.Prefix != "0190aaaa-0000-7000-8000-000000000001" {
			t.Fatalf("Prefix = %q", lookup.Prefix)
		}
	})

	t.Run("malformed prefix is refused", func(t *testing.T) {
		_, err := store.ResolveSessionPrefix(t.Context(), "0190aaaq", 5)
		if !errors.Is(err, ErrMalformedSessionID) {
			t.Fatalf("error = %v, want ErrMalformedSessionID", err)
		}
	})
}

// TestSessionPrefixQueriesUseIDIndex pins the lookup to an indexed
// range on sessions.id: a full-table scan would make resolving an old
// session cost the whole archive.
func TestSessionPrefixQueriesUseIDIndex(t *testing.T) {
	store := newTestArchiveStore(t)
	for name, query := range map[string]string{
		"range": sessionPrefixRangeQuery,
		"count": sessionPrefixCountQuery,
	} {
		t.Run(name, func(t *testing.T) {
			args := []any{"0190aaaa", "0190aaab"}
			if name == "range" {
				args = append(args, 6)
			}
			rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan plan: %v", err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("plan rows: %v", err)
			}
			plan := strings.Join(details, " | ")
			if !strings.Contains(plan, "SEARCH sessions USING") || !strings.Contains(plan, "(id>? AND id<?)") {
				t.Fatalf("query plan = %q, want an indexed range search on sessions.id", plan)
			}
		})
	}
}

// TestResolveSessionPrefixHonoursContext pins the lookup to the caller's
// context. A tool call that is cancelled or past its deadline must stop
// paying for the archive scan, and the count read that a shared prefix
// triggers is as cancellable as the range read every lookup makes.
func TestResolveSessionPrefixHonoursContext(t *testing.T) {
	store := newTestArchiveStore(t)
	base := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	const lower, upper = "0190aaaa", "0190aaab"
	for i := range 3 {
		id := fmt.Sprintf("%s-0000-7000-8000-%012d", lower, i)
		if _, err := store.db.Exec(`INSERT INTO sessions (id, conversation_id, started_at, title) VALUES (?, 'conv', ?, ?)`,
			id, base.Format(time.RFC3339Nano), "Bob import "+id); err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	t.Run("range read", func(t *testing.T) {
		if _, err := store.readSessionPrefixRange(cancelled, lower, upper, 6); !errors.Is(err, context.Canceled) {
			t.Fatalf("readSessionPrefixRange error = %v, want context.Canceled", err)
		}
	})
	t.Run("count read", func(t *testing.T) {
		if _, err := store.countSessionPrefixRange(cancelled, lower, upper); !errors.Is(err, context.Canceled) {
			t.Fatalf("countSessionPrefixRange error = %v, want context.Canceled", err)
		}
	})
	t.Run("lookup", func(t *testing.T) {
		// limit 1 against three matches is the shape that reads the
		// range and then counts, so neither read can be the only one
		// the cancellation reaches.
		if _, err := store.ResolveSessionPrefix(cancelled, lower, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("ResolveSessionPrefix error = %v, want context.Canceled", err)
		}
	})
	// Negative control: the same lookup under a live context answers, so
	// the cancelled cases above fail on cancellation rather than on the
	// fixture or the query.
	t.Run("live context still counts the matches", func(t *testing.T) {
		lookup, err := store.ResolveSessionPrefix(t.Context(), lower, 1)
		if err != nil {
			t.Fatalf("ResolveSessionPrefix: %v", err)
		}
		if len(lookup.Matches) != 1 || lookup.Total != 3 {
			t.Fatalf("lookup listed %d of %d matches, want 1 of 3", len(lookup.Matches), lookup.Total)
		}
	})
}

// TestNormalizeSessionIDPrefixBoundsItsEcho pins that a malformed
// argument cannot size the error it provokes. The refusal reaches the
// model as a tool result, and a session_id can arrive as a resolved
// content reference rather than as something a model typed, so the
// argument is not bounded by the caller's own output.
func TestNormalizeSessionIDPrefixBoundsItsEcho(t *testing.T) {
	const oversized = 200_000
	tests := []struct {
		name string
		raw  string
		// wantErr is a fragment the clipped error must still carry, so
		// the clip does not cost the diagnosis.
		wantErr string
	}{
		{
			name:    "non-hex run",
			raw:     strings.Repeat("z", oversized),
			wantErr: "character 'z' at byte 1 is not a hex digit or hyphen",
		},
		{
			name:    "hex run longer than an id",
			raw:     strings.Repeat("a", oversized),
			wantErr: "it has 200000 hex digits and a full session id has 32",
		},
		{
			name:    "hyphens only",
			raw:     strings.Repeat("-", oversized),
			wantErr: "it has no hex digits",
		},
		{
			name: "non-printable runes, which %q escapes several bytes each",
			raw:  strings.Repeat("\x00", oversized),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeSessionIDPrefix(tt.raw)
			if !errors.Is(err, ErrMalformedSessionID) {
				t.Fatalf("NormalizeSessionIDPrefix() error = %v, want ErrMalformedSessionID", err)
			}
			got := err.Error()
			// Generous next to the argument, tight next to any tool-result
			// cap: the echo is clipped, and %q escaping of what survives
			// is the only other term.
			if max := 8 * sessionIDEchoMaxBytes; len(got) > max {
				t.Errorf("error is %d bytes for a %d-byte argument, want at most %d", len(got), len(tt.raw), max)
			}
			if !utf8.ValidString(got) {
				t.Error("error is not valid UTF-8")
			}
			if want := fmt.Sprintf("... (%d bytes)", len(tt.raw)); !strings.Contains(got, want) {
				t.Errorf("error does not report the clipped argument's size %q: %s", want, got)
			}
			if tt.wantErr != "" && !strings.Contains(got, tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", got, tt.wantErr)
			}
		})
	}
}

// TestEchoSessionIDCutsOnARuneBoundary pins that the echo never splits a
// multi-byte character (AGENTS.md), including when the bound falls inside
// one.
func TestEchoSessionIDCutsOnARuneBoundary(t *testing.T) {
	// Four-byte runes: 96 is not a multiple of 4, so a byte-index cut
	// would land inside the 25th glyph.
	raw := strings.Repeat("😀", 40)
	got := echoSessionID(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("echoSessionID() is not valid UTF-8: %q", got)
	}
	clipped := strings.TrimSuffix(got, fmt.Sprintf("... (%d bytes)", len(raw)))
	if clipped == got {
		t.Fatalf("echoSessionID() did not mark the cut: %q", got)
	}
	if len(clipped) > sessionIDEchoMaxBytes {
		t.Errorf("echoSessionID() kept %d bytes, want at most %d", len(clipped), sessionIDEchoMaxBytes)
	}
	if !strings.HasPrefix(raw, clipped) {
		t.Errorf("echoSessionID() kept %q, which is not a prefix of the argument", clipped)
	}
}
