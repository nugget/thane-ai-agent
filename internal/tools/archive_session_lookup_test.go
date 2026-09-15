package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

const (
	// lookupOldSessionID is an archived session older than the 100
	// newest, the window the previous prefix scan was limited to.
	lookupOldSessionID = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	// lookupSharedPrefix begins the ids of lookupSharedCount sessions,
	// the way one import batch shares a leading id segment.
	lookupSharedPrefix = "01a1bbbb"
	lookupSharedCount  = 7
)

func lookupSharedSessionID(i int) string {
	return fmt.Sprintf("%s-0000-7000-8000-%012d", lookupSharedPrefix, i)
}

// newSessionLookupFixture seeds an archive with one old session that
// has a transcript, 100 newer sessions, and a batch of sessions without
// messages that share one id prefix.
func newSessionLookupFixture(t *testing.T) *Registry {
	t.Helper()
	working, err := memory.NewSQLiteStore(t.TempDir()+"/working.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = working.Close() })
	store, err := memory.NewArchiveStoreFromDB(working.DB(), nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStoreFromDB: %v", err)
	}
	r := NewEmptyRegistry()
	r.SetArchiveStore(store)

	db := working.DB()
	now := time.Now().UTC()
	old := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	insertLookupSession(t, db, lookupOldSessionID, old, "Alice plans the garden")
	insertLookupMessage(t, db, lookupOldSessionID, "old session message", old.Add(time.Minute))
	for i := range 100 {
		insertLookupSession(t, db, fmt.Sprintf("01a00000-0000-7000-8000-%012d", i), now.Add(-time.Duration(i)*time.Minute), "recent "+itoa(i))
	}
	batch := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	for i := range lookupSharedCount {
		insertLookupSession(t, db, lookupSharedSessionID(i), batch.Add(time.Duration(i)*time.Hour), "Bob import "+itoa(i))
	}
	return r
}

func insertLookupSession(t *testing.T, db *sql.DB, id string, startedAt time.Time, title string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (id, conversation_id, started_at, message_count, title) VALUES (?, 'conv-1', ?, 0, ?)`,
		id, startedAt.Format(time.RFC3339Nano), title); err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

func insertLookupMessage(t *testing.T, db *sql.DB, sessionID, content string, ts time.Time) {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp, status) VALUES (?, 'conv-1', ?, 'user', ?, ?, 'active')`,
		id.String(), sessionID, content, ts); err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

func TestArchiveSessionTranscriptTool_ResolvesSessionID(t *testing.T) {
	r := newSessionLookupFixture(t)
	tool := r.Get("archive_session_transcript")

	tests := []struct {
		name string
		arg  string
		// wantContent is the first message's content on success; empty
		// with no wantErr means an existing session with no messages.
		wantContent string
		wantErr     []string
		notInErr    []string
	}{
		{name: "full id", arg: lookupOldSessionID, wantContent: "old session message"},
		{name: "eight-character prefix older than the 100 newest", arg: "0190aaaa", wantContent: "old session message"},
		{name: "thirteen-character prefix", arg: "0190aaaa-bbbb", wantContent: "old session message"},
		{name: "thirty-five-character prefix", arg: lookupOldSessionID[:35], wantContent: "old session message"},
		{name: "prefix without hyphens", arg: "0190aaaabbbb7ccc", wantContent: "old session message"},
		{name: "uppercase prefix", arg: "0190AAAA-BBBB", wantContent: "old session message"},
		{name: "citation form", arg: "archive:session:" + lookupOldSessionID, wantContent: "old session message"},
		{name: "existing session without messages", arg: lookupSharedSessionID(3)},
		{
			name: "shared prefix lists bounded candidates",
			arg:  lookupSharedPrefix,
			wantErr: []string{
				`session_id "01a1bbbb" matches 7 archived sessions`,
				`"session_id":"` + lookupSharedSessionID(0) + `","started_at":"2025-06-01T12:00:00Z","title":"Bob import 0"`,
				lookupSharedSessionID(4),
				"(2 more not listed)",
				"Retry with the full session_id",
				"archive_search for words from the conversation itself",
			},
			notInErr: []string{lookupSharedSessionID(5), lookupSharedSessionID(6)},
		},
		{
			name:    "no match",
			arg:     "0fffffff",
			wantErr: []string{`no archived session has an id beginning "0fffffff"`, "archive_search"},
		},
		{
			name:    "full id that names no session",
			arg:     "0190aaaa-bbbb-7ccc-8ddd-000000000000",
			wantErr: []string{`no archived session has id "0190aaaa-bbbb-7ccc-8ddd-000000000000"`, "re-import", "archive_search"},
		},
		{
			name:    "non-hex character",
			arg:     "0190aaaa_bbbb",
			wantErr: []string{"session_id: malformed session id", `character '_' at byte 9 is not a hex digit or hyphen`, "8-4-4-4-12"},
		},
		{
			name:    "more digits than a full id",
			arg:     lookupOldSessionID + "0",
			wantErr: []string{"it has 33 hex digits and a full session id has 32"},
		},
		{name: "empty", arg: "  ", wantErr: []string{"session_id is required"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tool.Handler(context.Background(), map[string]any{"session_id": tt.arg})
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("handler(%q) = %s, want error", tt.arg, out)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error missing %q:\n%s", want, err)
					}
				}
				for _, unwanted := range tt.notInErr {
					if strings.Contains(err.Error(), unwanted) {
						t.Errorf("error lists %q beyond the candidate bound:\n%s", unwanted, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("handler(%q): %v", tt.arg, err)
			}
			var parsed struct {
				Messages []memory.MessageView `json:"messages"`
			}
			if err := json.Unmarshal([]byte(out), &parsed); err != nil {
				t.Fatalf("unmarshal: %v\noutput: %s", err, out)
			}
			if tt.wantContent == "" {
				if len(parsed.Messages) != 0 {
					t.Fatalf("messages = %d, want none", len(parsed.Messages))
				}
				return
			}
			if len(parsed.Messages) != 1 || parsed.Messages[0].Content != tt.wantContent {
				t.Fatalf("messages = %+v, want one with content %q", parsed.Messages, tt.wantContent)
			}
			if parsed.Messages[0].SessionID != lookupOldSessionID {
				t.Fatalf("session_id = %q, want the full id %q", parsed.Messages[0].SessionID, lookupOldSessionID)
			}
		})
	}
}
