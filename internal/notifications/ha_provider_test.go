package notifications

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
)

func TestHAPushProvider_Name(t *testing.T) {
	p := NewHAPushProvider(nil)
	if p.Name() != "ha_push" {
		t.Errorf("Name() = %q, want %q", p.Name(), "ha_push")
	}
}

func TestHAPushProvider_Send(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	sender := NewSender(ha, resolver, nil, slog.Default())
	provider := NewHAPushProvider(sender)

	err := provider.Send(context.Background(), NotificationRequest{
		Recipient: "nugget",
		Title:     "Test Title",
		Message:   "Hello from provider",
		Priority:  "urgent",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if len(ha.calls) != 1 {
		t.Fatalf("expected 1 HA call, got %d", len(ha.calls))
	}
	call := ha.calls[0]
	if call.service != "mobile_app_mcphone" {
		t.Errorf("service = %q, want %q", call.service, "mobile_app_mcphone")
	}
	if call.data["message"] != "Hello from provider" {
		t.Errorf("message = %v, want %q", call.data["message"], "Hello from provider")
	}
	if call.data["title"] != "Test Title" {
		t.Errorf("title = %v, want %q", call.data["title"], "Test Title")
	}
}

func TestHAPushProvider_SendActionable(t *testing.T) {
	testID := uuid.New()
	ha := &mockHAClient{}
	resolver := &mockContactResolver{
		contact: &contacts.Contact{ID: testID, Name: "nugget"},
		facts:   map[string][]string{"ha_companion_app": {"mobile_app_mcphone"}},
	}
	sender := NewSender(ha, resolver, nil, slog.Default())
	provider := NewHAPushProvider(sender)

	err := provider.SendActionable(context.Background(), ActionableRequest{
		NotificationRequest: NotificationRequest{
			Recipient: "nugget",
			Message:   "Approve this?",
		},
		Actions:   []Action{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}},
		RequestID: "test-request-id",
		Context:   "testing actionable",
	})
	if err != nil {
		t.Fatalf("SendActionable() error = %v", err)
	}

	if len(ha.calls) != 1 {
		t.Fatalf("expected 1 HA call, got %d", len(ha.calls))
	}
	call := ha.calls[0]

	// Verify actions were included in the HA payload.
	innerData, ok := call.data["data"].(map[string]any)
	if !ok {
		t.Fatal("expected inner data map in HA call")
	}
	actions, ok := innerData["actions"].([]map[string]any)
	if !ok {
		t.Fatal("expected actions array in inner data")
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0]["action"] != "THANE_test-request-id_approve" {
		t.Errorf("action[0] = %v, want THANE_test-request-id_approve", actions[0]["action"])
	}
}
