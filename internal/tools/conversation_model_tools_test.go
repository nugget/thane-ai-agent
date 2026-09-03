package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type mockConversationModelPinner struct {
	pins    map[string]ConversationModelPin
	pinErr  error
	lastPin struct{ conv, model, reason string }
}

func newMockConversationModelPinner() *mockConversationModelPinner {
	return &mockConversationModelPinner{pins: make(map[string]ConversationModelPin)}
}

func (m *mockConversationModelPinner) PinConversationModel(conversationID, model, reason string) (ConversationModelPin, error) {
	m.lastPin.conv, m.lastPin.model, m.lastPin.reason = conversationID, model, reason
	if m.pinErr != nil {
		return ConversationModelPin{}, m.pinErr
	}
	pin := ConversationModelPin{
		ConversationID: conversationID,
		Model:          model,
		Provider:       "anthropic",
		Resource:       "cloud",
		SupportsTools:  true,
		SupportsImages: true,
		Reason:         reason,
		PinnedAt:       time.Date(2026, 9, 3, 15, 4, 5, 0, time.UTC),
	}
	m.pins[conversationID] = pin
	return pin, nil
}

func (m *mockConversationModelPinner) ClearConversationModelPin(conversationID string) (ConversationModelPin, bool) {
	pin, ok := m.pins[conversationID]
	delete(m.pins, conversationID)
	return pin, ok
}

func (m *mockConversationModelPinner) ConversationModelPin(conversationID string) (ConversationModelPin, bool) {
	pin, ok := m.pins[conversationID]
	return pin, ok
}

func conversationModelPinTool(t *testing.T, pinner ConversationModelPinner) *Tool {
	t.Helper()
	reg := NewEmptyRegistry()
	reg.SetConversationModelPinner(pinner)
	tool := reg.Get("conversation_model_pin")
	if tool == nil {
		t.Fatal("conversation_model_pin not registered")
	}
	return tool
}

func decodeToolJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode tool result %q: %v", raw, err)
	}
	return out
}

func TestConversationModelPin_PinsCurrentConversation(t *testing.T) {
	pinner := newMockConversationModelPinner()
	tool := conversationModelPinTool(t, pinner)
	ctx := WithConversationID(context.Background(), "signal-alice")

	raw, err := tool.Handler(ctx, map[string]any{"model": " claude-opus-4-8 ", "reason": "user asked"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if pinner.lastPin.conv != "signal-alice" || pinner.lastPin.model != "claude-opus-4-8" || pinner.lastPin.reason != "user asked" {
		t.Fatalf("pinner received %+v, want trimmed model and reason for signal-alice", pinner.lastPin)
	}
	out := decodeToolJSON(t, raw)
	if out["status"] != "pinned" || out["applies"] != "next turn" {
		t.Fatalf("result = %v, want status pinned applying next turn", out)
	}
	pin, _ := out["pin"].(map[string]any)
	if pin["model"] != "claude-opus-4-8" || pin["conversation_id"] != "signal-alice" || pin["provider"] != "anthropic" {
		t.Fatalf("result pin = %v, want the resolved deployment echoed with provider", pin)
	}
	if _, ok := out["cleared_by"]; !ok {
		t.Fatalf("result = %v, want cleared_by guidance", out)
	}
}

func TestConversationModelPin_ClearSemantics(t *testing.T) {
	tests := []struct {
		name       string
		args       map[string]any
		wantStatus string
	}{
		{name: "clear flag", args: map[string]any{"clear": true}, wantStatus: "cleared"},
		{name: "clear flag ignores model", args: map[string]any{"clear": true, "model": "claude-opus-4-8"}, wantStatus: "cleared"},
		{name: "auto model clears", args: map[string]any{"model": "auto"}, wantStatus: "cleared"},
		{name: "router model clears", args: map[string]any{"model": "Router"}, wantStatus: "cleared"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pinner := newMockConversationModelPinner()
			pinner.pins["signal-alice"] = ConversationModelPin{ConversationID: "signal-alice", Model: "claude-opus-4-8"}
			tool := conversationModelPinTool(t, pinner)
			ctx := WithConversationID(context.Background(), "signal-alice")

			raw, err := tool.Handler(ctx, tc.args)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			out := decodeToolJSON(t, raw)
			if out["status"] != tc.wantStatus || out["previous_model"] != "claude-opus-4-8" || out["model_selection"] != "router" {
				t.Fatalf("result = %v, want %s with previous_model and router selection", out, tc.wantStatus)
			}
			if _, still := pinner.pins["signal-alice"]; still {
				t.Fatal("pin survived clear")
			}
			if pinner.lastPin.conv != "" {
				t.Fatalf("clear reached PinConversationModel with %+v", pinner.lastPin)
			}
		})
	}

	t.Run("clearing nothing reports not_pinned", func(t *testing.T) {
		tool := conversationModelPinTool(t, newMockConversationModelPinner())
		raw, err := tool.Handler(WithConversationID(context.Background(), "signal-alice"), map[string]any{"clear": true})
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		if out := decodeToolJSON(t, raw); out["status"] != "not_pinned" {
			t.Fatalf("result = %v, want not_pinned", out)
		}
	})
}

func TestConversationModelPin_ErrorsTeachTheNextMove(t *testing.T) {
	t.Run("turn without an id pins the shared default conversation", func(t *testing.T) {
		pinner := newMockConversationModelPinner()
		tool := conversationModelPinTool(t, pinner)
		if _, err := tool.Handler(context.Background(), map[string]any{"model": "claude-opus-4-8"}); err != nil {
			t.Fatalf("handler: %v", err)
		}
		if pinner.lastPin.conv != "default" {
			t.Fatalf("pinned conversation = %q, want the runtime's shared default", pinner.lastPin.conv)
		}
	})
	t.Run("missing model", func(t *testing.T) {
		tool := conversationModelPinTool(t, newMockConversationModelPinner())
		_, err := tool.Handler(WithConversationID(context.Background(), "signal-alice"), map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "model_registry_list") || !strings.Contains(err.Error(), "clear=true") {
			t.Fatalf("error = %v, want both next moves named", err)
		}
	})
	t.Run("pinner refusal passes through verbatim", func(t *testing.T) {
		pinner := newMockConversationModelPinner()
		pinner.pinErr = errors.New(`unknown model "nope"; pass a deployment id`)
		tool := conversationModelPinTool(t, pinner)
		_, err := tool.Handler(WithConversationID(context.Background(), "signal-alice"), map[string]any{"model": "nope"})
		if !errors.Is(err, pinner.pinErr) {
			t.Fatalf("error = %v, want the pinner's error unchanged", err)
		}
	})
}
