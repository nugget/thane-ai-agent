package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
)

// mockMCPCaller records CallTool invocations and returns canned responses.
type mockMCPCaller struct {
	mu       sync.Mutex
	calls    []mcpCall
	recvResp string // JSON response for receive_message
	recvErr  error
	sendErr  error
}

type mcpCall struct {
	Name string
	Args map[string]any
}

func (m *mockMCPCaller) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mcpCall{Name: name, Args: args})

	if name == "receive_message" {
		return m.recvResp, m.recvErr
	}
	if name == "send_message_to_user" {
		return "", m.sendErr
	}
	return "", nil
}

func (m *mockMCPCaller) getCalls() []mcpCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mcpCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// signalTestRunner records the most recent Run call and returns a canned
// response. Thread-safe for use from handleMessage goroutines.
type signalTestRunner struct {
	mu      sync.Mutex
	lastReq *agent.Request
	resp    *agent.Response
	err     error
}

func (r *signalTestRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastReq = req
	return r.resp, r.err
}

func (r *signalTestRunner) getLastReq() *agent.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReq
}

// callHandleMessage is a test helper that invokes handleMessage
// synchronously, providing the done channel the production code expects.
func callHandleMessage(b *SignalBridge, ctx context.Context, msg signalMessage) {
	done := make(chan struct{})
	b.handleMessage(ctx, msg, done)
}

func TestSignalBridge_MessageRoutesToAgent(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "What's the weather?",
	}
	msgJSON, _ := json.Marshal(msg)

	mcpMock := &mockMCPCaller{recvResp: string(msgJSON)}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "It's sunny!"},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if req.ConversationID != "signal-15551234567" {
		t.Errorf("ConversationID = %q, want %q", req.ConversationID, "signal-15551234567")
	}
	if req.Hints["source"] != "signal" {
		t.Errorf("hint source = %q, want %q", req.Hints["source"], "signal")
	}
	if req.Hints["sender"] != "+15551234567" {
		t.Errorf("hint sender = %q, want %q", req.Hints["sender"], "+15551234567")
	}
	if !strings.Contains(req.Messages[0].Content, "What's the weather?") {
		t.Errorf("message content missing user text: %q", req.Messages[0].Content)
	}
}

func TestSignalBridge_ZeroValueRoutingConfig(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Hello",
	}
	msgJSON, _ := json.Marshal(msg)

	mcpMock := &mockMCPCaller{recvResp: string(msgJSON)}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "Hi!"},
	}

	// Construct bridge without config.Load() / applyDefaults() —
	// zero-value SignalRoutingConfig means all routing fields are empty.
	// In production, applyDefaults() populates QualityFloor, Mission,
	// and DelegationGating before the bridge sees them.
	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if req.Hints["quality_floor"] != "" {
		t.Errorf("quality_floor hint = %q, want empty (zero-value config)", req.Hints["quality_floor"])
	}
	if req.Model != "" {
		t.Errorf("Model = %q, want empty (zero-value config)", req.Model)
	}
}

func TestSignalBridge_CustomRoutingConfig(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Use Opus",
	}
	msgJSON, _ := json.Marshal(msg)

	mcpMock := &mockMCPCaller{recvResp: string(msgJSON)}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "ok"},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
		Routing: config.SignalRoutingConfig{
			Model:            "claude-sonnet-4-20250514",
			QualityFloor:     "8",
			Mission:          "conversation",
			DelegationGating: "disabled",
		},
	})

	callHandleMessage(bridge, context.Background(), msg)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if req.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", req.Model, "claude-sonnet-4-20250514")
	}
	if req.Hints["quality_floor"] != "8" {
		t.Errorf("quality_floor = %q, want %q", req.Hints["quality_floor"], "8")
	}
	if req.Hints["mission"] != "conversation" {
		t.Errorf("mission = %q, want %q", req.Hints["mission"], "conversation")
	}
	if req.Hints["delegation_gating"] != "disabled" {
		t.Errorf("delegation_gating = %q, want %q", req.Hints["delegation_gating"], "disabled")
	}
}

func TestSignalBridge_ResponseSentBack(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Hello",
	}

	mcpMock := &mockMCPCaller{}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "Hi there!"},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	calls := mcpMock.getCalls()
	var sendCall *mcpCall
	for i := range calls {
		if calls[i].Name == "send_message_to_user" {
			sendCall = &calls[i]
			break
		}
	}
	if sendCall == nil {
		t.Fatal("send_message_to_user was not called")
	}
	if sendCall.Args["user_id"] != "+15551234567" {
		t.Errorf("user_id = %v, want %q", sendCall.Args["user_id"], "+15551234567")
	}
	if sendCall.Args["message"] != "Hi there!" {
		t.Errorf("message = %v, want %q", sendCall.Args["message"], "Hi there!")
	}
}

func TestSignalBridge_EmptyResponseNoReply(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Hello",
	}

	mcpMock := &mockMCPCaller{}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: ""},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	calls := mcpMock.getCalls()
	for _, c := range calls {
		if c.Name == "send_message_to_user" {
			t.Error("send_message_to_user should not be called for empty response")
		}
	}
}

func TestSignalBridge_GroupMessageIncludesGroupName(t *testing.T) {
	msg := signalMessage{
		SenderID:  "+15551234567",
		Message:   "Group hello",
		GroupName: "Family Chat",
	}

	mcpMock := &mockMCPCaller{}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "Hi group!"},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if !strings.Contains(req.Messages[0].Content, "Family Chat") {
		t.Errorf("message should contain group name: %q", req.Messages[0].Content)
	}
	if !strings.Contains(req.Messages[0].Content, "in group") {
		t.Errorf("message should say 'in group': %q", req.Messages[0].Content)
	}
}

func TestSignalBridge_RateLimitDropsMessages(t *testing.T) {
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "ok"},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         &mockMCPCaller{},
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
		RateLimit:   2, // 2 messages per minute
	})

	sender := "+15551234567"

	// First two should be allowed.
	if !bridge.allowSender(sender) {
		t.Error("message 1 should be allowed")
	}
	if !bridge.allowSender(sender) {
		t.Error("message 2 should be allowed")
	}
	// Third should be dropped.
	if bridge.allowSender(sender) {
		t.Error("message 3 should be rate-limited")
	}

	// Different sender should still be allowed.
	if !bridge.allowSender("+15559999999") {
		t.Error("different sender should be allowed")
	}
}

func TestSignalBridge_RateLimitDisabledWhenZero(t *testing.T) {
	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         &mockMCPCaller{},
		Runner:      &signalTestRunner{resp: &agent.Response{}},
		Logger:      slog.Default(),
		PollTimeout: 1,
		RateLimit:   0,
	})

	for i := 0; i < 100; i++ {
		if !bridge.allowSender("+15551234567") {
			t.Fatalf("message %d should be allowed with rate limit disabled", i+1)
		}
	}
}

func TestSanitizePhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"+15551234567", "15551234567"},
		{"+1 (555) 123-4567", "15551234567"},
		{"15551234567", "15551234567"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sanitizePhone(tt.input)
		if got != tt.want {
			t.Errorf("sanitizePhone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatSignalMessage_DirectMessage(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Hello Thane",
	}
	got := formatSignalMessage(msg)
	if !strings.Contains(got, "+15551234567") {
		t.Error("should contain sender ID")
	}
	if !strings.Contains(got, "Hello Thane") {
		t.Error("should contain message text")
	}
	if strings.Contains(got, "group") {
		t.Error("should not contain group reference for DM")
	}
}

func TestFormatSignalMessage_GroupMessage(t *testing.T) {
	msg := signalMessage{
		SenderID:  "+15551234567",
		Message:   "Group update",
		GroupName: "Family",
	}
	got := formatSignalMessage(msg)
	if !strings.Contains(got, "Family") {
		t.Error("should contain group name")
	}
	if !strings.Contains(got, "in group") {
		t.Error("should say 'in group'")
	}
}

func TestSignalBridge_ContextCancellation(t *testing.T) {
	mcpMock := &mockMCPCaller{
		recvResp: "{}",
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      &signalTestRunner{resp: &agent.Response{}},
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bridge.Start(ctx)
		close(done)
	}()

	// Let it poll at least once.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Clean exit.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestSignalBridge_ReadReceiptSentBeforeHandle(t *testing.T) {
	msg := signalMessage{
		SenderID:  "+15551234567",
		Message:   "Hello",
		Timestamp: 1700000000000,
	}
	msgJSON, _ := json.Marshal(msg)

	mcpMock := &mockMCPCaller{recvResp: string(msgJSON)}
	runner := &signalTestRunner{
		resp: &agent.Response{Content: "Hi!"},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	// Use Start with a context that cancels after one poll cycle.
	// The mock returns a valid message on first poll, then we cancel.
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		bridge.Start(ctx)
	}()
	<-started

	// Give the bridge time to process one message cycle.
	time.Sleep(200 * time.Millisecond)
	cancel()

	calls := mcpMock.getCalls()
	var order []string
	for _, c := range calls {
		switch c.Name {
		case "receive_message", "send_read_receipt", "send_message_to_user":
			order = append(order, c.Name)
		}
	}

	// Expect: receive_message → send_read_receipt → send_message_to_user
	if len(order) < 3 {
		t.Fatalf("expected at least 3 MCP calls, got %d: %v", len(order), order)
	}
	if order[0] != "receive_message" {
		t.Errorf("first call = %q, want receive_message", order[0])
	}
	if order[1] != "send_read_receipt" {
		t.Errorf("second call = %q, want send_read_receipt", order[1])
	}
	if order[2] != "send_message_to_user" {
		t.Errorf("third call = %q, want send_message_to_user", order[2])
	}

	// Verify read receipt parameters.
	var receiptCall *mcpCall
	for i := range calls {
		if calls[i].Name == "send_read_receipt" {
			receiptCall = &calls[i]
			break
		}
	}
	if receiptCall == nil {
		t.Fatal("send_read_receipt was not called")
	}
	if receiptCall.Args["recipient"] != "+15551234567" {
		t.Errorf("read receipt recipient = %v, want %q", receiptCall.Args["recipient"], "+15551234567")
	}
	ts, ok := receiptCall.Args["timestamps"]
	if !ok {
		t.Fatal("read receipt missing timestamps field")
	}
	tsSlice, ok := ts.([]int64)
	if !ok {
		t.Fatalf("timestamps type = %T, want []int64", ts)
	}
	if len(tsSlice) != 1 || tsSlice[0] != 1700000000000 {
		t.Errorf("timestamps = %v, want [1700000000000]", tsSlice)
	}
}

func TestSignalBridge_DoneChannelClosedOnCompletion(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Hello",
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         &mockMCPCaller{},
		Runner:      &signalTestRunner{resp: &agent.Response{Content: "Hi!"}},
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	done := make(chan struct{})
	go bridge.handleMessage(context.Background(), msg, done)

	select {
	case <-done:
		// Channel closed as expected.
	case <-time.After(5 * time.Second):
		t.Fatal("done channel was not closed after handleMessage returned")
	}
}

func TestSignalBridge_AgentAlreadySentSkipsDuplicateReply(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Send me a message",
	}

	mcpMock := &mockMCPCaller{}
	runner := &signalTestRunner{
		resp: &agent.Response{
			Content: "Already sent via tool",
			ToolsUsed: map[string]int{
				"mcp_signal_send_message_to_user": 1,
			},
		},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	// The bridge should NOT call send_message_to_user because the agent
	// already did it during its tool loop.
	calls := mcpMock.getCalls()
	for _, c := range calls {
		if c.Name == "send_message_to_user" {
			t.Error("send_message_to_user should not be called when agent already sent")
		}
	}
}

func TestSignalBridge_NoToolsUsedSendsNormally(t *testing.T) {
	msg := signalMessage{
		SenderID: "+15551234567",
		Message:  "Hello",
	}

	mcpMock := &mockMCPCaller{}
	runner := &signalTestRunner{
		resp: &agent.Response{
			Content:   "Hi there!",
			ToolsUsed: map[string]int{"some_other_tool": 2},
		},
	}

	bridge := NewSignalBridge(SignalBridgeConfig{
		MCP:         mcpMock,
		Runner:      runner,
		Logger:      slog.Default(),
		PollTimeout: 1,
	})

	callHandleMessage(bridge, context.Background(), msg)

	calls := mcpMock.getCalls()
	var found bool
	for _, c := range calls {
		if c.Name == "send_message_to_user" {
			found = true
		}
	}
	if !found {
		t.Error("send_message_to_user should be called when agent did not send")
	}
}

func TestAgentAlreadySent(t *testing.T) {
	tests := []struct {
		name      string
		toolsUsed map[string]int
		want      bool
	}{
		{"nil map", nil, false},
		{"empty map", map[string]int{}, false},
		{"unrelated tools", map[string]int{"web_search": 1}, false},
		{"exact match", map[string]int{"send_message_to_user": 1}, true},
		{"prefixed match", map[string]int{"mcp_signal_send_message_to_user": 1}, true},
		{"zero count", map[string]int{"mcp_signal_send_message_to_user": 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentAlreadySent(tt.toolsUsed); got != tt.want {
				t.Errorf("agentAlreadySent(%v) = %v, want %v", tt.toolsUsed, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("long string here", 4); got != "long..." {
		t.Errorf("truncate long = %q", got)
	}
}
