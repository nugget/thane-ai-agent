package tools

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/notifications"
)

// mockNotifyHA implements notifications.HAClient for testing.
type mockNotifyHA struct {
	err error
}

func (m *mockNotifyHA) CallService(_ context.Context, _, _ string, _ map[string]any) error {
	return m.err
}

// mockNotifyContacts implements notifications.ContactResolver for testing.
type mockNotifyContacts struct {
	contact *contacts.Contact
	findErr error
	facts   map[string][]string
}

func (m *mockNotifyContacts) FindByName(_ string) (*contacts.Contact, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	return m.contact, nil
}

func (m *mockNotifyContacts) GetFacts(_ uuid.UUID) (map[string][]string, error) {
	return m.facts, nil
}

func newTestNotifyRegistry() (*Registry, *mockNotifyHA) {
	testID := uuid.New()
	ha := &mockNotifyHA{}
	resolver := &mockNotifyContacts{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	sender := notifications.NewSender(ha, resolver, nil, slog.Default())

	reg := NewEmptyRegistry()
	reg.SetHANotifier(sender)
	return reg, ha
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
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{},
	}
	sender := notifications.NewSender(&mockNotifyHA{}, resolver, nil, slog.Default())

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
