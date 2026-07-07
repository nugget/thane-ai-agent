package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMidTurnMessageRoundTrip verifies the mid_turn provenance flag survives a
// write/read cycle through the SQLite store on both read paths, and that an
// ordinary message is never flagged (#1230): the structured contract that
// replaces substring-matching the rendered arrival marker.
func TestMidTurnMessageRoundTrip(t *testing.T) {
	store := newWindowStore(t, 100)

	if err := store.AddMessage("conv-1", "user", "ordinary"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := store.AddMidTurnMessage("conv-1", "user", "mid-turn arrival"); err != nil {
		t.Fatalf("AddMidTurnMessage: %v", err)
	}

	check := func(path string, msgs []Message) {
		byContent := make(map[string]Message, len(msgs))
		for _, m := range msgs {
			byContent[m.Content] = m
		}
		if _, ok := byContent["ordinary"]; !ok {
			t.Fatalf("%s: ordinary message missing", path)
		}
		if byContent["ordinary"].MidTurn {
			t.Errorf("%s: ordinary message flagged mid_turn", path)
		}
		if !byContent["mid-turn arrival"].MidTurn {
			t.Errorf("%s: mid-turn message not flagged mid_turn", path)
		}
	}
	check("GetMessages", store.GetMessages("conv-1"))
	check("GetAllMessages", store.GetAllMessages("conv-1"))
}

// TestMidTurnMessageNullLegacyRow pins the read paths against a row whose
// mid_turn is NULL — the shape a legacy row would take if the additive
// migration ever lost its DEFAULT 0. Scanning NULL into an int errors, and the
// read loops drop such rows on scan error, so without COALESCE(mid_turn, 0)
// this would silently lose the message from the conversation window.
func TestMidTurnMessageNullLegacyRow(t *testing.T) {
	store := newWindowStore(t, 100)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, err := store.db.Exec(`
		INSERT INTO messages (id, conversation_id, role, content, timestamp, token_count, mid_turn)
		VALUES ('legacy', 'conv-1', 'user', 'legacy null row', ?, 5, NULL)
	`, base); err != nil {
		t.Fatalf("insert NULL row: %v", err)
	}

	find := func(path string, msgs []Message) {
		for _, m := range msgs {
			if m.Content == "legacy null row" {
				if m.MidTurn {
					t.Errorf("%s: NULL mid_turn read as true, want false", path)
				}
				return
			}
		}
		t.Fatalf("%s: NULL-mid_turn row dropped from the read (silent message loss)", path)
	}
	find("GetMessages", store.GetMessages("conv-1"))
	find("GetAllMessages", store.GetAllMessages("conv-1"))
}

// TestMidTurnMessageInMemoryParity confirms the in-memory Store honors the same
// AddMidTurnMessage contract as the SQLite store.
func TestMidTurnMessageInMemoryParity(t *testing.T) {
	s := NewStore(100)
	if err := s.AddMessage("c", "user", "ordinary"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.AddMidTurnMessage("c", "user", "mid"); err != nil {
		t.Fatalf("AddMidTurnMessage: %v", err)
	}
	byContent := make(map[string]Message)
	for _, m := range s.GetMessages("c") {
		byContent[m.Content] = m
	}
	if byContent["ordinary"].MidTurn {
		t.Errorf("ordinary message flagged mid_turn")
	}
	if !byContent["mid"].MidTurn {
		t.Errorf("mid-turn message not flagged mid_turn")
	}
}

// TestMidTurnMessageJSONContract locks the wire shape the conversation API
// (writeJSON of memory.Conversation) exposes: mid_turn is present only when
// true, so ordinary messages stay unchanged for existing consumers.
func TestMidTurnMessageJSONContract(t *testing.T) {
	on, err := json.Marshal(Message{Role: "user", Content: "x", MidTurn: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(on), `"mid_turn":true`) {
		t.Errorf("mid_turn=true not surfaced in JSON: %s", on)
	}
	off, err := json.Marshal(Message{Role: "user", Content: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(off), "mid_turn") {
		t.Errorf("mid_turn should be omitted when false (omitempty): %s", off)
	}
}
