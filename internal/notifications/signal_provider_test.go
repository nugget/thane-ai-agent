package notifications

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
)

// mockSignalSender records calls for verification.
type mockSignalSender struct {
	lastRecipient string
	lastMessage   string
	err           error
}

func (m *mockSignalSender) Send(_ context.Context, recipient, message string) (int64, error) {
	m.lastRecipient = recipient
	m.lastMessage = message
	return 1234, m.err
}

// mockSignalContacts resolves contacts with configurable properties.
type mockSignalContacts struct {
	contact *contacts.Contact
	props   map[string][]string
	err     error
}

func (m *mockSignalContacts) ResolveContact(_ string) (*contacts.Contact, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.contact, nil
}

func (m *mockSignalContacts) GetPropertiesMap(_ uuid.UUID) (map[string][]string, error) {
	return m.props, nil
}

func TestSignalProvider_Name(t *testing.T) {
	p := NewSignalProvider(nil, nil, nil)
	if p.Name() != "signal" {
		t.Errorf("Name() = %q, want signal", p.Name())
	}
}

func TestSignalProvider_Send_IMPP(t *testing.T) {
	sender := &mockSignalSender{}
	contactID := uuid.Must(uuid.NewV7())
	resolver := &mockSignalContacts{
		contact: &contacts.Contact{ID: contactID, FormattedName: "nugget"},
		props: map[string][]string{
			"IMPP": {"signal:+15551234567"},
			"TEL":  {"+15559999999"}, // should prefer IMPP
		},
	}

	p := NewSignalProvider(sender, resolver, nil)
	err := p.Send(context.Background(), NotificationRequest{
		Recipient: "nugget",
		Title:     "New Feed",
		Message:   "A new episode is available.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.lastRecipient != "+15551234567" {
		t.Errorf("sent to %q, want +15551234567", sender.lastRecipient)
	}
	if sender.lastMessage != "New Feed\n\nA new episode is available." {
		t.Errorf("message = %q", sender.lastMessage)
	}
}

func TestSignalProvider_Send_TELFallback(t *testing.T) {
	sender := &mockSignalSender{}
	contactID := uuid.Must(uuid.NewV7())
	resolver := &mockSignalContacts{
		contact: &contacts.Contact{ID: contactID, FormattedName: "nugget"},
		props:   map[string][]string{"TEL": {"+15551234567"}},
	}

	p := NewSignalProvider(sender, resolver, nil)
	err := p.Send(context.Background(), NotificationRequest{
		Recipient: "nugget",
		Message:   "Hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sender.lastRecipient != "+15551234567" {
		t.Errorf("sent to %q, want +15551234567", sender.lastRecipient)
	}
	// No title — message should be plain.
	if sender.lastMessage != "Hello" {
		t.Errorf("message = %q, want Hello", sender.lastMessage)
	}
}

func TestSignalProvider_Send_NoPhone(t *testing.T) {
	sender := &mockSignalSender{}
	contactID := uuid.Must(uuid.NewV7())
	resolver := &mockSignalContacts{
		contact: &contacts.Contact{ID: contactID, FormattedName: "nugget"},
		props:   map[string][]string{}, // no phone properties
	}

	p := NewSignalProvider(sender, resolver, nil)
	err := p.Send(context.Background(), NotificationRequest{
		Recipient: "nugget",
		Message:   "Hello",
	})
	if err == nil {
		t.Fatal("expected error for contact without phone")
	}
}

func TestSignalProvider_Send_UnknownContact(t *testing.T) {
	sender := &mockSignalSender{}
	resolver := &mockSignalContacts{
		err: fmt.Errorf("not found"),
	}

	p := NewSignalProvider(sender, resolver, nil)
	err := p.Send(context.Background(), NotificationRequest{
		Recipient: "nobody",
		Message:   "Hello",
	})
	if err == nil {
		t.Fatal("expected error for unknown contact")
	}
}

func TestSignalProvider_SendActionable(t *testing.T) {
	sender := &mockSignalSender{}
	contactID := uuid.Must(uuid.NewV7())
	resolver := &mockSignalContacts{
		contact: &contacts.Contact{ID: contactID, FormattedName: "nugget"},
		props:   map[string][]string{"TEL": {"+15551234567"}},
	}

	p := NewSignalProvider(sender, resolver, nil)
	err := p.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{
			Recipient: "nugget",
			Title:     "Merge PR?",
			Message:   "PR #42 has 3 approvals.",
		},
		Actions:   []Action{{ID: "merge", Label: "Merge"}, {ID: "skip", Label: "Skip"}},
		RequestID: "r_test123",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should include numbered options in the message.
	if !strings.Contains(sender.lastMessage, "1) Merge") {
		t.Errorf("message should contain numbered options, got: %q", sender.lastMessage)
	}
	if !strings.Contains(sender.lastMessage, "2) Skip") {
		t.Errorf("message should contain option 2, got: %q", sender.lastMessage)
	}
	// The outbound message to the user doesn't include action_ids (those
	// are only in the memory annotation for the model's benefit).
}

func TestOutboundAnnotation(t *testing.T) {
	// Fire-and-forget.
	got := outboundAnnotation("", "Hello world", "", nil)
	if !strings.Contains(got, "[notification via signal]") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "Hello world") {
		t.Errorf("missing message: %q", got)
	}

	// Actionable with request_id and actions.
	got = outboundAnnotation("urgent", "Merge PR?", "r_abc", []Action{
		{ID: "merge", Label: "Merge"},
		{ID: "skip", Label: "Skip"},
	})
	if !strings.Contains(got, "request_id: r_abc") {
		t.Errorf("missing request_id: %q", got)
	}
	if !strings.Contains(got, "1) Merge (action_id: merge)") {
		t.Errorf("missing option 1 with action_id: %q", got)
	}
}
