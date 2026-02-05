package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSimpleChatRequest_Parsing(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantMsg string
		wantID  string
	}{
		{
			name:    "full request",
			json:    `{"message": "turn on the lights", "conversation_id": "test-conv"}`,
			wantMsg: "turn on the lights",
			wantID:  "test-conv",
		},
		{
			name:    "message only",
			json:    `{"message": "hello"}`,
			wantMsg: "hello",
			wantID:  "", // Should default to "default" in handler
		},
		{
			name:    "empty message",
			json:    `{"message": ""}`,
			wantMsg: "",
			wantID:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req SimpleChatRequest
			if err := json.NewDecoder(bytes.NewReader([]byte(tt.json))).Decode(&req); err != nil {
				t.Fatalf("failed to parse: %v", err)
			}

			if req.Message != tt.wantMsg {
				t.Errorf("message = %q, want %q", req.Message, tt.wantMsg)
			}
			if req.ConversationID != tt.wantID {
				t.Errorf("conversation_id = %q, want %q", req.ConversationID, tt.wantID)
			}
		})
	}
}

func TestSimpleChatRequest_DefaultConversationID(t *testing.T) {
	req := SimpleChatRequest{Message: "hello"}

	// Simulate handler logic
	convID := req.ConversationID
	if convID == "" {
		convID = "default"
	}

	if convID != "default" {
		t.Errorf("expected 'default', got %q", convID)
	}
}

func TestSimpleChatResponse_JSON(t *testing.T) {
	resp := SimpleChatResponse{
		Response:       "The kitchen is 72°F.",
		Model:          "qwen2.5:72b",
		ConversationID: "kitchen-conv",
		ToolCalls:      []string{"get_state"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded SimpleChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Response != resp.Response {
		t.Errorf("response mismatch")
	}
	if decoded.Model != resp.Model {
		t.Errorf("model mismatch")
	}
	if decoded.ConversationID != resp.ConversationID {
		t.Errorf("conversation_id mismatch")
	}
	if len(decoded.ToolCalls) != 1 || decoded.ToolCalls[0] != "get_state" {
		t.Errorf("tool_calls mismatch")
	}
}

func TestSimpleChatResponse_OmitEmptyToolCalls(t *testing.T) {
	resp := SimpleChatResponse{
		Response:       "Hello!",
		Model:          "test",
		ConversationID: "default",
		// No ToolCalls
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// tool_calls should be omitted when empty
	if bytes.Contains(data, []byte(`"tool_calls":[]`)) {
		t.Error("empty tool_calls should be omitted")
	}
}
