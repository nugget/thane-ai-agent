package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
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

// mockDelegateSpawner records Spawn calls and signals via a channel
// so tests can wait for the async goroutine in DispatchAction.
type mockDelegateSpawner struct {
	spawns []delegateSpawn
	done   chan struct{}
}

type delegateSpawn struct {
	task     string
	guidance string
}

func newMockDelegateSpawner() *mockDelegateSpawner {
	return &mockDelegateSpawner{done: make(chan struct{}, 10)}
}

func (m *mockDelegateSpawner) Spawn(_ context.Context, task, guidance string) error {
	m.spawns = append(m.spawns, delegateSpawn{task, guidance})
	m.done <- struct{}{}
	return nil
}

// waitSpawn blocks until one spawn completes or the timeout elapses.
func (m *mockDelegateSpawner) waitSpawn(t *testing.T) {
	t.Helper()
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delegate spawn")
	}
}

func newTestDispatcher(t *testing.T) (*CallbackDispatcher, *RecordStore, *mockInjector, *mockDelegateSpawner) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}

	inj := &mockInjector{alive: true}
	del := newMockDelegateSpawner()
	dispatcher := NewCallbackDispatcher(store, inj, del, "test-thane", slog.Default())
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

	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_approve"))

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

	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_deny"))

	rec, _ := store.Get("01234567-89ab-cdef-0123-456789abcdef")
	if rec.Status != StatusResponded {
		t.Errorf("Status = %q, want %q", rec.Status, StatusResponded)
	}

	// Delegate spawn runs in a goroutine — wait for it.
	del.waitSpawn(t)
	if len(del.spawns) != 1 {
		t.Fatalf("expected 1 delegate spawn, got %d", len(del.spawns))
	}
	if len(inj.messages) != 0 {
		t.Errorf("expected 0 injected messages, got %d", len(inj.messages))
	}
}

func TestCallbackDispatcher_UnknownRequestID(t *testing.T) {
	dispatcher, _, _, del := newTestDispatcher(t)

	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("TEST_THANE_99999999-0000-0000-0000-000000000000_approve"))

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for unknown ID, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_AlreadyResponded(t *testing.T) {
	dispatcher, store, inj, del := newTestDispatcher(t)
	seedRecord(t, store, "01234567-89ab-cdef-0123-456789abcdef", "conv-1")

	// First callback.
	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_approve"))
	// Second callback should be ignored.
	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_deny"))

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

	dispatcher.Handle("thane/test-thane/callbacks", []byte("not json"))

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for malformed payload, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_WrongPrefix(t *testing.T) {
	dispatcher, _, _, del := newTestDispatcher(t)

	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("SOME_OTHER_ACTION"))

	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns for wrong prefix, got %d", len(del.spawns))
	}
}

func TestCallbackDispatcher_InvalidActionID(t *testing.T) {
	dispatcher, store, inj, del := newTestDispatcher(t)
	inj.alive = true
	seedRecord(t, store, "01234567-89ab-cdef-0123-456789abcdef", "conv-1")

	// "bogus" is not one of the declared actions (approve, deny).
	dispatcher.Handle("thane/test-thane/callbacks", makeCallback("TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_bogus"))

	rec, _ := store.Get("01234567-89ab-cdef-0123-456789abcdef")
	if rec.Status != StatusPending {
		t.Errorf("Status = %q, want %q (invalid action should not update)", rec.Status, StatusPending)
	}
	if len(inj.messages) != 0 {
		t.Errorf("expected 0 injected messages, got %d", len(inj.messages))
	}
	if len(del.spawns) != 0 {
		t.Errorf("expected 0 delegate spawns, got %d", len(del.spawns))
	}
}

func TestParseCallbackAction(t *testing.T) {
	tests := []struct {
		input     string
		prefix    string
		wantReqID string
		wantActID string
		wantOK    bool
	}{
		{
			input:     "TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_approve",
			prefix:    "TEST_THANE",
			wantReqID: "01234567-89ab-cdef-0123-456789abcdef",
			wantActID: "approve",
			wantOK:    true,
		},
		{
			input:     "TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_multi_part_action",
			prefix:    "TEST_THANE",
			wantReqID: "01234567-89ab-cdef-0123-456789abcdef",
			wantActID: "multi_part_action",
			wantOK:    true,
		},
		{input: "TEST_THANE_short", prefix: "TEST_THANE", wantOK: false},
		{input: "TEST_THANE_01234567-89ab-cdef-0123-456789abcdef", prefix: "TEST_THANE", wantOK: false},         // no action ID
		{input: "TEST_THANE_01234567-89ab-cdef-0123-456789abcdefXapprove", prefix: "TEST_THANE", wantOK: false}, // no underscore separator
		{input: "TEST_THANE_01234567-89ab-cdef-0123-456789abcdef_", prefix: "TEST_THANE", wantOK: false},        // empty action ID
		{input: "OTHER_01234567-89ab-cdef-0123-456789abcdef_approve", prefix: "TEST_THANE", wantOK: false},      // wrong prefix
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			reqID, actID, ok := parseCallbackAction(tt.input, tt.prefix)
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

func TestActionPrefix(t *testing.T) {
	tests := []struct {
		deviceName string
		want       string
	}{
		{"aimee-thane", "AIMEE_THANE"},
		{"thane", "THANE"},
		{"", "THANE"},
		{"my-long-device-name", "MY_LONG_DEVICE_NAME"},
		{"ALREADY_UPPER", "ALREADY_UPPER"},
	}

	for _, tt := range tests {
		t.Run(tt.deviceName, func(t *testing.T) {
			got := ActionPrefix(tt.deviceName)
			if got != tt.want {
				t.Errorf("ActionPrefix(%q) = %q, want %q", tt.deviceName, got, tt.want)
			}
		})
	}
}
