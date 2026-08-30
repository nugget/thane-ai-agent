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
