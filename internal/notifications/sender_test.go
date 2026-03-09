package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
)

// mockHAClient records CallService invocations.
type mockHAClient struct {
	calls []haCall
	err   error
}

type haCall struct {
	domain  string
	service string
	data    map[string]any
}

func (m *mockHAClient) CallService(_ context.Context, domain, service string, data map[string]any) error {
	m.calls = append(m.calls, haCall{domain: domain, service: service, data: data})
	return m.err
}

// mockContactResolver provides canned contact lookups.
type mockContactResolver struct {
	contact  *contacts.Contact
	findErr  error
	props    map[string][]string
	propsErr error
}

func (m *mockContactResolver) ResolveContact(_ string) (*contacts.Contact, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.contact, nil
}

func (m *mockContactResolver) GetPropertiesMap(_ uuid.UUID) (map[string][]string, error) {
	if m.propsErr != nil {
		return nil, m.propsErr
	}
	return m.props, nil
}

// mockOpstate records SetWithTTL calls.
type mockOpstate struct {
	records map[string]string // key → value
}

func newMockOpstate() *mockOpstate {
	return &mockOpstate{records: make(map[string]string)}
}

func (m *mockOpstate) SetWithTTL(_, key, value string, _ time.Duration) error {
	m.records[key] = value
	return nil
}

func TestSend(t *testing.T) {
	testID := uuid.New()

	tests := []struct {
		name      string
		notif     Notification
		ha        *mockHAClient
		resolver  *mockContactResolver
		wantErr   string
		wantCalls int
	}{
		{
			name: "happy path with default priority",
			notif: Notification{
				Recipient: "nugget",
				Message:   "Hello from Thane",
			},
			ha: &mockHAClient{},
			resolver: &mockContactResolver{
				contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
				props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
			},
			wantCalls: 1,
		},
		{
			name: "happy path with title and urgent priority",
			notif: Notification{
				Recipient: "nugget",
				Title:     "Alert",
				Message:   "Something important",
				Priority:  "urgent",
			},
			ha: &mockHAClient{},
			resolver: &mockContactResolver{
				contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
				props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
			},
			wantCalls: 1,
		},
		{
			name: "low priority",
			notif: Notification{
				Recipient: "nugget",
				Message:   "FYI",
				Priority:  "low",
			},
			ha: &mockHAClient{},
			resolver: &mockContactResolver{
				contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
				props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
			},
			wantCalls: 1,
		},
		{
			name:  "empty message",
			notif: Notification{Recipient: "nugget"},
			ha:    &mockHAClient{},
			resolver: &mockContactResolver{
				contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
			},
			wantErr: "notification message is required",
		},
		{
			name:     "empty recipient",
			notif:    Notification{Message: "hello"},
			ha:       &mockHAClient{},
			resolver: &mockContactResolver{},
			wantErr:  "notification recipient is required",
		},
		{
			name:  "contact not found",
			notif: Notification{Recipient: "unknown", Message: "hello"},
			ha:    &mockHAClient{},
			resolver: &mockContactResolver{
				findErr: sql.ErrNoRows,
			},
			wantErr: `contact "unknown" not found`,
		},
		{
			name:  "contact lookup db error",
			notif: Notification{Recipient: "nugget", Message: "hello"},
			ha:    &mockHAClient{},
			resolver: &mockContactResolver{
				findErr: errors.New("database locked"),
			},
			wantErr: `resolve contact "nugget"`,
		},
		{
			name:  "facts lookup error",
			notif: Notification{Recipient: "nugget", Message: "hello"},
			ha:    &mockHAClient{},
			resolver: &mockContactResolver{
				contact:  &contacts.Contact{ID: testID, FormattedName: "nugget"},
				propsErr: errors.New("db error"),
			},
			wantErr: `lookup properties for "nugget"`,
		},
		{
			name:  "missing ha_companion_app fact",
			notif: Notification{Recipient: "nugget", Message: "hello"},
			ha:    &mockHAClient{},
			resolver: &mockContactResolver{
				contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
				props:   map[string][]string{"email": {"test@example.com"}},
			},
			wantErr: "has no ha_companion_app property configured",
		},
		{
			name:  "HA service call fails",
			notif: Notification{Recipient: "nugget", Message: "hello"},
			ha:    &mockHAClient{err: errors.New("connection refused")},
			resolver: &mockContactResolver{
				contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
				props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
			},
			wantErr: "HA notify call failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSender(tt.ha, tt.resolver, newMockOpstate(), "test-thane", slog.Default())
			err := s.Send(context.Background(), tt.notif)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tt.ha.calls) != tt.wantCalls {
				t.Fatalf("expected %d HA calls, got %d", tt.wantCalls, len(tt.ha.calls))
			}
		})
	}
}

func TestSend_CallServiceArgs(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	s := NewSender(ha, resolver, newMockOpstate(), "test-thane", slog.Default())

	err := s.Send(context.Background(), Notification{
		Recipient: "nugget",
		Title:     "Test Title",
		Message:   "Test body",
		Priority:  "urgent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ha.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(ha.calls))
	}

	call := ha.calls[0]
	if call.domain != "notify" {
		t.Errorf("expected domain %q, got %q", "notify", call.domain)
	}
	if call.service != "mobile_app_mcphone" {
		t.Errorf("expected service %q, got %q", "mobile_app_mcphone", call.service)
	}
	if call.data["message"] != "Test body" {
		t.Errorf("expected message %q, got %v", "Test body", call.data["message"])
	}
	if call.data["title"] != "Test Title" {
		t.Errorf("expected title %q, got %v", "Test Title", call.data["title"])
	}

	dataMap, ok := call.data["data"].(map[string]any)
	if !ok {
		t.Fatal("expected data.data to be map[string]any")
	}
	pushMap, ok := dataMap["push"].(map[string]any)
	if !ok {
		t.Fatal("expected data.data.push to be map[string]any")
	}
	if pushMap["interruption-level"] != "time-sensitive" {
		t.Errorf("expected interruption-level %q, got %v", "time-sensitive", pushMap["interruption-level"])
	}
}

func TestSend_OpstateRecord(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	ops := newMockOpstate()
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	s := NewSender(ha, resolver, ops, "test-thane", slog.Default())

	err := s.Send(context.Background(), Notification{
		Recipient: "nugget",
		Title:     "Test",
		Message:   "hello",
		Priority:  "urgent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ops.records) != 1 {
		t.Fatalf("expected 1 opstate record, got %d", len(ops.records))
	}

	for key, value := range ops.records {
		if !strings.HasPrefix(key, "nugget:sent:") {
			t.Errorf("expected key starting with %q, got %q", "nugget:sent:", key)
		}
		var record map[string]string
		if err := json.Unmarshal([]byte(value), &record); err != nil {
			t.Fatalf("failed to unmarshal opstate value: %v", err)
		}
		if record["source"] != "agent" {
			t.Errorf("expected source %q, got %q", "agent", record["source"])
		}
		if record["priority"] != "urgent" {
			t.Errorf("expected priority %q, got %q", "urgent", record["priority"])
		}
		if record["title"] != "Test" {
			t.Errorf("expected title %q, got %q", "Test", record["title"])
		}
	}
}

func TestSend_OpstateNilSafe(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	s := NewSender(ha, resolver, nil, "test-thane", slog.Default())

	err := s.Send(context.Background(), Notification{
		Recipient: "nugget",
		Message:   "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error with nil opstate: %v", err)
	}
}

func TestSend_WithActions(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	s := NewSender(ha, resolver, nil, "test-thane", slog.Default())

	err := s.Send(context.Background(), Notification{
		Recipient: "nugget",
		Message:   "Approve this?",
		RequestID: "req-abc-123",
		Actions:   []Action{{ID: "approve", Label: "Yes"}, {ID: "deny", Label: "No"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ha.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(ha.calls))
	}

	call := ha.calls[0]
	dataMap, ok := call.data["data"].(map[string]any)
	if !ok {
		t.Fatal("expected data.data to be map[string]any")
	}
	actions, ok := dataMap["actions"].([]map[string]any)
	if !ok {
		t.Fatalf("expected data.data.actions to be []map[string]any, got %T", dataMap["actions"])
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0]["action"] != "TEST_THANE_req-abc-123_approve" {
		t.Errorf("action[0] = %q, want %q", actions[0]["action"], "TEST_THANE_req-abc-123_approve")
	}
	if actions[1]["title"] != "No" {
		t.Errorf("action[1].title = %q, want %q", actions[1]["title"], "No")
	}
}

func TestSend_ActionsWithPriority(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	s := NewSender(ha, resolver, nil, "test-thane", slog.Default())

	err := s.Send(context.Background(), Notification{
		Recipient: "nugget",
		Message:   "Urgent approval",
		Priority:  "urgent",
		RequestID: "req-urgent-1",
		Actions:   []Action{{ID: "ok", Label: "OK"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call := ha.calls[0]
	dataMap := call.data["data"].(map[string]any)

	// Both push and actions should coexist.
	if _, ok := dataMap["push"]; !ok {
		t.Error("expected push key in data when priority is urgent")
	}
	if _, ok := dataMap["actions"]; !ok {
		t.Error("expected actions key in data when actions present")
	}
}

func TestSend_NoActionsBackwardCompat(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, FormattedName: "nugget"},
		props:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	s := NewSender(ha, resolver, nil, "test-thane", slog.Default())

	err := s.Send(context.Background(), Notification{
		Recipient: "nugget",
		Message:   "simple notification",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call := ha.calls[0]
	// Without actions or priority, there should be no data sub-map.
	if _, ok := call.data["data"]; ok {
		t.Error("expected no data sub-map for simple notification without actions or priority")
	}
}

func TestBuildHAActions(t *testing.T) {
	actions := []Action{
		{ID: "approve", Label: "Approve"},
		{ID: "deny", Label: "Deny"},
	}
	ha := buildHAActions("TEST_THANE", "req-123", actions)
	if len(ha) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(ha))
	}
	if ha[0]["action"] != "TEST_THANE_req-123_approve" {
		t.Errorf("action = %q, want %q", ha[0]["action"], "TEST_THANE_req-123_approve")
	}
	if ha[0]["title"] != "Approve" {
		t.Errorf("title = %q, want %q", ha[0]["title"], "Approve")
	}
	if ha[1]["action"] != "TEST_THANE_req-123_deny" {
		t.Errorf("action = %q, want %q", ha[1]["action"], "TEST_THANE_req-123_deny")
	}
}

func TestPriorityData(t *testing.T) {
	tests := []struct {
		priority string
		wantNil  bool
		wantKey  string
	}{
		{"low", false, "passive"},
		{"normal", true, ""},
		{"urgent", false, "time-sensitive"},
		{"", true, ""},
		{"unknown", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.priority, func(t *testing.T) {
			pd := priorityData(tt.priority)
			if tt.wantNil {
				if pd != nil {
					t.Fatalf("expected nil, got %v", pd)
				}
				return
			}
			if pd == nil {
				t.Fatal("expected non-nil priority data")
			}
			push, ok := pd["push"].(map[string]any)
			if !ok {
				t.Fatal("expected push key in priority data")
			}
			if push["interruption-level"] != tt.wantKey {
				t.Errorf("expected %q, got %v", tt.wantKey, push["interruption-level"])
			}
		})
	}
}
