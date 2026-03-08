package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// mockInjector records InjectSystemMessage calls.
type mockInjector struct {
	alive    bool
	messages []injectedMsg
}

type injectedMsg struct {
	conversationID string
	message        string
}

func (m *mockInjector) InjectSystemMessage(conversationID, message string) error {
	m.messages = append(m.messages, injectedMsg{conversationID, message})
	return nil
}

func (m *mockInjector) IsSessionAlive(_ string) bool {
	return m.alive
}

// mockDelegateSpawner records Spawn calls.
type mockDelegateSpawner struct {
	spawns []delegateSpawn
}

type delegateSpawn struct {
	task     string
	guidance string
}

func (m *mockDelegateSpawner) Spawn(_ context.Context, task, guidance string) error {
	m.spawns = append(m.spawns, delegateSpawn{task, guidance})
	return nil
}

func newTestDispatcher(t *testing.T) (*CallbackDispatcher, *RecordStore, *mockInjector, *mockDelegateSpawner) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewRecordStore(filepath.Join(dir, "test.db"), slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	inj := &mockInjector{alive: true}
	del := &mockDelegateSpawner{}
	dispatcher := NewCallbackDispatcher(store, inj, del, slog.Default())
	return dispatcher, store, inj, del
}

func makeCallback(action string) []byte {
	p := callbackPayload{Action: action, Timestamp: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(p)
	return b
}

func seedRecord(t *testing.T, store *RecordStore, requestID, conversation string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID:          requestID,
		Recipient:          "nugget",
		OriginConversation: conversation,
		Context:            "Test context",
		Actions:            []Action{{ID: "approve", Label: "Yes"}, {ID: "deny", Label: "No"}},
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := store.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestCallbackDispatcher_LiveSession(t *testing.T) {
	dispatcher, store, inj, del := newTestDispatcher(t)
	inj.alive = true
	seedRecord(t, store, "01234567-89ab-cdef-0123-456789abcdef", "signal-15551234567")

	dispatcher.Handle("thane/callbacks", makeCallback("THANE_01234567-89ab-cdef-0123-456789abcdef_approve"))

	rec, _ := store.Get("01234567-89ab-cdef-0123-456789abcdef")
	if rec.Status != StatusResponded {
		t.Errorf("Status = %q, want %q", rec.Status, StatusResponded)
	}
	if rec.ResponseAction != "approve" {
		t.Errorf("ResponseAction = %q, want %q", rec.ResponseAction, "approve")
	}

	if len(inj.messages) != 1 {
		t.Fatalf("expected 1 injected message, got %d", len(inj.messages))
	}
	if inj.messages[0].conversationID != "signal-15551234567" {
		t.Errorf("conversationID = %q, want %q", inj.messages[0].conversationID, "signal-15551234567")
	}
	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_DeadSession(t *testing.T) {
	dispatcher, store, inj, del := newTestDispatcher(t)
	inj.alive = false
	seedRecord(t, store, "01234567-89ab-cdef-0123-456789abcdef", "signal-15551234567")

	dispatcher.Handle("thane/callbacks", makeCallback("THANE_01234567-89ab-cdef-0123-456789abcdef_deny"))

	rec, _ := store.Get("01234567-89ab-cdef-0123-456789abcdef")
	if rec.Status != StatusResponded {
		t.Errorf("Status = %q, want %q", rec.Status, StatusResponded)
	}

	if len(del.spawns) != 1 {
		t.Fatalf("expected 1 delegate spawn, got %d", len(del.spawns))
	}
	if len(inj.messages) != 0 {
		t.Errorf("expected 0 injected messages, got %d", len(inj.messages))
	}
}

func TestCallbackDispatcher_UnknownRequestID(t *testing.T) {
	dispatcher, _, _, del := newTestDispatcher(t)

	dispatcher.Handle("thane/callbacks", makeCallback("THANE_99999999-0000-0000-0000-000000000000_approve"))

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for unknown ID, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_AlreadyResponded(t *testing.T) {
	dispatcher, store, inj, del := newTestDispatcher(t)
	seedRecord(t, store, "01234567-89ab-cdef-0123-456789abcdef", "conv-1")

	// First callback.
	dispatcher.Handle("thane/callbacks", makeCallback("THANE_01234567-89ab-cdef-0123-456789abcdef_approve"))
	// Second callback should be ignored.
	dispatcher.Handle("thane/callbacks", makeCallback("THANE_01234567-89ab-cdef-0123-456789abcdef_deny"))

	rec, _ := store.Get("01234567-89ab-cdef-0123-456789abcdef")
	if rec.ResponseAction != "approve" {
		t.Errorf("ResponseAction = %q, want %q (first response wins)", rec.ResponseAction, "approve")
	}
	// Only one injection from the first callback.
	if len(inj.messages) != 1 {
		t.Errorf("expected 1 injected message, got %d", len(inj.messages))
	}
	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_MalformedPayload(t *testing.T) {
	dispatcher, _, _, del := newTestDispatcher(t)

	dispatcher.Handle("thane/callbacks", []byte("not json"))

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for malformed payload, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_NonThaneAction(t *testing.T) {
	dispatcher, _, _, del := newTestDispatcher(t)

	dispatcher.Handle("thane/callbacks", makeCallback("SOME_OTHER_ACTION"))

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for non-THANE action, got %d", len(del.spawns))
	}
}

func TestParseCallbackAction(t *testing.T) {
	tests := []struct {
		input     string
		wantReqID string
		wantActID string
		wantOK    bool
	}{
		{
			input:     "THANE_01234567-89ab-cdef-0123-456789abcdef_approve",
			wantReqID: "01234567-89ab-cdef-0123-456789abcdef",
			wantActID: "approve",
			wantOK:    true,
		},
		{
			input:     "THANE_01234567-89ab-cdef-0123-456789abcdef_multi_part_action",
			wantReqID: "01234567-89ab-cdef-0123-456789abcdef",
			wantActID: "multi_part_action",
			wantOK:    true,
		},
		{input: "THANE_short", wantOK: false},
		{input: "THANE_01234567-89ab-cdef-0123-456789abcdef", wantOK: false},                        // no action ID
		{input: "THANE_01234567-89ab-cdef-0123-456789abcdefXapprove", wantOK: false},                // no underscore separator
		{input: "THANE_01234567-89ab-cdef-0123-456789abcdef_", wantOK: false},                       // empty action ID
		{input: "OTHER_01234567-89ab-cdef-0123-456789abcdef_approve", wantReqID: "", wantOK: false}, // wrong prefix handled by caller
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			reqID, actID, ok := parseCallbackAction(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if reqID != tt.wantReqID {
				t.Errorf("requestID = %q, want %q", reqID, tt.wantReqID)
			}
			if actID != tt.wantActID {
				t.Errorf("actionID = %q, want %q", actID, tt.wantActID)
			}
		})
	}
}
