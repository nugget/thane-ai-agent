package notifications

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
)

// mockProvider records calls and optionally returns errors.
type mockProvider struct {
	name            string
	sendCalls       []NotificationRequest
	actionableCalls []ActionableRequest
	sendErr         error
	actionableErr   error
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Send(_ context.Context, req NotificationRequest) error {
	m.sendCalls = append(m.sendCalls, req)
	return m.sendErr
}

func (m *mockProvider) SendActionable(_ context.Context, req ActionableRequest) error {
	m.actionableCalls = append(m.actionableCalls, req)
	return m.actionableErr
}

func newTestRouter(t *testing.T, resolver *mockContactResolver) (*NotificationRouter, *RecordStore) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "router-test.db")
	records, err := NewRecordStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore() error = %v", err)
	}
	t.Cleanup(func() { records.Close() })
	return NewNotificationRouter(resolver, records, slog.New(slog.NewTextHandler(os.Stderr, nil))), records
}

func TestRoute_HACompanionApp(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	provider := &mockProvider{name: "ha_push"}
	router.RegisterProvider(provider)

	got, err := router.Route("nugget")
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got.Name() != "ha_push" {
		t.Errorf("Route() provider = %q, want %q", got.Name(), "ha_push")
	}
}

func TestRoute_NotificationPreference(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts: map[string][]string{
			"ha_companion_app":        {"mobile_app_mcphone"},
			"notification_preference": {"signal"},
		},
	}
	router, _ := newTestRouter(t, resolver)

	haPush := &mockProvider{name: "ha_push"}
	signal := &mockProvider{name: "signal"}
	router.RegisterProvider(haPush)
	router.RegisterProvider(signal)

	got, err := router.Route("nugget")
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	// notification_preference should win over ha_companion_app.
	if got.Name() != "signal" {
		t.Errorf("Route() provider = %q, want %q", got.Name(), "signal")
	}
}

func TestRoute_NoProvider(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{}, // no delivery channel facts
	}
	router, _ := newTestRouter(t, resolver)
	router.RegisterProvider(&mockProvider{name: "ha_push"})

	_, err := router.Route("nugget")
	if err == nil {
		t.Fatal("expected error for contact with no provider")
	}
	if !strings.Contains(err.Error(), "no notification provider") {
		t.Errorf("error = %q, want to contain 'no notification provider'", err.Error())
	}
}

func TestRoute_ContactNotFound(t *testing.T) {
	resolver := &mockContactResolver{
		findErr: errors.New("contact not found"),
	}
	router, _ := newTestRouter(t, resolver)

	_, err := router.Route("nobody")
	if err == nil {
		t.Fatal("expected error for unknown contact")
	}
}

func TestSendNotification_HappyPath(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	provider := &mockProvider{name: "ha_push"}
	router.RegisterProvider(provider)

	err := router.SendNotification(context.Background(), NotificationRequest{
		Recipient: "nugget",
		Message:   "Hello",
		Priority:  "low",
	})
	if err != nil {
		t.Fatalf("SendNotification() error = %v", err)
	}
	if len(provider.sendCalls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(provider.sendCalls))
	}
	if provider.sendCalls[0].Message != "Hello" {
		t.Errorf("message = %q, want %q", provider.sendCalls[0].Message, "Hello")
	}
}

func TestSendActionable_HappyPath(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, records := newTestRouter(t, resolver)

	provider := &mockProvider{name: "ha_push"}
	router.RegisterProvider(provider)

	requestID, err := router.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{
			Recipient: "nugget",
			Message:   "Approve?",
		},
		Actions:       []Action{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
		Timeout:       5 * time.Minute,
		TimeoutAction: "cancel",
		Context:       "test context",
	}, "session-1", "conv-1")
	if err != nil {
		t.Fatalf("SendActionable() error = %v", err)
	}
	if requestID == "" {
		t.Fatal("expected non-empty request ID")
	}

	// Provider should have been called.
	if len(provider.actionableCalls) != 1 {
		t.Fatalf("expected 1 actionable call, got %d", len(provider.actionableCalls))
	}
	if provider.actionableCalls[0].RequestID != requestID {
		t.Errorf("provider got request_id = %q, want %q", provider.actionableCalls[0].RequestID, requestID)
	}

	// Record should exist.
	rec, err := records.Get(requestID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if rec.Recipient != "nugget" {
		t.Errorf("record recipient = %q, want %q", rec.Recipient, "nugget")
	}
	if rec.OriginSession != "session-1" {
		t.Errorf("origin_session = %q, want %q", rec.OriginSession, "session-1")
	}
	if rec.OriginConversation != "conv-1" {
		t.Errorf("origin_conversation = %q, want %q", rec.OriginConversation, "conv-1")
	}
	if rec.Status != StatusPending {
		t.Errorf("status = %q, want %q", rec.Status, StatusPending)
	}
	if len(rec.Actions) != 2 {
		t.Errorf("actions count = %d, want 2", len(rec.Actions))
	}
}

func TestSendActionable_DeliveryFailure(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, records := newTestRouter(t, resolver)

	provider := &mockProvider{name: "ha_push", actionableErr: errors.New("delivery failed")}
	router.RegisterProvider(provider)

	_, err := router.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{
			Recipient: "nugget",
			Message:   "Approve?",
		},
		Actions: []Action{{ID: "yes", Label: "Yes"}},
		Timeout: 5 * time.Minute,
	}, "session-1", "conv-1")
	if err == nil {
		t.Fatal("expected error on delivery failure")
	}

	// No record should have been created.
	expired, err := records.PendingExpired()
	if err != nil {
		t.Fatalf("PendingExpired() error = %v", err)
	}
	if len(expired) != 0 {
		t.Errorf("expected 0 records after delivery failure, got %d", len(expired))
	}
}

func TestSendActionable_NoRecordStore(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router := NewNotificationRouter(resolver, nil, slog.Default())
	router.RegisterProvider(&mockProvider{name: "ha_push"})

	_, err := router.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{
			Recipient: "nugget",
			Message:   "Approve?",
		},
		Actions: []Action{{ID: "yes", Label: "Yes"}},
		Timeout: 5 * time.Minute,
	}, "", "")
	if err == nil {
		t.Fatal("expected error when record store is nil")
	}
	if !strings.Contains(err.Error(), "record store is nil") {
		t.Errorf("error = %q, want to contain 'record store is nil'", err.Error())
	}
}

func TestRouter_Send_EscalationSender(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	provider := &mockProvider{name: "ha_push"}
	router.RegisterProvider(provider)

	// Send via the EscalationSender interface (legacy Notification struct).
	var sender EscalationSender = router
	err := sender.Send(context.Background(), Notification{
		Recipient: "nugget",
		Message:   "Escalation test",
		Priority:  "urgent",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(provider.sendCalls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(provider.sendCalls))
	}
	if provider.sendCalls[0].Priority != "urgent" {
		t.Errorf("priority = %q, want %q", provider.sendCalls[0].Priority, "urgent")
	}
}
