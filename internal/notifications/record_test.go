package notifications

import (
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/database"
)

func newTestRecordStore(t *testing.T) *RecordStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}
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

func TestNewRecordStore_NilDB(t *testing.T) {
	_, err := NewRecordStore(nil, slog.Default())
	if err == nil {
		t.Fatal("expected error for nil DB")
	}
}

func TestRecordStore_CreateWithHistoryFields(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	r := &Record{
		RequestID: "req-hist-001",
		Recipient: "nugget",
		Actions:   []Action{{ID: "approve", Label: "Yes"}},
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
		Channel:   "ha_push",
		Source:    "metacognitive",
		Kind:      KindActionable,
		Title:     "Lock battery",
		Message:   "Front-door lock battery warning is active",
	}
	if err := s.Create(r); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get("req-hist-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Channel != "ha_push" {
		t.Errorf("Channel = %q, want %q", got.Channel, "ha_push")
	}
	if got.Source != "metacognitive" {
		t.Errorf("Source = %q, want %q", got.Source, "metacognitive")
	}
	if got.Kind != KindActionable {
		t.Errorf("Kind = %q, want %q", got.Kind, KindActionable)
	}
	if got.Title != "Lock battery" {
		t.Errorf("Title = %q, want %q", got.Title, "Lock battery")
	}
	if got.Message != "Front-door lock battery warning is active" {
		t.Errorf("Message = %q, want %q", got.Message, "Front-door lock battery warning is active")
	}
}

func TestRecordStore_LogAndRecent(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)

	// Log two fire-and-forget notifications at different times.
	old := &Record{
		RequestID: "ff-001",
		Recipient: "nugget",
		Channel:   "ha_push",
		Source:    "metacognitive",
		Title:     "Lock battery",
		Message:   "Battery warning active",
		CreatedAt: now.Add(-2 * time.Hour),
	}
	recent := &Record{
		RequestID: "ff-002",
		Recipient: "nugget",
		Channel:   "signal",
		Source:    "signal/+15125551234",
		Title:     "Weather alert",
		Message:   "Storm warning",
		CreatedAt: now.Add(-30 * time.Minute),
	}
	for _, r := range []*Record{old, recent} {
		if err := s.Log(r); err != nil {
			t.Fatalf("Log(%s): %v", r.RequestID, err)
		}
	}

	// Query recent (last 1 hour) — should only return the recent one.
	got, err := s.Recent(now.Add(-1*time.Hour), 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent len = %d, want 1", len(got))
	}
	if got[0].RequestID != "ff-002" {
		t.Errorf("Recent[0].RequestID = %q, want %q", got[0].RequestID, "ff-002")
	}
	if got[0].Status != StatusSent {
		t.Errorf("Status = %q, want %q", got[0].Status, StatusSent)
	}
	if got[0].Kind != KindFireAndForget {
		t.Errorf("Kind = %q, want %q", got[0].Kind, KindFireAndForget)
	}
	if got[0].Channel != "signal" {
		t.Errorf("Channel = %q, want %q", got[0].Channel, "signal")
	}
	if got[0].Source != "signal/+15125551234" {
		t.Errorf("Source = %q, want %q", got[0].Source, "signal/+15125551234")
	}

	// Query with wider window — should return both, newest first.
	got, err = s.Recent(now.Add(-3*time.Hour), 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent len = %d, want 2", len(got))
	}
	if got[0].RequestID != "ff-002" {
		t.Errorf("Recent[0] = %q, want newest first (ff-002)", got[0].RequestID)
	}
}

func TestRecordStore_RecentMixed(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)

	// Create an actionable notification.
	actionable := &Record{
		RequestID: "act-001",
		Recipient: "nugget",
		Actions:   []Action{{ID: "yes", Label: "Yes"}},
		CreatedAt: now.Add(-1 * time.Hour),
		ExpiresAt: now.Add(time.Hour),
		Channel:   "ha_push",
		Source:    "metacognitive",
		Kind:      KindActionable,
		Title:     "Turn off lights?",
		Message:   "Living room lights have been on for 4 hours",
	}
	if err := s.Create(actionable); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Log a fire-and-forget.
	ff := &Record{
		RequestID: "ff-003",
		Recipient: "nugget",
		Channel:   "ha_push",
		Source:    "metacognitive",
		Title:     "Battery low",
		Message:   "Lock battery warning",
		CreatedAt: now.Add(-30 * time.Minute),
	}
	if err := s.Log(ff); err != nil {
		t.Fatalf("Log: %v", err)
	}

	// Respond to the actionable.
	if _, err := s.Respond("act-001", "yes"); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	// Recent should return both, with the actionable showing responded status.
	got, err := s.Recent(now.Add(-2*time.Hour), 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent len = %d, want 2", len(got))
	}

	// Newest first.
	if got[0].Kind != KindFireAndForget {
		t.Errorf("got[0].Kind = %q, want fire_and_forget", got[0].Kind)
	}
	if got[1].Kind != KindActionable {
		t.Errorf("got[1].Kind = %q, want actionable", got[1].Kind)
	}
	if got[1].Status != StatusResponded {
		t.Errorf("got[1].Status = %q, want responded", got[1].Status)
	}
	if got[1].ResponseAction != "yes" {
		t.Errorf("got[1].ResponseAction = %q, want yes", got[1].ResponseAction)
	}
}

func TestRecordStore_RecentLimit(t *testing.T) {
	s := newTestRecordStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		r := &Record{
			RequestID: fmt.Sprintf("ff-lim-%d", i),
			Recipient: "nugget",
			Channel:   "ha_push",
			Source:    "agent",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}
		if err := s.Log(r); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	got, err := s.Recent(now.Add(-1*time.Hour), 3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Recent len = %d, want 3 (limited)", len(got))
	}
}
