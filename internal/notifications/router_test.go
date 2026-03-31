package notifications

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/database"
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
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	records, err := NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore() error = %v", err)
	}
	return NewNotificationRouter(resolver, records, slog.New(slog.NewTextHandler(os.Stderr, nil))), records
}

func TestRoute_HACompanionApp(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
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
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props: map[string][]string{
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
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{}, // no delivery channel properties
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

func TestRoute_EmptyHACompanionApp(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {}}, // key present but empty
	}
	router, _ := newTestRouter(t, resolver)
	router.RegisterProvider(&mockProvider{name: "ha_push"})

	_, err := router.Route("nugget")
	if err == nil {
		t.Fatal("expected error when ha_companion_app is empty")
	}
	if !strings.Contains(err.Error(), "no notification provider") {
		t.Errorf("error = %q, want to contain 'no notification provider'", err.Error())
	}
}

func TestRegisterProvider_Nil(t *testing.T) {
	router := NewNotificationRouter(nil, nil, slog.Default())
	// Should not panic.
	router.RegisterProvider(nil)
	if len(router.providers) != 0 {
		t.Errorf("expected 0 providers after registering nil, got %d", len(router.providers))
	}
}

func TestSendActionable_NoActions(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)
	router.RegisterProvider(&mockProvider{name: "ha_push"})

	_, err := router.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{Recipient: "nugget", Message: "test"},
		Timeout:             5 * time.Minute,
	}, "", "")
	if err == nil {
		t.Fatal("expected error for empty actions")
	}
	if !strings.Contains(err.Error(), "at least one action") {
		t.Errorf("error = %q, want to contain 'at least one action'", err.Error())
	}
}

func TestSendActionable_ZeroTimeout(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)
	router.RegisterProvider(&mockProvider{name: "ha_push"})

	_, err := router.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{Recipient: "nugget", Message: "test"},
		Actions:             []Action{{ID: "ok", Label: "OK"}},
	}, "", "")
	if err == nil {
		t.Fatal("expected error for zero timeout")
	}
	if !strings.Contains(err.Error(), "positive timeout") {
		t.Errorf("error = %q, want to contain 'positive timeout'", err.Error())
	}
}

func TestNewNotificationRouter_NilLogger(t *testing.T) {
	// Should not panic when nil logger is passed.
	router := NewNotificationRouter(nil, nil, nil)
	if router.logger == nil {
		t.Fatal("expected non-nil logger after nil was passed")
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
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
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
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
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
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
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
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
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

// mockActivitySource returns fixed channel activity.
type mockActivitySource struct {
	channels []ChannelActivity
}

func (m *mockActivitySource) ActiveChannels() []ChannelActivity { return m.channels }

func TestRoute_ActiveChannelPreferred(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	haPush := &mockProvider{name: "ha_push"}
	signal := &mockProvider{name: "signal"}
	router.RegisterProvider(haPush)
	router.RegisterProvider(signal)
	router.SetActivitySource(&mockActivitySource{
		channels: []ChannelActivity{
			{Channel: "signal", Contact: "nugget", LastActive: time.Now()},
		},
	})

	got, err := router.Route("nugget")
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got.Name() != "signal" {
		t.Errorf("Route() = %q, want signal (active channel)", got.Name())
	}
}

func TestRoute_PreferenceOverridesActivity(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props: map[string][]string{
			"ha_companion_app":        {"mobile_app_mcphone"},
			"notification_preference": {"ha_push"},
		},
	}
	router, _ := newTestRouter(t, resolver)

	haPush := &mockProvider{name: "ha_push"}
	signal := &mockProvider{name: "signal"}
	router.RegisterProvider(haPush)
	router.RegisterProvider(signal)
	router.SetActivitySource(&mockActivitySource{
		channels: []ChannelActivity{
			{Channel: "signal", Contact: "nugget", LastActive: time.Now()},
		},
	})

	got, err := router.Route("nugget")
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got.Name() != "ha_push" {
		t.Errorf("Route() = %q, want ha_push (explicit preference)", got.Name())
	}
}

func TestRoute_NoActivityFallsBackToStatic(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	haPush := &mockProvider{name: "ha_push"}
	signal := &mockProvider{name: "signal"}
	router.RegisterProvider(haPush)
	router.RegisterProvider(signal)
	router.SetActivitySource(&mockActivitySource{channels: nil}) // no activity

	got, err := router.Route("nugget")
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got.Name() != "ha_push" {
		t.Errorf("Route() = %q, want ha_push (static fallback)", got.Name())
	}
}

func TestRoute_ActivityDifferentContact(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	haPush := &mockProvider{name: "ha_push"}
	signal := &mockProvider{name: "signal"}
	router.RegisterProvider(haPush)
	router.RegisterProvider(signal)
	router.SetActivitySource(&mockActivitySource{
		channels: []ChannelActivity{
			{Channel: "signal", Contact: "someone_else", LastActive: time.Now()},
		},
	})

	got, err := router.Route("nugget")
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	// Activity is for a different contact — should not match.
	if got.Name() != "ha_push" {
		t.Errorf("Route() = %q, want ha_push (activity for different contact)", got.Name())
	}
}

func TestSendActionable_FallbackOnDeliveryFailure(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	router, _ := newTestRouter(t, resolver)

	// Signal is active but doesn't support actionable.
	signal := &mockProvider{name: "signal", actionableErr: errors.New("not supported")}
	haPush := &mockProvider{name: "ha_push"}
	router.RegisterProvider(signal)
	router.RegisterProvider(haPush)
	router.SetActivitySource(&mockActivitySource{
		channels: []ChannelActivity{
			{Channel: "signal", Contact: "nugget", LastActive: time.Now()},
		},
	})

	_, err := router.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{Recipient: "nugget", Message: "Approve?"},
		Actions:             []Action{{ID: "yes", Label: "Yes"}},
		Timeout:             5 * time.Minute,
	}, "session-1", "conv-1")
	if err != nil {
		t.Fatalf("SendActionable() should fall back to ha_push, got error: %v", err)
	}
	// ha_push should have received the call.
	if len(haPush.actionableCalls) != 1 {
		t.Errorf("ha_push actionable calls = %d, want 1", len(haPush.actionableCalls))
	}
}

func TestRouter_Send_EscalationSender(t *testing.T) {
	testID := uuid.New()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
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
