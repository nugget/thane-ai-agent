package signal

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/attachments"
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

	bridge.handleMessage(context.Background(), env, nil)

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

	bridge.handleMessage(context.Background(), env, nil)

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

	bridge.handleMessage(context.Background(), env, nil)

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

	bridge.handleMessage(context.Background(), env, nil)

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

	bridge.handleMessage(ctx, env, nil)

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

	bridge.handleMessage(context.Background(), env, nil)

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

	bridge.handleMessage(ctx, env, nil)
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
	got := formatMessage(env, nil)
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
	got := formatMessage(env, nil)
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
	got := formatMessage(env, nil)
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
	got := formatMessage(env, nil)
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
	got := formatMessage(env, nil)
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

func (m *mockRotator) RotateIdleSession(_ context.Context, conversationID, _ string) bool {
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
	bridge.handleMessage(context.Background(), env, nil)

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
	bridge.handleMessage(context.Background(), env, nil)

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
	bridge.handleMessage(context.Background(), env, nil)

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
	bridge1.handleMessage(context.Background(), env, nil)
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

	bridge2.handleMessage(context.Background(), env, nil)

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

// mockResolver resolves phone numbers to contact names for testing.
type mockResolver struct {
	contacts map[string]string // phone → name
}

func (m *mockResolver) ResolvePhone(phone string) (string, string, bool) {
	name, ok := m.contacts[phone]
	if !ok {
		return "", "", false
	}
	return name, "known", true
}

func TestBridge_ContactResolution(t *testing.T) {
	resolver := &mockResolver{contacts: map[string]string{
		"+15551234567": "Alice Smith",
	}}
	bridge, stdout, stdin, runner := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Resolver = resolver
	})
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Hello"},
	}

	bridge.handleMessage(context.Background(), env, nil)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if req.Hints["sender_name"] != "Alice Smith" {
		t.Errorf("sender_name hint = %q, want %q", req.Hints["sender_name"], "Alice Smith")
	}
}

func TestBridge_ContactResolution_Unknown(t *testing.T) {
	resolver := &mockResolver{contacts: map[string]string{
		"+15559999999": "Known Person",
	}}
	bridge, stdout, stdin, runner := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Resolver = resolver
	})
	go drainRPCRequests(t, stdin, stdout)

	// This sender is not in the resolver.
	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Hello"},
	}

	bridge.handleMessage(context.Background(), env, nil)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if _, exists := req.Hints["sender_name"]; exists {
		t.Errorf("sender_name hint should not be set for unknown sender, got %q", req.Hints["sender_name"])
	}
}

func TestBridge_ContactResolution_NilResolver(t *testing.T) {
	// Resolver is nil by default — should not panic.
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:      "+15551234567",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "Hello"},
	}

	bridge.handleMessage(context.Background(), env, nil)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	if _, exists := req.Hints["sender_name"]; exists {
		t.Errorf("sender_name hint should not be set with nil resolver, got %q", req.Hints["sender_name"])
	}
}

// --- Issue #357: Typing Indicator Refresh ---

func TestStartTypingRefresh_CancelsCleanly(t *testing.T) {
	bridge, _, stdin, _ := bridgeHelper(t)

	// Drain RPC requests so the client doesn't block on pipe writes.
	go func() {
		reader := bufio.NewReader(stdin)
		for {
			if _, err := reader.ReadBytes('\n'); err != nil {
				return
			}
		}
	}()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ctxCancel()

	cancel := bridge.startTypingRefresh(ctx, "+15551234567")

	// Let the goroutine start and potentially fire.
	time.Sleep(50 * time.Millisecond)

	// Cancel should not panic or block.
	cancel()

	// Brief wait to confirm no further activity.
	time.Sleep(50 * time.Millisecond)
}

// --- Issue #358: Reaction Handling ---

func TestBridge_ReactionWakesAgent(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:     "+15551234567",
		SourceName: "Alice",
		Timestamp:  1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Reaction: &Reaction{
				Emoji:               "❤️",
				TargetAuthor:        "+15559999999",
				TargetSentTimestamp: 1700000099000,
				IsRemove:            false,
			},
		},
	}

	bridge.handleReaction(context.Background(), env)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called for reaction")
	}
	if req.Hints["event_type"] != "reaction" {
		t.Errorf("event_type hint = %q, want %q", req.Hints["event_type"], "reaction")
	}
	if req.Hints["reaction_emoji"] != "❤️" {
		t.Errorf("reaction_emoji hint = %q, want %q", req.Hints["reaction_emoji"], "❤️")
	}
	if req.Hints["target_sent_timestamp"] != "1700000099000" {
		t.Errorf("target_sent_timestamp hint = %q, want %q", req.Hints["target_sent_timestamp"], "1700000099000")
	}
	if req.Hints["source"] != "signal" {
		t.Errorf("source hint = %q, want %q", req.Hints["source"], "signal")
	}
	if req.ConversationID != "signal-15551234567" {
		t.Errorf("ConversationID = %q, want %q", req.ConversationID, "signal-15551234567")
	}
}

func TestBridge_ReactionRemovalIgnored(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Reaction: &Reaction{
				Emoji:               "❤️",
				TargetAuthor:        "+15559999999",
				TargetSentTimestamp: 1700000099000,
				IsRemove:            true,
			},
		},
	}

	bridge.handleReaction(context.Background(), env)

	if runner.getLastReq() != nil {
		t.Error("runner.Run should not be called for reaction removal")
	}
}

func TestFormatReaction(t *testing.T) {
	tests := []struct {
		name       string
		env        *Envelope
		wantEmoji  string
		wantSender string
		wantTS     string
	}{
		{
			name: "with source name",
			env: &Envelope{
				Source:     "+15551234567",
				SourceName: "Alice",
				DataMessage: &DataMessage{
					Reaction: &Reaction{
						Emoji:               "👍",
						TargetAuthor:        "+15559999999",
						TargetSentTimestamp: 1700000099000,
					},
				},
			},
			wantEmoji:  "👍",
			wantSender: "Alice (+15551234567)",
			wantTS:     "[ts:1700000099000]",
		},
		{
			name: "without source name",
			env: &Envelope{
				Source: "+15551234567",
				DataMessage: &DataMessage{
					Reaction: &Reaction{
						Emoji:               "🎉",
						TargetAuthor:        "+15559999999",
						TargetSentTimestamp: 1700000050000,
					},
				},
			},
			wantEmoji:  "🎉",
			wantSender: "+15551234567",
			wantTS:     "[ts:1700000050000]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatReaction(tt.env)
			if !strings.Contains(got, tt.wantEmoji) {
				t.Errorf("should contain emoji %q, got: %q", tt.wantEmoji, got)
			}
			if !strings.Contains(got, tt.wantSender) {
				t.Errorf("should contain sender %q, got: %q", tt.wantSender, got)
			}
			if !strings.Contains(got, tt.wantTS) {
				t.Errorf("should contain timestamp %q, got: %q", tt.wantTS, got)
			}
		})
	}
}

func TestBridge_ReactionRateLimited(t *testing.T) {
	bridge, _, _, runner := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.RateLimit = 1
	})

	// Use up the rate limit.
	bridge.allowSender("+15551234567")

	// Reaction should be rate-limited — handleReaction should not be called
	// (we test via allowSender directly since the Start loop is hard to
	// drive in unit tests).
	if bridge.allowSender("+15551234567") {
		t.Error("second message should be rate-limited")
	}

	// Runner should not have been called.
	if runner.getLastReq() != nil {
		t.Error("runner should not be called when rate-limited")
	}
}

// --- Issue #359: Attachment Handling ---

func TestBridge_AttachmentOnlyMessageProcessed(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Message:   "", // no text
			Attachments: []Attachment{
				{ContentType: "image/jpeg", Filename: "photo.jpg", ID: "abc123", Size: 245760},
			},
		},
	}

	bridge.handleMessage(context.Background(), env, nil)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called for attachment-only message")
	}
	if !strings.Contains(req.Messages[0].Content, "image/jpeg") {
		t.Errorf("message should describe attachment, got: %q", req.Messages[0].Content)
	}
}

func TestBridge_AttachmentWithText(t *testing.T) {
	bridge, stdout, stdin, runner := bridgeHelper(t)
	go drainRPCRequests(t, stdin, stdout)

	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Message:   "Check this out",
			Attachments: []Attachment{
				{ContentType: "image/png", Filename: "screenshot.png", ID: "def456", Size: 100000},
			},
		},
	}

	bridge.handleMessage(context.Background(), env, nil)

	req := runner.getLastReq()
	if req == nil {
		t.Fatal("runner.Run was not called")
	}
	content := req.Messages[0].Content
	if !strings.Contains(content, "image/png") {
		t.Errorf("should contain attachment type, got: %q", content)
	}
	if !strings.Contains(content, "Check this out") {
		t.Errorf("should contain message text, got: %q", content)
	}
}

func TestDescribeAttachment(t *testing.T) {
	tests := []struct {
		name       string
		attachment Attachment
		pathOrNote string
		wantParts  []string
	}{
		{
			name:       "image with dimensions",
			attachment: Attachment{ContentType: "image/jpeg", Filename: "photo.jpg", Size: 245760, Width: 1920, Height: 1080},
			pathOrNote: "/tmp/photo.jpg",
			wantParts:  []string{"image/jpeg", `filename="photo.jpg"`, "245760 bytes", "1920x1080", "/tmp/photo.jpg"},
		},
		{
			name:       "document without dimensions",
			attachment: Attachment{ContentType: "application/pdf", Filename: "report.pdf", Size: 50000},
			pathOrNote: "",
			wantParts:  []string{"application/pdf", `filename="report.pdf"`, "50000 bytes"},
		},
		{
			name:       "file not available",
			attachment: Attachment{ContentType: "audio/ogg", ID: "abc", Size: 30000},
			pathOrNote: "file not available",
			wantParts:  []string{"audio/ogg", "30000 bytes", "file not available"},
		},
		{
			name:       "exceeds size limit",
			attachment: Attachment{ContentType: "video/mp4", Filename: "big.mp4", Size: 99999999},
			pathOrNote: "exceeds size limit",
			wantParts:  []string{"video/mp4", "exceeds size limit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeAttachment(tt.attachment, tt.pathOrNote)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("description should contain %q, got: %q", part, got)
				}
			}
			if !strings.HasPrefix(got, "[Attachment:") {
				t.Errorf("should start with [Attachment:, got: %q", got)
			}
			if !strings.HasSuffix(got, "]") {
				t.Errorf("should end with ], got: %q", got)
			}
		})
	}
}

func TestProcessAttachments_CopiesFile(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a fake attachment file.
	attachmentID := "test-attachment-123"
	content := []byte("fake image data")
	if err := os.WriteFile(filepath.Join(srcDir, attachmentID), content, 0644); err != nil {
		t.Fatal(err)
	}

	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Attachments = AttachmentConfig{
			SourceDir: srcDir,
			DestDir:   destDir,
		}
	})

	attachments := []Attachment{
		{ContentType: "image/jpeg", Filename: "photo.jpg", ID: attachmentID, Size: int64(len(content))},
	}

	descs := bridge.processAttachments(context.Background(), attachments, "+15551234567", "conv-test", time.Now())
	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}

	// Verify file was copied.
	destPath := filepath.Join(destDir, "photo.jpg")
	copied, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("attachment not copied to %s: %v", destPath, err)
	}
	if string(copied) != string(content) {
		t.Errorf("copied content mismatch: got %q, want %q", copied, content)
	}

	// Description should reference the destination path.
	if !strings.Contains(descs[0], destPath) {
		t.Errorf("description should contain dest path %q, got: %q", destPath, descs[0])
	}
}

func TestProcessAttachments_MissingFile(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Attachments = AttachmentConfig{
			SourceDir: srcDir,
			DestDir:   destDir,
		}
	})

	attachments := []Attachment{
		{ContentType: "image/jpeg", ID: "nonexistent", Size: 1000},
	}

	descs := bridge.processAttachments(context.Background(), attachments, "+15551234567", "conv-test", time.Now())
	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}
	if !strings.Contains(descs[0], "file not available") {
		t.Errorf("should indicate file not available, got: %q", descs[0])
	}
}

func TestProcessAttachments_ExceedsMaxSize(t *testing.T) {
	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Attachments = AttachmentConfig{
			SourceDir: t.TempDir(),
			DestDir:   t.TempDir(),
			MaxSize:   1000,
		}
	})

	attachments := []Attachment{
		{ContentType: "video/mp4", Filename: "big.mp4", ID: "abc", Size: 50000},
	}

	descs := bridge.processAttachments(context.Background(), attachments, "+15551234567", "conv-test", time.Now())
	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}
	if !strings.Contains(descs[0], "exceeds size limit") {
		t.Errorf("should indicate size exceeded, got: %q", descs[0])
	}
}

func TestProcessAttachments_NoDirsConfigured(t *testing.T) {
	bridge, _, _, _ := bridgeHelper(t)

	attachments := []Attachment{
		{ContentType: "image/jpeg", Filename: "photo.jpg", ID: "abc", Size: 1000},
	}

	descs := bridge.processAttachments(context.Background(), attachments, "+15551234567", "conv-test", time.Now())
	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}
	// Should describe the attachment without a path note.
	if !strings.Contains(descs[0], "image/jpeg") {
		t.Errorf("should contain content type, got: %q", descs[0])
	}
}

func TestProcessAttachments_PathTraversal(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create a fake attachment file.
	if err := os.WriteFile(filepath.Join(srcDir, "malicious"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Attachments = AttachmentConfig{
			SourceDir: srcDir,
			DestDir:   destDir,
		}
	})

	attachments := []Attachment{
		{ContentType: "image/jpeg", Filename: "../../../etc/passwd", ID: "malicious", Size: 4},
	}

	descs := bridge.processAttachments(context.Background(), attachments, "+15551234567", "conv-test", time.Now())
	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}

	// File should be written inside destDir with basename only.
	destPath := filepath.Join(destDir, "passwd")
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("file should be saved with sanitized name in dest dir: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("copied content = %q, want %q", got, "data")
	}

	// Description should reference the sanitized dest path inside
	// destDir, not a traversal path outside it.
	if !strings.Contains(descs[0], destPath) {
		t.Errorf("description should reference dest path %q, got: %q", destPath, descs[0])
	}
	// The saved path component (after " — ") must not contain "..".
	parts := strings.SplitN(descs[0], " — ", 2)
	if len(parts) == 2 && strings.Contains(parts[1], "..") {
		t.Errorf("saved path should not contain traversal components: %q", parts[1])
	}
}

func TestProcessAttachments_FilenameCollision(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	// Create two fake attachment files with different IDs.
	if err := os.WriteFile(filepath.Join(srcDir, "abc"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "def"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	bridge, _, _, _ := bridgeHelper(t, func(cfg *BridgeConfig) {
		cfg.Attachments = AttachmentConfig{
			SourceDir: srcDir,
			DestDir:   destDir,
		}
	})

	// Process first attachment.
	descs1 := bridge.processAttachments(context.Background(), []Attachment{
		{ContentType: "image/jpeg", Filename: "photo.jpg", ID: "abc", Size: 5},
	}, "+15551234567", "conv-test", time.Now())
	if len(descs1) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs1))
	}

	// Process second attachment with same filename — should not overwrite.
	descs2 := bridge.processAttachments(context.Background(), []Attachment{
		{ContentType: "image/jpeg", Filename: "photo.jpg", ID: "def", Size: 6},
	}, "+15551234567", "conv-test", time.Now())
	if len(descs2) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs2))
	}

	// Both descriptions should reference dest paths.
	if !strings.Contains(descs1[0], filepath.Join(destDir, "photo.jpg")) {
		t.Errorf("first should use original name, got: %q", descs1[0])
	}
	// Second should have a different path (timestamp suffix).
	if descs1[0] == descs2[0] {
		t.Error("second attachment should have a different dest path than first")
	}

	// Verify both files exist and have correct contents.
	first, err := os.ReadFile(filepath.Join(destDir, "photo.jpg"))
	if err != nil {
		t.Fatalf("first file missing: %v", err)
	}
	if string(first) != "first" {
		t.Errorf("first file content = %q, want %q", first, "first")
	}
}

func TestProcessAttachments_WithStore(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "signal-src")
	storeDir := filepath.Join(dir, "store")
	dbPath := filepath.Join(dir, "test.db")

	os.MkdirAll(srcDir, 0o750)
	os.WriteFile(filepath.Join(srcDir, "att-001"), []byte("image data"), 0o640)

	store, err := attachments.NewStore(dbPath, storeDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	bridge := NewBridge(BridgeConfig{
		Client: &Client{},
		Runner: &testRunner{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Attachments: AttachmentConfig{
			SourceDir: srcDir,
		},
		AttachmentStore: store,
	})

	descs := bridge.processAttachments(context.Background(), []Attachment{
		{ContentType: "image/jpeg", Filename: "photo.jpg", ID: "att-001", Size: 10, Width: 800, Height: 600},
	}, "+15559999999", "conv-store-001", time.Now())

	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}

	// Description should reference a path in the store, not the legacy dest.
	if !strings.Contains(descs[0], storeDir) {
		t.Errorf("expected store path in description, got: %q", descs[0])
	}

	// Verify file exists in the content-addressed path.
	if !strings.Contains(descs[0], ".jpg") {
		t.Errorf("expected .jpg extension in store path, got: %q", descs[0])
	}

	// Verify metadata was recorded — compute the expected hash for "image data".
	h := sha256.Sum256([]byte("image data"))
	expectedHash := hex.EncodeToString(h[:])
	rec, err := store.ByHash(context.Background(), expectedHash)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("ByHash returned nil for ingested attachment")
	}
	if rec.Channel != "signal" {
		t.Errorf("Channel = %q, want %q", rec.Channel, "signal")
	}
	if rec.Sender != "+15559999999" {
		t.Errorf("Sender = %q, want %q", rec.Sender, "+15559999999")
	}
	if rec.ConversationID != "conv-store-001" {
		t.Errorf("ConversationID = %q, want %q", rec.ConversationID, "conv-store-001")
	}
	if rec.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want %q", rec.ContentType, "image/jpeg")
	}
	if rec.Width != 800 || rec.Height != 600 {
		t.Errorf("dimensions = %dx%d, want 800x600", rec.Width, rec.Height)
	}
}

func TestProcessAttachments_WithStore_MissingSource(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "signal-src")
	storeDir := filepath.Join(dir, "store")
	dbPath := filepath.Join(dir, "test.db")

	os.MkdirAll(srcDir, 0o750)
	// Do NOT create the source file — it should be missing.

	store, err := attachments.NewStore(dbPath, storeDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	bridge := NewBridge(BridgeConfig{
		Client: &Client{},
		Runner: &testRunner{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Attachments: AttachmentConfig{
			SourceDir: srcDir,
		},
		AttachmentStore: store,
	})

	descs := bridge.processAttachments(context.Background(), []Attachment{
		{ContentType: "image/jpeg", ID: "missing-att", Size: 100},
	}, "+15559999999", "conv-store-002", time.Now())

	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}
	if !strings.Contains(descs[0], "file not available") {
		t.Errorf("expected 'file not available' in description, got: %q", descs[0])
	}
}

func TestProcessAttachments_WithStore_Dedup(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "signal-src")
	storeDir := filepath.Join(dir, "store")
	dbPath := filepath.Join(dir, "test.db")

	os.MkdirAll(srcDir, 0o750)
	// Two source files with identical content.
	os.WriteFile(filepath.Join(srcDir, "att-A"), []byte("same content"), 0o640)
	os.WriteFile(filepath.Join(srcDir, "att-B"), []byte("same content"), 0o640)

	store, err := attachments.NewStore(dbPath, storeDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	bridge := NewBridge(BridgeConfig{
		Client: &Client{},
		Runner: &testRunner{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Attachments: AttachmentConfig{
			SourceDir: srcDir,
		},
		AttachmentStore: store,
	})

	descs1 := bridge.processAttachments(context.Background(), []Attachment{
		{ContentType: "text/plain", ID: "att-A", Size: 12},
	}, "+15551111111", "conv-A", time.Now())

	descs2 := bridge.processAttachments(context.Background(), []Attachment{
		{ContentType: "text/plain", ID: "att-B", Size: 12},
	}, "+15552222222", "conv-B", time.Now())

	if len(descs1) != 1 || len(descs2) != 1 {
		t.Fatal("expected 1 description each")
	}

	// Both should reference the same store path (same content hash).
	if !strings.Contains(descs1[0], storeDir) || !strings.Contains(descs2[0], storeDir) {
		t.Fatal("both should reference the store directory")
	}

	// Extract the absolute store path from each description and verify
	// they are identical (true dedup: one file on disk).
	extractPath := func(desc string) string {
		// Description format: "[type ...] path-or-note"
		idx := strings.Index(desc, storeDir)
		if idx == -1 {
			return ""
		}
		end := strings.IndexAny(desc[idx:], "] ")
		if end == -1 {
			return desc[idx:]
		}
		return desc[idx : idx+end]
	}
	path1 := extractPath(descs1[0])
	path2 := extractPath(descs2[0])
	if path1 != path2 {
		t.Errorf("expected deduplicated attachments to share store path, got %q and %q", path1, path2)
	}
}

func TestFormatMessage_WithAttachments(t *testing.T) {
	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Message:   "Look at this",
		},
	}
	descs := []string{"[Attachment: image/jpeg, 1000 bytes]"}
	got := formatMessage(env, descs)

	if !strings.Contains(got, "[Attachment: image/jpeg") {
		t.Errorf("should contain attachment description, got: %q", got)
	}
	if !strings.Contains(got, "Look at this") {
		t.Errorf("should contain message text, got: %q", got)
	}
}

func TestFormatMessage_AttachmentOnlyNoText(t *testing.T) {
	env := &Envelope{
		Source:    "+15551234567",
		Timestamp: 1700000000000,
		DataMessage: &DataMessage{
			Timestamp: 1700000000000,
			Message:   "",
		},
	}
	descs := []string{"[Attachment: audio/ogg, 5000 bytes]"}
	got := formatMessage(env, descs)

	if !strings.Contains(got, "[Attachment: audio/ogg") {
		t.Errorf("should contain attachment description, got: %q", got)
	}
	// Should not have a double newline between attachment desc and empty text.
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("should not end with double newline for attachment-only, got: %q", got)
	}
}
