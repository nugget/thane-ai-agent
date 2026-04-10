package tools

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/notifications"
)

// mockNotifyHA implements notifications.HAClient for testing.
type mockNotifyHA struct {
	calls []mockHACall
	err   error
}

type mockHACall struct {
	domain  string
	service string
	data    map[string]any
}

func (m *mockNotifyHA) CallService(_ context.Context, domain, service string, data map[string]any) error {
	m.calls = append(m.calls, mockHACall{domain: domain, service: service, data: data})
	return m.err
}

// mockNotifyContacts implements notifications.ContactResolver for testing.
type mockNotifyContacts struct {
	contact *contacts.Contact
	findErr error
	props   map[string][]string
}

func (m *mockNotifyContacts) ResolveContact(_ string) (*contacts.Contact, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.contact, nil
}

func (m *mockNotifyContacts) GetPropertiesMap(_ uuid.UUID) (map[string][]string, error) {
	return m.props, nil
}

func newTestNotifyRegistry() (*Registry, *mockNotifyHA) {
	testID := uuid.New()
	ha := &mockNotifyHA{}
	resolver := &mockNotifyContacts{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	sender := notifications.NewSender(ha, resolver, nil, "test-thane", slog.Default())

	reg := NewEmptyRegistry()
	reg.SetHANotifier(sender)
	return reg, ha
}

func newTestNotifyRegistryWithRecords(t *testing.T) (*Registry, *mockNotifyHA, *notifications.RecordStore) {
	t.Helper()
	reg, ha := newTestNotifyRegistry()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := notifications.NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}

	reg.SetNotificationRecords(store)
	return reg, ha, store
}

func TestHANotify_Registered(t *testing.T) {
	reg, _ := newTestNotifyRegistry()
	tool := reg.Get("ha_notify")
	if tool == nil {
		t.Fatal("ha_notify tool not registered")
	}
}

func TestHANotify_MissingRecipient(t *testing.T) {
	reg, _ := newTestNotifyRegistry()
	_, err := reg.Execute(context.Background(), "ha_notify", `{"message": "hello"}`)
	if err == nil {
		t.Fatal("expected error for missing recipient")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error should mention recipient, got: %v", err)
	}
}

func TestHANotify_MissingMessage(t *testing.T) {
	reg, _ := newTestNotifyRegistry()
	_, err := reg.Execute(context.Background(), "ha_notify", `{"recipient": "nugget"}`)
	if err == nil {
		t.Fatal("expected error for missing message")
	}
	if !strings.Contains(err.Error(), "message") {
		t.Errorf("error should mention message, got: %v", err)
	}
}

func TestHANotify_HappyPath(t *testing.T) {
	reg, _ := newTestNotifyRegistry()
	result, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "test notification"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "nugget") {
		t.Errorf("result should mention recipient, got: %s", result)
	}
}

func TestHANotify_WithTitleAndPriority(t *testing.T) {
	reg, _ := newTestNotifyRegistry()
	result, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "urgent alert", "title": "Warning", "priority": "urgent"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "nugget") {
		t.Errorf("result should mention recipient, got: %s", result)
	}
}

func TestHANotify_SenderError(t *testing.T) {
	testID := uuid.New()
	resolver := &mockNotifyContacts{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{},
	}
	sender := notifications.NewSender(&mockNotifyHA{}, resolver, nil, "test-thane", slog.Default())

	reg := NewEmptyRegistry()
	reg.SetHANotifier(sender)

	_, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "hello"}`)
	if err == nil {
		t.Fatal("expected error from sender")
	}
	if !strings.Contains(err.Error(), "ha_companion_app") {
		t.Errorf("error should mention ha_companion_app, got: %v", err)
	}
}

func TestHANotify_ActionableHappyPath(t *testing.T) {
	reg, ha, store := newTestNotifyRegistryWithRecords(t)

	ctx := WithConversationID(context.Background(), "signal-test")
	ctx = WithSessionID(ctx, "sess-test")

	result, err := reg.Execute(ctx, "ha_notify",
		`{"recipient": "nugget", "message": "Approve this?", "actions": [{"id": "approve", "label": "Yes"}, {"id": "deny", "label": "No"}], "context": "Email approval", "timeout": "15m", "timeout_action": "deny"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "approve, deny") {
		t.Errorf("result should list action IDs, got: %s", result)
	}
	if !strings.Contains(result, "Request ID:") {
		t.Errorf("result should contain request ID, got: %s", result)
	}
	if !strings.Contains(result, "15m") {
		t.Errorf("result should mention timeout, got: %s", result)
	}

	// Verify HA was called with actions.
	if len(ha.calls) != 1 {
		t.Fatalf("expected 1 HA call, got %d", len(ha.calls))
	}
	call := ha.calls[0]
	dataMap, ok := call.data["data"].(map[string]any)
	if !ok {
		t.Fatal("expected data.data map")
	}
	actions, ok := dataMap["actions"].([]map[string]any)
	if !ok {
		t.Fatalf("expected actions in data, got %T", dataMap["actions"])
	}
	if len(actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(actions))
	}

	// Verify record was created with correct origin info.
	// Extract request ID from result.
	parts := strings.Split(result, "Request ID: ")
	if len(parts) < 2 {
		t.Fatal("could not extract request ID from result")
	}
	requestID := strings.Split(parts[1], ".")[0]

	rec, err := store.Get(requestID)
	if err != nil {
		t.Fatalf("Get record: %v", err)
	}
	if rec.OriginConversation != "signal-test" {
		t.Errorf("OriginConversation = %q, want %q", rec.OriginConversation, "signal-test")
	}
	if rec.OriginSession != "sess-test" {
		t.Errorf("OriginSession = %q, want %q", rec.OriginSession, "sess-test")
	}
	if rec.TimeoutAction != "deny" {
		t.Errorf("TimeoutAction = %q, want %q", rec.TimeoutAction, "deny")
	}
	if rec.TimeoutSeconds != 900 {
		t.Errorf("TimeoutSeconds = %d, want 900", rec.TimeoutSeconds)
	}
	if rec.Context != "Email approval" {
		t.Errorf("Context = %q, want %q", rec.Context, "Email approval")
	}
}

func TestHANotify_ActionableNoRecordStore(t *testing.T) {
	reg, _ := newTestNotifyRegistry()
	// No record store configured — actions should fail.

	_, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "test", "actions": [{"id": "ok", "label": "OK"}]}`)
	if err == nil {
		t.Fatal("expected error when record store is nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error should mention not configured, got: %v", err)
	}
}

func TestHANotify_ActionableInvalidTimeout(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRecords(t)

	_, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "test", "actions": [{"id": "ok", "label": "OK"}], "timeout": "not-a-duration"}`)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "invalid timeout") {
		t.Errorf("error should mention invalid timeout, got: %v", err)
	}
}

func TestHANotify_ActionableNegativeTimeout(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRecords(t)

	_, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "test", "actions": [{"id": "ok", "label": "OK"}], "timeout": "-5m"}`)
	if err == nil {
		t.Fatal("expected error for negative timeout")
	}
	if !strings.Contains(err.Error(), "timeout must be positive") {
		t.Errorf("error should mention positive, got: %v", err)
	}
}

func TestHANotify_ActionableDefaultTimeout(t *testing.T) {
	reg, _, store := newTestNotifyRegistryWithRecords(t)

	result, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "test", "actions": [{"id": "ok", "label": "OK"}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "30m") {
		t.Errorf("result should mention default 30m timeout, got: %s", result)
	}

	// Extract request ID and verify timeout_seconds.
	parts := strings.Split(result, "Request ID: ")
	requestID := strings.Split(parts[1], ".")[0]
	rec, _ := store.Get(requestID)
	if rec.TimeoutSeconds != 1800 {
		t.Errorf("TimeoutSeconds = %d, want 1800", rec.TimeoutSeconds)
	}
}

func TestHANotify_BackwardCompatNoActions(t *testing.T) {
	reg, ha, _ := newTestNotifyRegistryWithRecords(t)

	result, err := reg.Execute(context.Background(), "ha_notify",
		`{"recipient": "nugget", "message": "simple notification"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT mention request ID or actions.
	if strings.Contains(result, "Request ID") {
		t.Errorf("fire-and-forget should not have Request ID, got: %s", result)
	}

	// HA call should not have actions in data.
	call := ha.calls[0]
	if _, ok := call.data["data"]; ok {
		t.Error("fire-and-forget should not have data sub-map")
	}
}

// --- Router-based tool tests (send_notification, request_human_decision) ---

// mockRouterProvider implements notifications.NotificationProvider for
// testing the router-based tools.
type mockRouterProvider struct {
	name            string
	sendCalls       []notifications.NotificationRequest
	actionableCalls []notifications.ActionableRequest
	sendErr         error
	actionableErr   error
}

func (m *mockRouterProvider) Name() string { return m.name }

func (m *mockRouterProvider) Send(_ context.Context, req notifications.NotificationRequest) error {
	m.sendCalls = append(m.sendCalls, req)
	return m.sendErr
}

func (m *mockRouterProvider) SendActionable(_ context.Context, req notifications.ActionableRequest) error {
	m.actionableCalls = append(m.actionableCalls, req)
	return m.actionableErr
}

func newTestNotifyRegistryWithRouter(t *testing.T) (*Registry, *mockRouterProvider, *notifications.RecordStore) {
	t.Helper()

	testID := uuid.New()
	resolver := &mockNotifyContacts{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	records, err := notifications.NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}

	router := notifications.NewNotificationRouter(resolver, records, slog.Default())
	provider := &mockRouterProvider{name: "ha_push"}
	router.RegisterProvider(provider)

	reg := NewEmptyRegistry()
	reg.SetNotificationRouter(router)

	return reg, provider, records
}

func TestSendNotification_Registered(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)
	tool := reg.Get("send_notification")
	if tool == nil {
		t.Fatal("send_notification tool not registered")
	}
	if tool.AlwaysAvailable {
		t.Error("send_notification should rely on capability tags instead of AlwaysAvailable")
	}
}

func TestRequestHumanDecision_Registered(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)
	tool := reg.Get("request_human_decision")
	if tool == nil {
		t.Fatal("request_human_decision tool not registered")
	}
	if tool.AlwaysAvailable {
		t.Error("request_human_decision should rely on capability tags instead of AlwaysAvailable")
	}
}

func TestSendNotification_HappyPath(t *testing.T) {
	reg, provider, _ := newTestNotifyRegistryWithRouter(t)

	result, err := reg.Execute(context.Background(), "send_notification",
		`{"recipient": "nugget", "message": "Hello from router"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "nugget") {
		t.Errorf("result should mention recipient, got: %s", result)
	}
	if len(provider.sendCalls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(provider.sendCalls))
	}
	if provider.sendCalls[0].Message != "Hello from router" {
		t.Errorf("message = %q, want %q", provider.sendCalls[0].Message, "Hello from router")
	}
}

func TestSendNotification_WithTitleAndPriority(t *testing.T) {
	reg, provider, _ := newTestNotifyRegistryWithRouter(t)

	_, err := reg.Execute(context.Background(), "send_notification",
		`{"recipient": "nugget", "message": "urgent alert", "title": "Warning", "priority": "urgent"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.sendCalls[0].Title != "Warning" {
		t.Errorf("title = %q, want %q", provider.sendCalls[0].Title, "Warning")
	}
	if provider.sendCalls[0].Priority != "urgent" {
		t.Errorf("priority = %q, want %q", provider.sendCalls[0].Priority, "urgent")
	}
}

func TestSendNotification_MissingRecipient(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)

	_, err := reg.Execute(context.Background(), "send_notification",
		`{"message": "no recipient"}`)
	if err == nil {
		t.Fatal("expected error for missing recipient")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error should mention recipient, got: %v", err)
	}
}

func TestSendNotification_MissingMessage(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)

	_, err := reg.Execute(context.Background(), "send_notification",
		`{"recipient": "nugget"}`)
	if err == nil {
		t.Fatal("expected error for missing message")
	}
	if !strings.Contains(err.Error(), "message") {
		t.Errorf("error should mention message, got: %v", err)
	}
}

func TestRequestHumanDecision_HappyPath(t *testing.T) {
	reg, provider, records := newTestNotifyRegistryWithRouter(t)

	ctx := WithConversationID(context.Background(), "conv-router")
	ctx = WithSessionID(ctx, "sess-router")

	result, err := reg.Execute(ctx, "request_human_decision",
		`{"recipient": "nugget", "message": "Approve?", "actions": [{"id": "yes", "label": "Yes"}, {"id": "no", "label": "No"}], "context": "test context", "timeout": "10m", "timeout_action": "no"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "yes, no") {
		t.Errorf("result should list action IDs, got: %s", result)
	}
	if !strings.Contains(result, "Request ID:") {
		t.Errorf("result should contain Request ID, got: %s", result)
	}
	if !strings.Contains(result, "10m") {
		t.Errorf("result should mention timeout, got: %s", result)
	}

	// Provider should have been called.
	if len(provider.actionableCalls) != 1 {
		t.Fatalf("expected 1 actionable call, got %d", len(provider.actionableCalls))
	}

	// Extract request ID from result and verify record.
	parts := strings.Split(result, "Request ID: ")
	if len(parts) < 2 {
		t.Fatal("could not extract request ID from result")
	}
	requestID := strings.Split(parts[1], ".")[0]

	rec, err := records.Get(requestID)
	if err != nil {
		t.Fatalf("Get record: %v", err)
	}
	if rec.OriginConversation != "conv-router" {
		t.Errorf("OriginConversation = %q, want %q", rec.OriginConversation, "conv-router")
	}
	if rec.OriginSession != "sess-router" {
		t.Errorf("OriginSession = %q, want %q", rec.OriginSession, "sess-router")
	}
	if rec.TimeoutAction != "no" {
		t.Errorf("TimeoutAction = %q, want %q", rec.TimeoutAction, "no")
	}
	if rec.Context != "test context" {
		t.Errorf("Context = %q, want %q", rec.Context, "test context")
	}
}

func TestRequestHumanDecision_MissingActions(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)

	_, err := reg.Execute(context.Background(), "request_human_decision",
		`{"recipient": "nugget", "message": "test"}`)
	if err == nil {
		t.Fatal("expected error for missing actions")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Errorf("error should mention action, got: %v", err)
	}
}

func TestRequestHumanDecision_MissingRecipient(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)

	_, err := reg.Execute(context.Background(), "request_human_decision",
		`{"message": "test", "actions": [{"id": "ok", "label": "OK"}]}`)
	if err == nil {
		t.Fatal("expected error for missing recipient")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error should mention recipient, got: %v", err)
	}
}

func TestRequestHumanDecision_InvalidTimeout(t *testing.T) {
	reg, _, _ := newTestNotifyRegistryWithRouter(t)

	_, err := reg.Execute(context.Background(), "request_human_decision",
		`{"recipient": "nugget", "message": "test", "actions": [{"id": "ok", "label": "OK"}], "timeout": "banana"}`)
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "invalid timeout") {
		t.Errorf("error should mention invalid timeout, got: %v", err)
	}
}

func TestRequestHumanDecision_DefaultTimeout(t *testing.T) {
	reg, _, records := newTestNotifyRegistryWithRouter(t)

	result, err := reg.Execute(context.Background(), "request_human_decision",
		`{"recipient": "nugget", "message": "test", "actions": [{"id": "ok", "label": "OK"}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "30m") {
		t.Errorf("result should mention default 30m timeout, got: %s", result)
	}

	// Extract request ID and verify timeout_seconds in record.
	parts := strings.Split(result, "Request ID: ")
	requestID := strings.Split(parts[1], ".")[0]
	rec, _ := records.Get(requestID)
	if rec.TimeoutSeconds != 1800 {
		t.Errorf("TimeoutSeconds = %d, want 1800", rec.TimeoutSeconds)
	}
}

func TestParseActionsArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{
			name: "valid actions",
			args: map[string]any{
				"actions": []any{
					map[string]any{"id": "approve", "label": "Yes"},
					map[string]any{"id": "deny", "label": "No"},
				},
			},
			want: 2,
		},
		{
			name: "no actions key",
			args: map[string]any{},
			want: 0,
		},
		{
			name: "empty actions",
			args: map[string]any{"actions": []any{}},
			want: 0,
		},
		{
			name: "invalid action missing id",
			args: map[string]any{
				"actions": []any{
					map[string]any{"label": "Yes"},
				},
			},
			want: 0,
		},
		{
			name: "invalid action missing label",
			args: map[string]any{
				"actions": []any{
					map[string]any{"id": "approve"},
				},
			},
			want: 0,
		},
		{
			name: "mixed valid and invalid",
			args: map[string]any{
				"actions": []any{
					map[string]any{"id": "ok", "label": "OK"},
					map[string]any{"id": "", "label": "Bad"},
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseActionsArg(tt.args)
			if len(got) != tt.want {
				t.Errorf("parseActionsArg() returned %d actions, want %d", len(got), tt.want)
			}
		})
	}
}
