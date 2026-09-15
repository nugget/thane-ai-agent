package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArchiveSearchContextMixedTimestamps(t *testing.T) {
	for _, fts := range []bool{true, false} {
		name := "fts"
		if !fts {
			name = "like"
		}
		t.Run(name, func(t *testing.T) {
			store := newTestArchiveStore(t)
			store.ftsEnabled = fts
			anchor := time.Date(2026, 9, 15, 0, 0, 0, 500, time.UTC)
			seedArchiveSearchMessage(t, store, "hit", "conv", "needle", anchor)
			seedArchiveSearchMessage(t, store, "before", "conv", "earlier", anchor.Add(-time.Minute))
			seedArchiveSearchMessage(t, store, "after", "conv", "later", "2026-09-14T19:01:00.0000005-05:00")
			seedArchiveSearchMessage(t, store, "near-before", "conv", "same second earlier", anchor.Add(-time.Nanosecond))
			seedArchiveSearchMessage(t, store, "near-after", "conv", "same second later", anchor.Add(time.Nanosecond))
			seedArchiveSearchMessage(t, store, "same-time", "conv", "simultaneous", anchor)
			seedArchiveSearchMessage(t, store, "far-before", "conv", "before silence", anchor.Add(-20*time.Minute))
			seedArchiveSearchMessage(t, store, "far-after", "conv", "after silence", anchor.Add(20*time.Minute))
			seedArchiveSearchMessage(t, store, "other", "other", "unrelated", anchor.Add(time.Second))
			seedArchiveSearchMessage(t, store, "unrelated-invalid", "other", "unrelated", "invalid")

			results, err := store.Search(SearchOptions{Query: "needle", ConversationID: "conv"})
			if err != nil || len(results) != 1 {
				t.Fatalf("Search = %v, %v", results, err)
			}
			assertSearchMessageIDs(t, results[0].ContextBefore, "before", "near-before")
			assertSearchMessageIDs(t, results[0].ContextAfter, "near-after", "after")
		})
	}
}

func TestArchiveSearchContextWindowBounds(t *testing.T) {
	store := newTestArchiveStore(t)
	anchor := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	seedArchiveSearchMessage(t, store, "hit", "conv", "needle", anchor)
	for _, offset := range []int{-3, -2, -1, 1, 2, 3} {
		seedArchiveSearchMessage(t, store, fmt.Sprint(offset), "conv", "context", anchor.Add(time.Duration(offset)*time.Minute))
	}
	for _, tc := range []struct {
		name   string
		opts   SearchOptions
		before []string
		after  []string
	}{
		{"exclusive duration", SearchOptions{MaxDuration: 3 * time.Minute}, []string{"-2", "-1"}, []string{"1", "2"}},
		{"per direction cap", SearchOptions{MaxMessages: 1}, []string{"-1"}, []string{"1"}},
		{"silence", SearchOptions{SilenceThreshold: 30 * time.Second}, nil, nil},
		{"no context", SearchOptions{NoContext: true}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Query = "needle"
			results, err := store.SearchContext(context.Background(), tc.opts)
			if err != nil || len(results) != 1 {
				t.Fatalf("SearchContext = %v, %v", results, err)
			}
			assertSearchMessageIDs(t, results[0].ContextBefore, tc.before...)
			assertSearchMessageIDs(t, results[0].ContextAfter, tc.after...)
		})
	}
}

func TestArchiveSearchContextErrors(t *testing.T) {
	for _, tc := range []struct {
		name, update, want string
	}{
		{"timestamp", "UPDATE messages SET timestamp = 'invalid' WHERE id = 'context'", "unrecognized timestamp"},
		{"archive timestamp", "UPDATE messages SET archived_at = 'invalid' WHERE id = 'context'", "parse message archived_at"},
		{"scan", "UPDATE messages SET token_count = 'invalid' WHERE id = 'context'", "scan message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestArchiveStore(t)
			anchor := time.Now()
			seedArchiveSearchMessage(t, store, "hit", "conv", "needle", anchor)
			seedArchiveSearchMessage(t, store, "context", "conv", "surrounding words", anchor.Add(-time.Second))
			if _, err := store.db.Exec(tc.update); err != nil {
				t.Fatal(err)
			}
			results, err := store.SearchContext(context.Background(), SearchOptions{Query: "needle"})
			if results != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SearchContext = %v, %v; want no partial results and %q", results, err, tc.want)
			}
			// Corruption outside the requested context must not discard a
			// valid matching row when surrounding context was not requested.
			results, err = store.SearchContext(context.Background(), SearchOptions{Query: "needle", NoContext: true})
			if err != nil || len(results) != 1 {
				t.Fatalf("SearchContext without context = %v, %v", results, err)
			}
		})
	}
	store := newTestArchiveStore(t)
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	results, err := store.expandContext(context.Background(), "conv", time.Now(), false, SearchOptions{MaxDuration: time.Hour, MaxMessages: 5})
	if err == nil || results != nil {
		t.Fatalf("closed database context = %v, %v", results, err)
	}
}

func TestMemorySearchContextFailsIncompleteBundle(t *testing.T) {
	for _, tc := range []struct {
		name, corrupt, want string
	}{
		{"messages", "UPDATE messages SET token_count = 'invalid'", "scan"},
		{"sessions query", "DROP TABLE sessions_fts", "search sessions"},
		{"session timestamp", "UPDATE sessions SET started_at = 'invalid'", "parse session started_at"},
		{"session tags", "UPDATE sessions SET tags = 'invalid'", "parse session tags"},
		{"working query", "DROP TABLE working_memory_fts", "search working memory"},
		{"working timestamp", "UPDATE working_memory SET updated_at = 'invalid'", "parse working memory updated_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := newTestArchiveStore(t)
			seedArchiveSearchMessage(t, archive, "hit", "conv", "needle", time.Now())
			seedArchiveSearchSession(t, archive, "session", "conv", "needle")
			working, err := NewWorkingMemoryStore(archive.DB(), true)
			if err != nil {
				t.Fatal(err)
			}
			if err := working.Set("conv", "needle"); err != nil {
				t.Fatal(err)
			}
			if _, err := archive.db.Exec(tc.corrupt); err != nil {
				t.Fatal(err)
			}
			bundle, err := NewMemorySearch(archive, working, nil).SearchContext(context.Background(), SearchOptions{Query: "needle"})
			if bundle != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SearchContext = %+v, %v; want nil bundle and %q", bundle, err, tc.want)
			}
		})
	}
}

func TestMemorySearchContextScopesBeforeDistilledLimit(t *testing.T) {
	archive := newTestArchiveStore(t)
	working, err := NewWorkingMemoryStore(archive.DB(), true)
	if err != nil {
		t.Fatal(err)
	}
	// Enough better-ranked unrelated records to fill both global limits.
	for i := range maxDistilledSessions + maxDistilledWorkingMemory {
		id := fmt.Sprintf("other-%02d", i)
		seedArchiveSearchSession(t, archive, id, id, "needle")
		if err := working.Set(id, "needle"); err != nil {
			t.Fatal(err)
		}
	}
	seedArchiveSearchSession(t, archive, "target-session", "target", "needle "+strings.Repeat("background ", 100))
	if err := working.Set("target", "needle "+strings.Repeat("background ", 100)); err != nil {
		t.Fatal(err)
	}
	// Distilled surfaces intentionally have no message-time filter.
	bundle, err := NewMemorySearch(archive, working, nil).SearchContext(context.Background(), SearchOptions{
		Query: "needle", ConversationID: "target", From: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Sessions) != 1 || bundle.Sessions[0].SessionID != "target-session" ||
		len(bundle.WorkingMemory) != 1 || bundle.WorkingMemory[0].ConversationID != "target" {
		t.Fatalf("scoped bundle lost lower-ranked target: %+v", bundle)
	}
}

func TestMemorySearchContextReportsUnavailableSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		messages, sessions, wm bool
		want                   []string
	}{
		{"all enabled", true, true, true, nil},
		{"raw LIKE fallback", false, true, true, nil},
		{"sessions disabled", true, false, true, []string{"sessions"}},
		{"working disabled", true, true, false, []string{"working_memory"}},
		{"distilled disabled", true, false, false, []string{"sessions", "working_memory"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := newTestArchiveStore(t)
			seedArchiveSearchMessage(t, archive, "hit", "conv", "needle", time.Now())
			working, err := NewWorkingMemoryStore(archive.DB(), true)
			if err != nil {
				t.Fatal(err)
			}
			archive.ftsEnabled = tc.messages
			archive.sessionsFTSEnabled = tc.sessions
			working.ftsEnabled = tc.wm
			bundle, err := NewMemorySearch(archive, working, nil).SearchContext(context.Background(), SearchOptions{Query: "needle"})
			if err != nil || bundle == nil {
				t.Fatalf("SearchContext = %+v, %v", bundle, err)
			}
			if !slices.Equal(bundle.UnavailableSurfaces, tc.want) || len(bundle.Messages) != 1 {
				t.Fatalf("bundle = %+v, want one raw hit and unavailable %v", bundle, tc.want)
			}
		})
	}
	archive := newTestArchiveStore(t)
	bundle, err := NewMemorySearch(archive, nil, nil).SearchContext(context.Background(), SearchOptions{Query: "needle"})
	if err != nil || !slices.Equal(bundle.UnavailableSurfaces, []string{"working_memory"}) {
		t.Fatalf("unconfigured working-memory bundle = %+v, %v", bundle, err)
	}
	bundle, err = NewMemorySearch(nil, nil, nil).SearchContext(context.Background(), SearchOptions{Query: "needle"})
	if err != nil || !slices.Equal(bundle.UnavailableSurfaces, []string{"messages", "sessions", "working_memory"}) {
		t.Fatalf("unconfigured bundle = %+v, %v", bundle, err)
	}
}

func TestMemorySearchContextReportsSurfaceOverflow(t *testing.T) {
	for _, surface := range []string{"messages", "sessions", "working_memory"} {
		for _, overflow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/overflow=%t", surface, overflow), func(t *testing.T) {
				archive := newTestArchiveStore(t)
				working, err := NewWorkingMemoryStore(archive.DB(), true)
				if err != nil {
					t.Fatal(err)
				}
				cap := 1
				switch surface {
				case "sessions":
					cap = maxDistilledSessions
				case "working_memory":
					cap = maxDistilledWorkingMemory
				}
				count := cap
				if overflow {
					count++
				}
				for i := range count {
					id := fmt.Sprint(i)
					switch surface {
					case "messages":
						seedArchiveSearchMessage(t, archive, id, "conv", "needle", time.Now())
					case "sessions":
						seedArchiveSearchSession(t, archive, id, "conv", "needle")
					case "working_memory":
						if err := working.Set(id, "needle"); err != nil {
							t.Fatal(err)
						}
					}
				}
				bundle, err := NewMemorySearch(archive, working, nil).SearchContext(context.Background(), SearchOptions{Query: "needle", Limit: 1, NoContext: true})
				if err != nil {
					t.Fatal(err)
				}
				if got := len(bundle.Messages) + len(bundle.Sessions) + len(bundle.WorkingMemory); got != cap || bundle.Truncated != overflow {
					t.Fatalf("bundle count=%d truncated=%t, want count=%d truncated=%t", got, bundle.Truncated, cap, overflow)
				}
			})
		}
	}
}

func TestMemorySearchContextLIKEReportsPossibleOverflow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int
		count     int
		wantCount int
		truncated bool
	}{
		{"below cap", 2, 1, 1, false},
		{"at cap", 2, 2, 2, true},
		{"above cap", 2, 3, 2, true},
		{"default below cap", 0, 9, 9, false},
		{"default full page", 0, 11, 10, true},
		{"negative limit default", -1, 11, 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := newTestArchiveStore(t)
			archive.ftsEnabled = false
			for i := range tc.count {
				seedArchiveSearchMessage(t, archive, fmt.Sprint(i), "conv", "needle", time.Now())
			}
			bundle, err := NewMemorySearch(archive, nil, nil).SearchContext(context.Background(), SearchOptions{
				Query: "needle", Limit: tc.limit, NoContext: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(bundle.Messages) != tc.wantCount || bundle.Truncated != tc.truncated || bundle.TotalMessages != 0 {
				t.Fatalf("LIKE bundle count=%d truncated=%t total=%d, want count=%d truncated=%t unknown total",
					len(bundle.Messages), bundle.Truncated, bundle.TotalMessages, tc.wantCount, tc.truncated)
			}
		})
	}
}

func TestFormatMultiKindResultsCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unavailable []string
		truncated   bool
	}{
		{"complete empty search", nil, false},
		{"unavailable distilled", []string{"sessions", "working_memory"}, false},
		{"unavailable and capped", []string{"working_memory"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := FormatMultiKindResults(&SearchBundle{UnavailableSurfaces: tc.unavailable, Truncated: tc.truncated}, time.Now(), false)
			var got struct {
				Messages            []json.RawMessage `json:"messages"`
				Sessions            []json.RawMessage `json:"sessions"`
				WorkingMemory       []json.RawMessage `json:"working_memory"`
				UnavailableSurfaces []string          `json:"unavailable_surfaces"`
				Truncated           bool              `json:"truncated"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.UnavailableSurfaces, tc.unavailable) || got.Truncated != tc.truncated ||
				got.Messages == nil || got.Sessions == nil || got.WorkingMemory == nil {
				t.Fatalf("coverage lost in envelope: %s", data)
			}
		})
	}
}

func TestArchiveSearchContextCancellation(t *testing.T) {
	for _, name := range []string{"fts", "like", "count", "sessions", "working", "bundle", "context"} {
		t.Run(name, func(t *testing.T) {
			archive := newTestArchiveStore(t)
			working, err := NewWorkingMemoryStore(archive.DB(), true)
			if err != nil {
				t.Fatal(err)
			}
			if name == "like" {
				archive.ftsEnabled = false
			}
			query := func(ctx context.Context) error {
				switch name {
				case "fts", "like":
					_, err = archive.SearchContext(ctx, SearchOptions{Query: "needle"})
				case "count":
					_, err = archive.CountMatchesContext(ctx, SearchOptions{Query: "needle"})
				case "sessions":
					_, err = archive.SearchSessionsContext(ctx, "needle", 5)
				case "working":
					_, err = working.SearchContext(ctx, "needle", 5)
				case "bundle":
					_, err = NewMemorySearch(archive, working, nil).SearchContext(ctx, SearchOptions{Query: "needle"})
				case "context":
					_, err = archive.expandContext(ctx, "conv", time.Now(), false, SearchOptions{MaxDuration: time.Hour, MaxMessages: 5})
				}
				return err
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := query(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled query = %v", err)
			}
			archive.db.SetMaxOpenConns(1)
			conn, err := archive.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// A missing QueryContext must fail within a bounded time rather
			// than hanging the regression suite behind this connection.
			release := time.AfterFunc(time.Second, func() { _ = conn.Close() })
			defer release.Stop()
			ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := query(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting query = %v", err)
			}
		})
	}
}

func seedArchiveSearchSession(t *testing.T, archive *ArchiveStore, id, conversation, summary string) {
	t.Helper()
	if _, err := archive.db.Exec(`INSERT INTO sessions (id, conversation_id, started_at, summary)
		VALUES (?, ?, ?, ?)`, id, conversation, time.Now().UTC().Format(time.RFC3339Nano), summary); err != nil {
		t.Fatal(err)
	}
}

func seedArchiveSearchMessage(t *testing.T, store *ArchiveStore, id, conversation, content string, timestamp any) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO messages (id, conversation_id, role, content, timestamp)
		VALUES (?, ?, 'user', ?, ?)`, id, conversation, content, timestamp)
	if err != nil {
		t.Fatal(err)
	}
}

func assertSearchMessageIDs(t *testing.T, messages []Message, want ...string) {
	t.Helper()
	var got []string
	for _, msg := range messages {
		got = append(got, msg.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("message IDs = %v, want %v", got, want)
	}
}
