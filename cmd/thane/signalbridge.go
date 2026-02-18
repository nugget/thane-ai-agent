package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// signalMCPCaller abstracts the MCP client for testability. The real
// implementation is *mcp.Client.
type signalMCPCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// signalHandleTimeout bounds how long a single inbound message may be
// processed (agent loop + response send).
const signalHandleTimeout = 5 * time.Minute

// signalBackoffInit is the initial delay after a poll error.
const signalBackoffInit = 5 * time.Second

// signalBackoffMax is the maximum delay between poll retries.
const signalBackoffMax = 60 * time.Second

// signalRateWindow is the sliding window for per-sender rate limiting.
const signalRateWindow = time.Minute

// signalCleanupInterval controls how often stale rate-limit entries are
// evicted.
const signalCleanupInterval = 10 * time.Minute

// SignalBridgeConfig holds the dependencies for a SignalBridge.
type SignalBridgeConfig struct {
	MCP         signalMCPCaller
	Runner      agentRunner
	Logger      *slog.Logger
	PollTimeout int                        // seconds, passed to receive_message
	RateLimit   int                        // per sender per minute; 0 = unlimited
	Routing     config.SignalRoutingConfig // model selection and routing hints
}

// SignalBridge polls the signal-mcp receive_message tool for incoming
// messages and routes them through the agent loop, sending responses
// back via send_message_to_user.
type SignalBridge struct {
	mcp         signalMCPCaller
	runner      agentRunner
	logger      *slog.Logger
	pollTimeout int
	rateLimit   int
	routing     config.SignalRoutingConfig

	mu          sync.Mutex
	senderTimes map[string][]time.Time
	lastCleanup time.Time
}

// signalMessage is the parsed response from the signal-mcp
// receive_message tool.
type signalMessage struct {
	Message   string `json:"message"`
	SenderID  string `json:"sender_id"`
	GroupName string `json:"group_name"`
	Timestamp int64  `json:"timestamp"`
	Error     string `json:"error"`
}

// NewSignalBridge creates a Signal message bridge.
func NewSignalBridge(cfg SignalBridgeConfig) *SignalBridge {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &SignalBridge{
		mcp:         cfg.MCP,
		runner:      cfg.Runner,
		logger:      logger,
		pollTimeout: cfg.PollTimeout,
		rateLimit:   cfg.RateLimit,
		routing:     cfg.Routing,
		senderTimes: make(map[string][]time.Time),
	}
}

// Start runs the long-polling loop until ctx is cancelled.
func (b *SignalBridge) Start(ctx context.Context) {
	b.logger.Info("signal bridge polling started")

	backoff := signalBackoffInit
	for {
		if ctx.Err() != nil {
			b.logger.Info("signal bridge shutting down")
			return
		}

		result, err := b.mcp.CallTool(ctx, "receive_message", map[string]any{
			"timeout": b.pollTimeout,
		})
		if err != nil {
			if ctx.Err() != nil {
				b.logger.Info("signal bridge shutting down")
				return
			}
			b.logger.Error("signal poll failed",
				"error", err,
				"backoff", backoff,
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, signalBackoffMax)
			continue
		}

		// Reset backoff on successful poll (even if no message).
		backoff = signalBackoffInit

		var msg signalMessage
		if err := json.Unmarshal([]byte(result), &msg); err != nil {
			b.logger.Debug("signal poll returned non-JSON response",
				"result", truncate(result, 200),
			)
			continue
		}

		if msg.Error != "" {
			b.logger.Warn("signal receive_message error",
				"error", msg.Error,
			)
			continue
		}

		if msg.SenderID == "" || msg.Message == "" {
			b.logger.Debug("signal poll timeout or empty message")
			continue
		}

		if !b.allowSender(msg.SenderID) {
			b.logger.Warn("signal message rate-limited",
				"sender", msg.SenderID,
			)
			continue
		}

		// Acknowledge receipt before the potentially long agent loop.
		// Best-effort — failure does not prevent message processing.
		if _, err := b.mcp.CallTool(ctx, "send_read_receipt", map[string]any{
			"user_id":    msg.SenderID,
			"timestamps": []int64{msg.Timestamp},
		}); err != nil {
			b.logger.Warn("signal read receipt failed",
				"sender", msg.SenderID,
				"error", err,
			)
		}

		// If the context was cancelled during read receipt, shut down
		// without launching a redundant handler goroutine.
		if ctx.Err() != nil {
			b.logger.Info("signal bridge shutting down", "error", ctx.Err())
			return
		}

		// Wait for message handling (including reply send) to complete
		// before resuming the poll. The stdio transport mutex is
		// single-threaded; re-entering receive_message before the reply
		// send would starve send_message_to_user.
		done := make(chan struct{})
		go b.handleMessage(ctx, msg, done)
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

// handleMessage processes a single inbound Signal message: runs it
// through the agent loop and sends the response back to the sender.
// The done channel is closed when the function returns, signalling the
// poll loop that the transport mutex is free.
func (b *SignalBridge) handleMessage(ctx context.Context, msg signalMessage, done chan<- struct{}) {
	defer close(done)

	ctx, cancel := context.WithTimeout(ctx, signalHandleTimeout)
	defer cancel()

	convID := fmt.Sprintf("signal-%s", sanitizePhone(msg.SenderID))
	content := formatSignalMessage(msg)

	b.logger.Info("signal message received",
		"sender", msg.SenderID,
		"conversation_id", convID,
		"group", msg.GroupName,
		"message_len", len(msg.Message),
	)

	req := &agent.Request{
		ConversationID: convID,
		Messages:       []agent.Message{{Role: "user", Content: content}},
		Model:          b.routing.Model,
		Hints: map[string]string{
			"source":                    "signal",
			"sender":                    msg.SenderID,
			router.HintQualityFloor:     b.routing.QualityFloor,
			router.HintMission:          b.routing.Mission,
			router.HintDelegationGating: b.routing.DelegationGating,
		},
	}

	resp, err := b.runner.Run(ctx, req, nil)
	if err != nil {
		b.logger.Error("signal agent run failed",
			"sender", msg.SenderID,
			"conversation_id", convID,
			"error", err,
		)
		return
	}

	b.logger.Info("signal agent run completed",
		"sender", msg.SenderID,
		"conversation_id", convID,
		"response_len", len(resp.Content),
		"model", resp.Model,
	)

	// If the agent already called send_message_to_user during its tool
	// loop, skip the bridge-level send to avoid duplicate messages.
	if agentAlreadySent(resp.ToolsUsed) {
		b.logger.Info("signal reply already sent by agent tool call",
			"sender", msg.SenderID,
			"conversation_id", convID,
		)
		return
	}

	if resp.Content == "" {
		return
	}

	b.logger.Info("signal sending reply",
		"sender", msg.SenderID,
		"conversation_id", convID,
		"response_len", len(resp.Content),
	)

	_, err = b.mcp.CallTool(ctx, "send_message_to_user", map[string]any{
		"user_id": msg.SenderID,
		"message": resp.Content,
	})
	if err != nil {
		b.logger.Error("signal reply send failed",
			"sender", msg.SenderID,
			"conversation_id", convID,
			"error", err,
		)
		return
	}

	b.logger.Info("signal reply sent",
		"sender", msg.SenderID,
		"conversation_id", convID,
	)
}

// allowSender checks whether the sender is within the per-minute rate
// limit. Returns true if the message should be processed.
func (b *SignalBridge) allowSender(senderID string) bool {
	if b.rateLimit <= 0 {
		return true
	}

	now := time.Now()
	cutoff := now.Add(-signalRateWindow)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.maybeCleanupLocked(now)

	// Prune expired timestamps for this sender.
	timestamps := b.senderTimes[senderID]
	valid := timestamps[:0]
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= b.rateLimit {
		b.senderTimes[senderID] = valid
		return false
	}

	b.senderTimes[senderID] = append(valid, now)
	return true
}

// maybeCleanupLocked evicts stale sender entries. Must be called with
// b.mu held.
func (b *SignalBridge) maybeCleanupLocked(now time.Time) {
	if now.Sub(b.lastCleanup) < signalCleanupInterval {
		return
	}
	b.lastCleanup = now

	cutoff := now.Add(-2 * signalRateWindow)
	for sender, timestamps := range b.senderTimes {
		if len(timestamps) == 0 {
			delete(b.senderTimes, sender)
			continue
		}
		// If the most recent timestamp is old enough, evict the entry.
		if timestamps[len(timestamps)-1].Before(cutoff) {
			delete(b.senderTimes, sender)
		}
	}
}

// sanitizePhone strips non-alphanumeric characters from a phone number
// to produce a safe conversation ID component.
func sanitizePhone(phone string) string {
	var sb strings.Builder
	for _, r := range phone {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// formatSignalMessage builds the user-facing message content for the
// agent loop from a received Signal message.
func formatSignalMessage(msg signalMessage) string {
	var sb strings.Builder
	if msg.GroupName != "" {
		fmt.Fprintf(&sb, "Signal message from %s in group %q:\n\n", msg.SenderID, msg.GroupName)
	} else {
		fmt.Fprintf(&sb, "Signal message from %s:\n\n", msg.SenderID)
	}
	sb.WriteString(msg.Message)
	return sb.String()
}

// agentAlreadySent reports whether the agent invoked a
// send_message_to_user tool during its loop. The MCP bridge prefixes
// tool names (e.g. "mcp_signal_send_message_to_user"), so we match on
// the suffix.
func agentAlreadySent(toolsUsed map[string]int) bool {
	for name, count := range toolsUsed {
		if count > 0 && strings.HasSuffix(name, "send_message_to_user") {
			return true
		}
	}
	return false
}

// truncate returns s truncated to maxLen characters with an ellipsis if
// it exceeds the limit.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
