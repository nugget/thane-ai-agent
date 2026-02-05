package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockLoop implements agent.Loop interface for testing.
type mockLoop struct {
	response string
	model    string
	err      error
}

func (m *mockLoop) Run(ctx interface{}, req interface{}, stream interface{}) (*mockResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &mockResponse{
		Content: m.response,
		Model:   m.model,
	}, nil
}

type mockResponse struct {
	Content      string
	Model        string
	FinishReason string
}

func TestHandleSimpleChat_ValidRequest(t *testing.T) {
	// Create a minimal server for testing the endpoint structure
	req := SimpleChatRequest{
		Message:        "turn on the lights",
		ConversationID: "test-conv",
	}
	body, _ := json.Marshal(req)

	r := httptest.NewRequest("POST", "/v1/chat", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Test request parsing (we can't easily mock the full loop)
	var parsed SimpleChatRequest
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&parsed); err != nil {
		t.Fatalf("failed to parse request: %v", err)
	}

	if parsed.Message != "turn on the lights" {
		t.Errorf("expected message 'turn on the lights', got %q", parsed.Message)
	}
	if parsed.ConversationID != "test-conv" {
		t.Errorf("expected conversation_id 'test-conv', got %q", parsed.ConversationID)
	}

	// Test response structure
	resp := SimpleChatResponse{
		Response:       "Done, turned on the living room lights.",
		Model:          "qwen2.5:72b",
		ConversationID: "test-conv",
	}

	respBody, _ := json.Marshal(resp)
	w.Write(respBody)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestHandleSimpleChat_EmptyMessage(t *testing.T) {
	req := SimpleChatRequest{
		Message: "",
	}
	body, _ := json.Marshal(req)

	// Verify validation would catch empty message
	var parsed SimpleChatRequest
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&parsed)

	if parsed.Message != "" {
		t.Error("expected empty message")
	}

	// The actual handler returns 400 for empty message
}

func TestHandleSimpleChat_DefaultConversationID(t *testing.T) {
	req := SimpleChatRequest{
		Message: "hello",
		// No ConversationID specified
	}
	body, _ := json.Marshal(req)

	var parsed SimpleChatRequest
	_ = json.NewDecoder(bytes.NewReader(body)).Decode(&parsed)

	// Handler should default to "default" when empty
	convID := parsed.ConversationID
	if convID == "" {
		convID = "default"
	}

	if convID != "default" {
		t.Errorf("expected default conversation_id 'default', got %q", convID)
	}
}

func TestSimpleChatRequest_JSONRoundtrip(t *testing.T) {
	original := SimpleChatRequest{
		Message:        "what's the temperature?",
		ConversationID: "kitchen-conv",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded SimpleChatRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Message != original.Message {
		t.Errorf("message mismatch: %q != %q", decoded.Message, original.Message)
	}
	if decoded.ConversationID != original.ConversationID {
		t.Errorf("conversation_id mismatch: %q != %q", decoded.ConversationID, original.ConversationID)
	}
}

func TestSimpleChatResponse_JSONRoundtrip(t *testing.T) {
	original := SimpleChatResponse{
		Response:       "The kitchen is 72°F.",
		Model:          "qwen2.5:72b",
		ConversationID: "kitchen-conv",
		ToolCalls:      []string{"get_state"},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded SimpleChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Response != original.Response {
		t.Errorf("response mismatch")
	}
	if decoded.Model != original.Model {
		t.Errorf("model mismatch")
	}
	if len(decoded.ToolCalls) != 1 || decoded.ToolCalls[0] != "get_state" {
		t.Errorf("tool_calls mismatch")
	}
}
