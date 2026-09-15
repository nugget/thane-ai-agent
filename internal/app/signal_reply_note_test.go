package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// TestSignalReplyNoteRecorder pins the store write behind the Signal
// bridge's RecordNote hook against a real SQLite store. The note must
// land in the named conversation as a system row with OriginInternal,
// byte for byte, and later turns must read it as a framed memory note,
// not as something the assistant said on Signal.
func TestSignalReplyNoteRecorder(t *testing.T) {
	tests := []struct {
		name           string
		conversationID string
		note           string
	}{
		{
			name:           "held reply",
			conversationID: "signal-15551234567",
			note:           `{"kind":"signal_reply_held","signal_message_sent":false,"reason":"answered garage-watch; sensor glitch","final_text_withheld":true}`,
		},
		{
			name:           "reply not sent",
			conversationID: "signal-15557654321",
			note:           `{"kind":"signal_reply_not_sent","signal_message_sent":false,"cause":"the wake turn ended with no final text and no signal_hold_reply call","final_text_withheld":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := memory.NewSQLiteStore(t.TempDir()+"/working.db", 100)
			if err != nil {
				t.Fatalf("NewSQLiteStore: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			if err := signalReplyNoteRecorder(store)(tt.conversationID, tt.note); err != nil {
				t.Fatalf("record note: %v", err)
			}

			rows := store.GetMessages(tt.conversationID)
			if len(rows) != 1 {
				t.Fatalf("rows = %+v, want exactly the note", rows)
			}
			row := rows[0]
			if row.Role != "system" {
				t.Errorf("role = %q, want system: any other role renders the note as something said on the thread", row.Role)
			}
			if row.Origin != memory.OriginInternal {
				t.Errorf("origin = %q, want %q", row.Origin, memory.OriginInternal)
			}
			if row.Content != tt.note {
				t.Errorf("content = %q, want the note unchanged", row.Content)
			}
			if other := store.GetMessages("signal-other"); len(other) != 0 {
				t.Errorf("note leaked into another conversation: %+v", other)
			}

			rendered, ok := memory.FormatStoredHistoryMessage(row, time.Now())
			if !ok {
				t.Fatal("stored note does not render into history")
			}
			for _, want := range []string{"stored conversation memory note", "original_role=system", "not_active_instruction=true", tt.note} {
				if !strings.Contains(rendered.Content, want) {
					t.Errorf("rendered history missing %q:\n%s", want, rendered.Content)
				}
			}
		})
	}
}

type failingNoteStore struct{ err error }

func (s failingNoteStore) AddMessage(string, string, string, string) error { return s.err }

// TestSignalReplyNoteRecorder_ReturnsStoreError pins that a failed write
// reaches the bridge, which logs it, instead of vanishing.
func TestSignalReplyNoteRecorder_ReturnsStoreError(t *testing.T) {
	storeErr := errors.New("database is locked")
	if err := signalReplyNoteRecorder(failingNoteStore{err: storeErr})("signal-1", `{"kind":"signal_reply_held"}`); !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want the store's error", err)
	}
}
