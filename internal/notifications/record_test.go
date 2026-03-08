package notifications

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func newTestRecordStore(t *testing.T) *RecordStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_notifications.db")
	s, err := NewRecordStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordStore_CreateAndGet(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID:          "req-001",
		Recipient:          "nugget",
		OriginSession:      "sess-abc",
		OriginConversation: "conv-xyz",
		Context:            "Approve email to boss?",
		Actions:            []Action{{ID: "approve", Label: "Yes"}, {ID: "deny", Label: "No"}},
		TimeoutSeconds:     1800,
		TimeoutAction:      "deny",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}

	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get("req-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.RequestID != "req-001" {
		t.Errorf("RequestID = %q, want %q", got.RequestID, "req-001")
	}
	if got.Recipient != "nugget" {
		t.Errorf("Recipient = %q, want %q", got.Recipient, "nugget")
	}
	if got.OriginSession != "sess-abc" {
		t.Errorf("OriginSession = %q, want %q", got.OriginSession, "sess-abc")
	}
	if got.OriginConversation != "conv-xyz" {
		t.Errorf("OriginConversation = %q, want %q", got.OriginConversation, "conv-xyz")
	}
	if got.Context != "Approve email to boss?" {
		t.Errorf("Context = %q, want %q", got.Context, "Approve email to boss?")
	}
	if got.Status != StatusPending {
		t.Errorf("Status = %q, want %q", got.Status, StatusPending)
	}
	if got.TimeoutSeconds != 1800 {
		t.Errorf("TimeoutSeconds = %d, want %d", got.TimeoutSeconds, 1800)
	}
	if got.TimeoutAction != "deny" {
		t.Errorf("TimeoutAction = %q, want %q", got.TimeoutAction, "deny")
	}
	if len(got.Actions) != 2 {
		t.Fatalf("Actions len = %d, want 2", len(got.Actions))
	}
	if got.Actions[0].ID != "approve" || got.Actions[0].Label != "Yes" {
		t.Errorf("Actions[0] = %+v, want {approve, Yes}", got.Actions[0])
	}
	if got.Actions[1].ID != "deny" || got.Actions[1].Label != "No" {
		t.Errorf("Actions[1] = %+v, want {deny, No}", got.Actions[1])
	}
}

func TestRecordStore_GetNotFound(t *testing.T) {
	s := newTestRecordStore(t)

	_, err := s.Get("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("Get(nonexistent) error = %v, want sql.ErrNoRows", err)
	}
}

func TestRecordStore_Respond(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID: "req-002",
		Recipient: "nugget",
		Actions:   []Action{{ID: "approve", Label: "Yes"}},
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := s.Respond("req-002", "approve")
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if !updated {
		t.Error("Respond should return true for pending record")
	}

	got, err := s.Get("req-002")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusResponded {
		t.Errorf("Status = %q, want %q", got.Status, StatusResponded)
	}
	if got.ResponseAction != "approve" {
		t.Errorf("ResponseAction = %q, want %q", got.ResponseAction, "approve")
	}
	if got.RespondedAt.IsZero() {
		t.Error("RespondedAt should be set")
	}
}

func TestRecordStore_RespondAlreadyResponded(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID: "req-003",
		Recipient: "nugget",
		Actions:   []Action{{ID: "approve", Label: "Yes"}, {ID: "deny", Label: "No"}},
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First response.
	updated, err := s.Respond("req-003", "approve")
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if !updated {
		t.Error("first Respond should return true")
	}

	// Second response should be a no-op (not an error), returning false.
	updated, err = s.Respond("req-003", "deny")
	if err != nil {
		t.Fatalf("second Respond: %v", err)
	}
	if updated {
		t.Error("second Respond should return false (already responded)")
	}

	got, err := s.Get("req-003")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Should still reflect the first response.
	if got.ResponseAction != "approve" {
		t.Errorf("ResponseAction = %q, want %q (first response should win)", got.ResponseAction, "approve")
	}
}

func TestRecordStore_Expire(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID: "req-004",
		Recipient: "nugget",
		Actions:   []Action{{ID: "ok", Label: "OK"}},
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := s.Expire("req-004")
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if !updated {
		t.Error("Expire should return true for pending record")
	}

	got, err := s.Get("req-004")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusExpired {
		t.Errorf("Status = %q, want %q", got.Status, StatusExpired)
	}
}

func TestRecordStore_ExpireAlreadyExpired(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID: "req-005",
		Recipient: "nugget",
		Actions:   []Action{{ID: "ok", Label: "OK"}},
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := s.Expire("req-005")
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if !updated {
		t.Error("first Expire should return true")
	}
	// Second expire is a no-op, returning false.
	updated, err = s.Expire("req-005")
	if err != nil {
		t.Fatalf("second Expire: %v", err)
	}
	if updated {
		t.Error("second Expire should return false (already expired)")
	}

	got, _ := s.Get("req-005")
	if got.Status != StatusExpired {
		t.Errorf("Status = %q, want %q", got.Status, StatusExpired)
	}
}

func TestRecordStore_PendingExpired(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)

	// Record that expired 5 minutes ago.
	expired := &Record{
		RequestID: "req-expired",
		Recipient: "nugget",
		Actions:   []Action{{ID: "ok", Label: "OK"}},
		CreatedAt: now.Add(-35 * time.Minute),
		ExpiresAt: now.Add(-5 * time.Minute),
	}
	// Record that expires in the future.
	future := &Record{
		RequestID: "req-future",
		Recipient: "nugget",
		Actions:   []Action{{ID: "ok", Label: "OK"}},
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
	}
	// Record that expired but is already responded.
	responded := &Record{
		RequestID: "req-responded",
		Recipient: "nugget",
		Actions:   []Action{{ID: "ok", Label: "OK"}},
		CreatedAt: now.Add(-35 * time.Minute),
		ExpiresAt: now.Add(-5 * time.Minute),
	}

	for _, r := range []*Record{expired, future, responded} {
		if err := s.Create(r); err != nil {
			t.Fatalf("Create(%s): %v", r.RequestID, err)
		}
	}
	// Mark one as responded.
	if _, err := s.Respond("req-responded", "ok"); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	got, err := s.PendingExpired()
	if err != nil {
		t.Fatalf("PendingExpired: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("PendingExpired len = %d, want 1", len(got))
	}
	if got[0].RequestID != "req-expired" {
		t.Errorf("PendingExpired[0].RequestID = %q, want %q", got[0].RequestID, "req-expired")
	}
}

func TestRecordStore_PendingExpiredEmpty(t *testing.T) {
	s := newTestRecordStore(t)

	got, err := s.PendingExpired()
	if err != nil {
		t.Fatalf("PendingExpired: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("PendingExpired len = %d, want 0", len(got))
	}
}

func TestNewRecordStore_InvalidPath(t *testing.T) {
	_, err := NewRecordStore(filepath.Join(os.DevNull, "impossible", "path.db"), slog.Default())
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}
