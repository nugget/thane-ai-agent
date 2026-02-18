package signal

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
)

// testRunner records the most recent Run call and returns a canned
// response. Thread-safe for use from handleMessage goroutines.
type testRunner struct {
	mu      sync.Mutex
	lastReq *agent.Request
	resp    *agent.Response
	err     error
}

func (r *testRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastReq = req
	return r.resp, r.err
}

func (r *testRunner) getLastReq() *agent.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReq
}

// bridgeHelper sets up a Bridge wired to pipe-backed Client and a mock
// runner. It returns the bridge, the stdout writer (to simulate
// subprocess output), the stdin reader (to capture what the client
// sends), and the test runner.
func bridgeHelper(t *testing.T, opts ...func(*BridgeConfig)) (*Bridge, io.Writer, io.Reader, *testRunner) {
	t.Helper()
	client, stdout, stdin := pipeClient(t)
	runner := &testRunner{
		resp: &agent.Response{Content: "ok"},
	}

	cfg := BridgeConfig{
		Client: client,
		Runner: runner,
		Logger: slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}

	bridge := NewBridge(cfg)
	return bridge, stdout, stdin, runner
}

// drainRPCRequests reads all pending JSON-RPC requests from the stdin
// reader and sends responses back to stdout. This prevents the client
// from blocking on pipe writes.
func drainRPCRequests(t *testing.T, stdin io.Reader, stdout io.Writer) {
	t.Helper()
	reader := bufio.NewReader(stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		// Send a generic success response.
		resp := `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"result":{}}` + "\n"
		if _, err := io.WriteString(stdout, resp); err != nil {
			return
		}
	}
}

// itoa is a minimal int64-to-string for test response construction.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func TestBridge_MessageRoutesToAgent(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)

	// Drain RPC requests (receipts, typing) so the client doesn't block.
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:       "+15551234567",
		SourceNumber: "+15551234567",
		SourceName:   "Alice",
		Timestamp:    1700000000000,
		DataMessage:  &DataMessage{Message: "What's the weather?"},
	}

	bridge.handleMessage(context.Background(), env)

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

func TestBridge_MessageIncludesSourceName(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:      "+15551234567",
		SourceName:  "Alice",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Hello"},
	}

	bridge.handleMessage(context.Background(), env)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if !strings.Contains(req.Messages[0].Content, "Alice") {
		t.Errorf("message should contain sender name: %q", req.Messages[0].Content)
	}
}

func TestBridge_ZeroValueRoutingConfig(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Hello"},
	}

	bridge.handleMessage(context.Background(), env)

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

func TestBridge_CustomRoutingConfig(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Routing = config.SignalRoutingConfig{
			Model:            "claude-sonnet-4-20250514",
			QualityFloor:     "8",
			Mission:          "conversation",
			DelegationGating: "disabled",
		}
	})
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Use Opus"},
	}

	bridge.handleMessage(context.Background(), env)

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

func TestBridge_EmptyResponseNoReply(t *testing.T) {
	bridge, _, stdin, runner := bridgeHelper(t)

	// Track what gets written to stdin (client sends).
	var mu sync.Mutex
	var sentMethods []string

	go func() {
		reader := bufio.NewReader(stdin)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req rpcRequest
			if err := json.Unmarshal(line, &req); err != nil {
				continue
			}
			mu.Lock()
			sentMethods = append(sentMethods, req.Method)
			mu.Unlock()
		}
	}()

	runner.resp = &agent.Response{Content: ""}

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Hello"},
	}

	// Use a short timeout context so typing/receipt RPCs fail quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	bridge.handleMessage(ctx, env)

	// Give a moment for any in-flight writes.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, m := range sentMethods {
		if m == "send" {
			t.Error("send should not be called for empty response")
		}
	}
}

func TestBridge_GroupMessageIncludesGroupInfo(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Message: "Group hello",
			GroupInfo: &GroupInfo{
				GroupID: "abc123groupid",
				Type:    "DELIVER",
			},
		},
	}

	bridge.handleMessage(context.Background(), env)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if !strings.Contains(req.Messages[0].Content, "abc123groupid") {
		t.Errorf("message should contain group ID: %q", req.Messages[0].Content)
	}
	if !strings.Contains(req.Messages[0].Content, "in group") {
		t.Errorf("message should say 'in group': %q", req.Messages[0].Content)
	}
}

func TestBridge_RateLimitDropsMessages(t *testing.T) {
	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.RateLimit = 2
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

func TestBridge_RateLimitDisabledWhenZero(t *testing.T) {
	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.RateLimit = 0
	})

	for i := 0; i < 100; i++ {
		if !bridge.allowSender("+15551234567") {
			t.Fatalf("message %d should be allowed with rate limit disabled", i+1)
		}
	}
}

func TestBridge_AgentAlreadySentSkipsDuplicateReply(t *testing.T) {
	bridge, _, stdin, runner := bridgeHelper(t)

	var mu sync.Mutex
	var sentMethods []string

	go func() {
		reader := bufio.NewReader(stdin)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req rpcRequest
			if err := json.Unmarshal(line, &req); err != nil {
				continue
			}
			mu.Lock()
			sentMethods = append(sentMethods, req.Method)
			mu.Unlock()
		}
	}()

	runner.resp = &agent.Response{
		Content: "Already sent via tool",
		ToolsUsed: map[string]int{
			"signal_send_message": 1,
		},
	}

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Send me a message"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	bridge.handleMessage(ctx, env)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, m := range sentMethods {
		if m == "send" {
			t.Error("send should not be called when agent already sent via tool")
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

func TestFormatMessage_DirectMessage(t *testing.T) {
	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Message:   "Hello Thane",
		},
	}
	got := formatMessage(env)
	if !strings.Contains(got, "+15551234567") {
		t.Error("should contain sender number")
	}
	if !strings.Contains(got, "Hello Thane") {
		t.Error("should contain message text")
	}
	if strings.Contains(got, "group") {
		t.Error("should not contain group reference for DM")
	}
	if !strings.Contains(got, "[ts:1700000000000]") {
		t.Errorf("should contain timestamp tag, got: %q", got)
	}
}

func TestFormatMessage_WithSourceName(t *testing.T) {
	env := &Envelope{
		Source:      "+15551234567",
		SourceName:  "Alice",
		DataMessage: &DataMessage{Message: "Hello"},
	}
	got := formatMessage(env)
	if !strings.Contains(got, "Alice") {
		t.Error("should contain source name")
	}
	if !strings.Contains(got, "+15551234567") {
		t.Error("should contain phone number")
	}
}

func TestFormatMessage_GroupMessage(t *testing.T) {
	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Message:   "Group update",
			GroupInfo: &GroupInfo{GroupID: "family-group-id"},
		},
	}
	got := formatMessage(env)
	if !strings.Contains(got, "family-group-id") {
		t.Error("should contain group ID")
	}
	if !strings.Contains(got, "in group") {
		t.Error("should say 'in group'")
	}
	if !strings.Contains(got, "[ts:1700000000000]") {
		t.Errorf("should contain timestamp tag, got: %q", got)
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
		{"exact match", map[string]int{"signal_send_message": 1}, true},
		{"prefixed match", map[string]int{"mcp_signal_send_message": 1}, true},
		{"zero count", map[string]int{"signal_send_message": 0}, false},
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

func TestFormatMessage_PrefersDataMessageTimestamp(t *testing.T) {
	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1000,
		DataMessage: &DataMessage{
			Timestamp: 2000,
			Message:   "Hello",
		},
	}
	got := formatMessage(env)
	if !strings.Contains(got, "[ts:2000]") {
		t.Errorf("should prefer DataMessage.Timestamp, got: %q", got)
	}
	if strings.Contains(got, "[ts:1000]") {
		t.Error("should not use envelope timestamp when DataMessage.Timestamp is set")
	}
}

func TestFormatMessage_FallsBackToEnvelopeTimestamp(t *testing.T) {
	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 3000,
		DataMessage: &DataMessage{
			Timestamp: 0, // zero — should fall back
			Message:   "Hello",
		},
	}
	got := formatMessage(env)
	if !strings.Contains(got, "[ts:3000]") {
		t.Errorf("should fall back to envelope timestamp, got: %q", got)
	}
}

// mockRotator records RotateIdleSession calls for testing.
type mockRotator struct {
	mu    sync.Mutex
	calls []string // conversation IDs
	ret   bool     // return value
}

func (m *mockRotator) RotateIdleSession(conversationID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, conversationID)
	return m.ret
}

func (m *mockRotator) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

func TestBridge_IdleSessionRotation(t *testing.T) {
	rotator := &mockRotator{ret: true}
	bridge, stdout, stdin, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Rotator = rotator
		cfg.IdleTimeout = 30 * time.Minute
	})
	go drainRPCRequests(t, stdin, stdout)

	// Seed a message from 45 minutes ago.
	bridge.mu.Lock()
	bridge.lastInboundTS["+15551234567"] = lastMessage{
		signalTS:   1700000000000,
		receivedAt: time.Now().Add(-45 * time.Minute),
	}
	bridge.mu.Unlock()

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000045000,
		DataMessage: &DataMessage{Message: "Hello after break"},
	}
	bridge.handleMessage(context.Background(), env)

	calls := rotator.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 rotation call, got %d", len(calls))
	}
	if calls[0] != "signal-15551234567" {
		t.Errorf("rotation convID = %q, want %q", calls[0], "signal-15551234567")
	}
}

func TestBridge_NoRotationWithinTimeout(t *testing.T) {
	rotator := &mockRotator{ret: true}
	bridge, stdout, stdin, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Rotator = rotator
		cfg.IdleTimeout = 30 * time.Minute
	})
	go drainRPCRequests(t, stdin, stdout)

	// Seed a message from 5 minutes ago — within timeout.
	bridge.mu.Lock()
	bridge.lastInboundTS["+15551234567"] = lastMessage{
		signalTS:   1700000000000,
		receivedAt: time.Now().Add(-5 * time.Minute),
	}
	bridge.mu.Unlock()

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000005000,
		DataMessage: &DataMessage{Message: "Quick follow-up"},
	}
	bridge.handleMessage(context.Background(), env)

	if len(rotator.getCalls()) != 0 {
		t.Error("should not rotate when within timeout")
	}
}

func TestBridge_NoRotationFirstMessage(t *testing.T) {
	rotator := &mockRotator{ret: true}
	bridge, stdout, stdin, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Rotator = rotator
		cfg.IdleTimeout = 30 * time.Minute
	})
	go drainRPCRequests(t, stdin, stdout)

	// No seeded lastInboundTS — first message from this sender.
	env := &Envelope{
		Source:      "+15559999999",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "First message ever"},
	}
	bridge.handleMessage(context.Background(), env)

	if len(rotator.getCalls()) != 0 {
		t.Error("should not rotate on first message from sender")
	}
}

func TestBridge_NoRotationWhenDisabled(t *testing.T) {
	// Case 1: nil rotator with non-zero timeout.
	bridge1, stdout1, stdin1, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.IdleTimeout = 30 * time.Minute
		// Rotator intentionally nil.
	})
	go drainRPCRequests(t, stdin1, stdout1)

	bridge1.mu.Lock()
	bridge1.lastInboundTS["+15551234567"] = lastMessage{
		signalTS:   1700000000000,
		receivedAt: time.Now().Add(-45 * time.Minute),
	}
	bridge1.mu.Unlock()

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000045000,
		DataMessage: &DataMessage{Message: "Hello"},
	}
	bridge1.handleMessage(context.Background(), env)
	// No panic — pass.

	// Case 2: rotator set but zero timeout.
	rotator := &mockRotator{ret: true}
	bridge2, stdout2, stdin2, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Rotator = rotator
		cfg.IdleTimeout = 0
	})
	go drainRPCRequests(t, stdin2, stdout2)

	bridge2.mu.Lock()
	bridge2.lastInboundTS["+15551234567"] = lastMessage{
		signalTS:   1700000000000,
		receivedAt: time.Now().Add(-45 * time.Minute),
	}
	bridge2.mu.Unlock()

	bridge2.handleMessage(context.Background(), env)

	if len(rotator.getCalls()) != 0 {
		t.Error("should not rotate when idle timeout is 0")
	}
}

func TestBridge_LastInboundTimestamp(t *testing.T) {
	bridge, _, _, _ := bridgeHelper(t)

	// Initially no timestamps tracked.
	_, ok := bridge.LastInboundTimestamp("+15551234567")
	if ok {
		t.Error("expected no timestamp before any messages")
	}

	// Simulate storing a timestamp (as Start() would do).
	bridge.mu.Lock()
	bridge.lastInboundTS["+15551234567"] = lastMessage{
		signalTS:   1700000000000,
		receivedAt: time.Now(),
	}
	bridge.mu.Unlock()

	ts, ok := bridge.LastInboundTimestamp("+15551234567")
	if !ok {
		t.Fatal("expected timestamp to be tracked")
	}
	if ts != 1700000000000 {
		t.Errorf("timestamp = %d, want 1700000000000", ts)
	}

	// Different sender should not have a timestamp.
	_, ok = bridge.LastInboundTimestamp("+15559999999")
	if ok {
		t.Error("expected no timestamp for different sender")
	}

	// Update overwrites previous value.
	bridge.mu.Lock()
	bridge.lastInboundTS["+15551234567"] = lastMessage{
		signalTS:   1700000001000,
		receivedAt: time.Now(),
	}
	bridge.mu.Unlock()

	ts, ok = bridge.LastInboundTimestamp("+15551234567")
	if !ok {
		t.Fatal("expected timestamp after update")
	}
	if ts != 1700000001000 {
		t.Errorf("timestamp = %d, want 1700000001000", ts)
	}
}
