package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

type stubConversationModelPins map[string]tools.ConversationModelPin

func (s stubConversationModelPins) ConversationModelPin(conversationID string) (tools.ConversationModelPin, bool) {
	pin, ok := s[conversationID]
	return pin, ok
}

func getConversation(t *testing.T, s *Server, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations/"+id, nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	s.handleConversationGet(rr, req)
	var body map[string]any
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (raw=%s)", err, rr.Body.String())
		}
	}
	return rr, body
}

func TestHandleConversationGetCarriesModelPin(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "signal-alice", 2, nil)
	addConv(t, store, "signal-bob", 1, nil)
	s.SetConversationModelPins(stubConversationModelPins{
		"signal-alice": {
			ConversationID: "signal-alice",
			Model:          "claude-opus-4-8",
			Provider:       "anthropic",
			Reason:         "compare",
			PinnedAt:       time.Date(2026, 9, 3, 15, 4, 5, 0, time.UTC),
			LastFallback: &tools.ConversationModelPinFallback{
				At:     time.Date(2026, 9, 3, 15, 10, 0, 0, time.UTC),
				Model:  "qwen3:8b",
				Reason: "it does not support image inputs",
			},
		},
	})

	rr, body := getConversation(t, s, "signal-alice")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if body["id"] != "signal-alice" {
		t.Fatalf("id = %v, want signal-alice (stored record fields must still flatten)", body["id"])
	}
	if msgs, _ := body["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("messages = %v, want the 2 stored messages", body["messages"])
	}
	pin, _ := body["model_pin"].(map[string]any)
	if pin["model"] != "claude-opus-4-8" || pin["provider"] != "anthropic" || pin["reason"] != "compare" {
		t.Fatalf("model_pin = %v, want the pin's deployment, provider, and reason", body["model_pin"])
	}
	if fb, _ := pin["last_fallback"].(map[string]any); fb["model"] != "qwen3:8b" {
		t.Fatalf("model_pin.last_fallback = %v, want the routed replacement", pin["last_fallback"])
	}

	rr, body = getConversation(t, s, "signal-bob")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if _, present := body["model_pin"]; present {
		t.Fatalf("unpinned conversation body = %v, want model_pin omitted", body)
	}
}

func TestHandleConversationGetWithoutPinReader(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "signal-alice", 1, nil)

	rr, body := getConversation(t, s, "signal-alice")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if _, present := body["model_pin"]; present {
		t.Fatalf("body = %v, want model_pin omitted when no reader is wired", body)
	}
}
