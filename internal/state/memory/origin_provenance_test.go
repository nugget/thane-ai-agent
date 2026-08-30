package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMessageOriginRoundTrip verifies the origin provenance stamp survives
// a write/read cycle through the SQLite store on both read paths, for every
// declared origin and the unstamped empty value. Origin is the structured
// contract that replaces sniffing transport envelopes out of message
// content to tell counterparty contact from internal wake prompts.
func TestMessageOriginRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		origin string
	}{
		{"channel", OriginChannel},
		{"wake", OriginWake},
		{"api", OriginAPI},
		{"internal", OriginInternal},
		{"unstamped", ""},
	}

	store := newWindowStore(t, 100)
	for _, tt := range tests {
		if err := store.AddMessage("conv-1", "user", "msg-"+tt.name, tt.origin); err != nil {
			t.Fatalf("AddMessage(%s): %v", tt.name, err)
		}
	}
	if err := store.AddMidTurnMessage("conv-1", "user", "msg-midturn", OriginChannel); err != nil {
		t.Fatalf("AddMidTurnMessage: %v", err)
	}

	check := func(path string, msgs []Message) {
		byContent := make(map[string]Message, len(msgs))
		for _, m := range msgs {
			byContent[m.Content] = m
		}
		for _, tt := range tests {
			m, ok := byContent["msg-"+tt.name]
			if !ok {
				t.Fatalf("%s: message %q missing", path, tt.name)
			}
			if m.Origin != tt.origin {
				t.Errorf("%s: origin = %q, want %q", path, m.Origin, tt.origin)
			}
		}
		mid, ok := byContent["msg-midturn"]
		if !ok {
			t.Fatalf("%s: mid-turn message missing", path)
		}
		if !mid.MidTurn || mid.Origin != OriginChannel {
			t.Errorf("%s: mid-turn row = (mid_turn=%v, origin=%q), want (true, %q) — the two stamps must compose",
				path, mid.MidTurn, mid.Origin, OriginChannel)
		}
	}
	check("GetMessages", store.GetMessages("conv-1"))
	check("GetAllMessages", store.GetAllMessages("conv-1"))
}

// TestMessageOriginNullLegacyRow pins the read paths against a row whose
// origin is NULL — the shape a pre-stamp row takes if the additive
// migration ever lost its empty-string DEFAULT. Without the COALESCE in
// every origin-bearing SELECT, scanning NULL into a string errors and
// the read loops silently drop the message.
func TestMessageOriginNullLegacyRow(t *testing.T) {
	store := newWindowStore(t, 100)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, origin)
		VALUES ('legacy', 'conv-1', 'user', 'legacy null row', ?, 5, NULL)
	`, base); err != nil {
		t.Fatalf("insert NULL row: %v", err)
	}

	find := func(path string, msgs []Message) {
		for _, m := range msgs {
			if m.Content == "legacy null row" {
				if m.Origin != "" {
					t.Errorf("%s: NULL origin read as %q, want empty", path, m.Origin)
				}
				return
			}
		}
		t.Fatalf("%s: NULL-origin row dropped from the read (silent message loss)", path)
	}
	find("GetMessages", store.GetMessages("conv-1"))
	find("GetAllMessages", store.GetAllMessages("conv-1"))
}

// TestMessageOriginInMemoryParity confirms the in-memory Store honors the
// same origin contract as the SQLite store.
func TestMessageOriginInMemoryParity(t *testing.T) {
	s := NewStore(100)
	if err := s.AddMessage("c", "user", "from-channel", OriginChannel); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.AddMidTurnMessage("c", "user", "mid", OriginWake); err != nil {
		t.Fatalf("AddMidTurnMessage: %v", err)
	}
	byContent := make(map[string]Message)
	for _, m := range s.GetMessages("c") {
		byContent[m.Content] = m
	}
	if got := byContent["from-channel"].Origin; got != OriginChannel {
		t.Errorf("origin = %q, want %q", got, OriginChannel)
	}
	if m := byContent["mid"]; !m.MidTurn || m.Origin != OriginWake {
		t.Errorf("mid-turn row = (mid_turn=%v, origin=%q), want (true, %q)", m.MidTurn, m.Origin, OriginWake)
	}
}

// TestMessageOriginCompactionRows verifies the compaction insert paths
// stamp their rows OriginInternal: ApplyCompaction and
// AddCompactionSummary write system rows directly rather than through
// addMessage, and a known system-authored row persisted as unstamped
// would violate the contract that empty means the enqueue site could
// not know.
func TestMessageOriginCompactionRows(t *testing.T) {
	store := newWindowStore(t, 100)

	if err := store.AddMessage("conv-1", "user", "to be compacted", OriginChannel); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	compacted := store.GetMessages("conv-1")
	if len(compacted) != 1 {
		t.Fatalf("seed message missing")
	}
	summaryTS := time.Now().Add(-time.Minute)
	if err := store.ApplyCompaction("conv-1", []string{compacted[0].ID}, CompactionSummaryPrefix+" the earlier exchange", summaryTS); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
	}
	if err := store.AddCompactionSummary("conv-1", CompactionSummaryPrefix+" session handoff"); err != nil {
		t.Fatalf("AddCompactionSummary: %v", err)
	}

	for _, m := range store.GetMessages("conv-1") {
		if m.Role == "system" && m.Origin != OriginInternal {
			t.Errorf("compaction row %q origin = %q, want %q", m.Content, m.Origin, OriginInternal)
		}
	}
}

// TestMessageOriginArchiveRoundTrip verifies origin survives the archive
// write and read paths in both storage modes: ImportMessages into the
// unified table read back through the session-transcript and search
// scanners, and legacy split-DB ArchiveMessages into archive_messages —
// including a NULL-origin legacy row surviving the legacy scanner.
func TestMessageOriginArchiveRoundTrip(t *testing.T) {
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	imported := []Message{
		{
			ID: "arch-1", ConversationID: "conv-arch", SessionID: "sess-arch",
			Role: "user", Content: "archived inbound greeting",
			Timestamp: base, Origin: OriginChannel,
		},
		{
			ID: "arch-2", ConversationID: "conv-arch", SessionID: "sess-arch",
			Role: "user", Content: "archived wake prompt",
			Timestamp: base.Add(time.Minute), Origin: OriginWake,
		},
	}

	t.Run("unified", func(t *testing.T) {
		store := newWindowStore(t, 100)
		archive, err := NewArchiveStoreFromDB(store.db, nil, nil)
		if err != nil {
			t.Fatalf("NewArchiveStoreFromDB: %v", err)
		}
		if err := archive.ImportMessages(imported); err != nil {
			t.Fatalf("ImportMessages: %v", err)
		}

		transcript, err := archive.GetSessionTranscript("sess-arch")
		if err != nil {
			t.Fatalf("GetSessionTranscript: %v", err)
		}
		assertOrigins(t, "transcript", transcript, map[string]string{
			"archived inbound greeting": OriginChannel,
			"archived wake prompt":      OriginWake,
		})

		results, err := archive.Search(SearchOptions{Query: "archived inbound", Limit: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) == 0 {
			t.Fatalf("search found nothing for the archived row")
		}
		if got := results[0].Match.Origin; got != OriginChannel {
			t.Errorf("search match origin = %q, want %q", got, OriginChannel)
		}
	})

	t.Run("legacy split-DB", func(t *testing.T) {
		archive, err := NewArchiveStore(t.TempDir()+"/archive.db", nil, nil, nil)
		if err != nil {
			t.Fatalf("NewArchiveStore: %v", err)
		}
		t.Cleanup(func() { _ = archive.Close() })

		if err := archive.ArchiveMessages(imported); err != nil {
			t.Fatalf("ArchiveMessages: %v", err)
		}
		// A pre-stamp legacy row: NULL origin must scan as "" rather
		// than dropping the message.
		if _, err := archive.DB().Exec(`
			INSERT INTO archive_messages (id, conversation_id, session_id, role, content, timestamp, archived_at, archive_reason, origin)
			VALUES ('arch-legacy', 'conv-arch', 'sess-arch', 'user', 'pre-stamp row', ?, ?, 'import', NULL)
		`, base.Add(2*time.Minute).Format(time.RFC3339Nano), base.Add(2*time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("insert NULL-origin legacy row: %v", err)
		}

		transcript, err := archive.GetSessionTranscript("sess-arch")
		if err != nil {
			t.Fatalf("GetSessionTranscript: %v", err)
		}
		assertOrigins(t, "legacy transcript", transcript, map[string]string{
			"archived inbound greeting": OriginChannel,
			"archived wake prompt":      OriginWake,
			"pre-stamp row":             "",
		})
	})
}

// assertOrigins checks each expected content string is present with the
// expected origin.
func assertOrigins(t *testing.T, path string, msgs []Message, want map[string]string) {
	t.Helper()
	byContent := make(map[string]Message, len(msgs))
	for _, m := range msgs {
		byContent[m.Content] = m
	}
	for content, origin := range want {
		m, ok := byContent[content]
		if !ok {
			t.Errorf("%s: message %q missing", path, content)
			continue
		}
		if m.Origin != origin {
			t.Errorf("%s: %q origin = %q, want %q", path, content, m.Origin, origin)
		}
	}
}

// TestMessageOriginJSONContract locks the wire shape: origin appears only
// when stamped, so unstamped rows stay unchanged for existing consumers.
func TestMessageOriginJSONContract(t *testing.T) {
	on, err := json.Marshal(Message{Role: "user", Content: "x", Origin: OriginChannel})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(on), `"origin":"channel"`) {
		t.Errorf("stamped origin not surfaced in JSON: %s", on)
	}
	off, err := json.Marshal(Message{Role: "user", Content: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(off), "origin") {
		t.Errorf("origin should be omitted when unstamped (omitempty): %s", off)
	}
}
