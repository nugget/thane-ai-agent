package signal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// AgentRunner abstracts the agent loop for testability. The real
// implementation is *agent.Loop.
type AgentRunner interface {
	Run(ctx context.Context, req *agent.Request, stream agent.StreamCallback) (*agent.Response, error)
}

// SessionRotator ends the active session for a conversation, triggering
// background summarization. The agent loop's EnsureSession call creates
// a fresh session on the next message automatically.
type SessionRotator interface {
	// RotateIdleSession ends the active session for conversationID.
	// Returns true if a session was ended. No-op if no active session.
	RotateIdleSession(conversationID string) bool
}

// handleTimeout bounds how long a single inbound message may be
// processed (agent loop + response send).
const handleTimeout = 5 * time.Minute

// rateWindow is the sliding window for per-sender rate limiting.
const rateWindow = time.Minute

// cleanupInterval controls how often stale rate-limit entries are
// evicted.
const cleanupInterval = 10 * time.Minute

// lastMessage tracks a sender's most recent inbound message timestamp
// along with when we received it, for bounded cleanup.
type lastMessage struct {
	signalTS   int64     // signal-cli message timestamp
	receivedAt time.Time // wall clock when we stored it
}

// BridgeConfig holds the dependencies for a Bridge.
type BridgeConfig struct {
	Client      *Client
	Runner      AgentRunner
	Logger      *slog.Logger
	RateLimit   int                        // per sender per minute; 0 = unlimited
	Routing     config.SignalRoutingConfig // model selection and routing hints
	Rotator     SessionRotator             // nil disables idle session rotation
	IdleTimeout time.Duration              // 0 disables idle session rotation
}

// Bridge receives Signal messages from the signal-cli client, routes
// them through the agent loop, and sends responses back via Signal.
type Bridge struct {
	client      *Client
	runner      AgentRunner
	logger      *slog.Logger
	rateLimit   int
	routing     config.SignalRoutingConfig
	rotator     SessionRotator
	idleTimeout time.Duration

	mu            sync.Mutex
	senderTimes   map[string][]time.Time
	lastCleanup   time.Time
	lastInboundTS map[string]lastMessage // most recent message per sender
}

// NewBridge creates a Signal message bridge.
func NewBridge(cfg BridgeConfig) *Bridge {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Bridge{
		client:        cfg.Client,
		runner:        cfg.Runner,
		logger:        logger,
		rateLimit:     cfg.RateLimit,
		routing:       cfg.Routing,
		rotator:       cfg.Rotator,
		idleTimeout:   cfg.IdleTimeout,
		senderTimes:   make(map[string][]time.Time),
		lastInboundTS: make(map[string]lastMessage),
	}
}

// LastInboundTimestamp returns the most recent message timestamp
// received from the given sender. The tool handler uses this to
// resolve the "latest" sentinel for reactions.
func (b *Bridge) LastInboundTimestamp(sender string) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lm, ok := b.lastInboundTS[sender]
	return lm.signalTS, ok
}

// Start receives messages from the signal-cli client and routes them
// through the agent loop until ctx is cancelled.
func (b *Bridge) Start(ctx context.Context) {
	b.logger.Info("signal bridge started")

	for {
		select {
		case <-ctx.Done():
			b.logger.Info("signal bridge shutting down")
			return
		case env, ok := <-b.client.Messages():
			if !ok {
				b.logger.Info("signal message channel closed, bridge stopping")
				return
			}

			if env.Source == "" {
				b.logger.Debug("signal ignoring envelope with empty source")
				continue
			}

			// Track the most recent inbound timestamp for this
			// sender (even for non-text messages like attachments)
			// so the signal_send_reaction tool can resolve "latest".
			if env.DataMessage != nil {
				ts := env.Timestamp
				if env.DataMessage.Timestamp != 0 {
					ts = env.DataMessage.Timestamp
				}
				b.mu.Lock()
				b.lastInboundTS[env.Source] = lastMessage{
					signalTS:   ts,
					receivedAt: time.Now(),
				}
				b.mu.Unlock()
			}

			if env.DataMessage == nil || env.DataMessage.Message == "" {
				b.logger.Debug("signal ignoring non-text envelope",
					"sender", env.Source,
				)
				continue
			}

			if !b.allowSender(env.Source) {
				b.logger.Warn("signal message rate-limited",
					"sender", env.Source,
				)
				continue
			}

			// Acknowledge receipt before the potentially long agent
			// loop. Best-effort — failure does not prevent processing.
			// Prefer the data message timestamp when available; fall
			// back to the envelope timestamp.
			receiptTS := env.Timestamp
			if env.DataMessage != nil && env.DataMessage.Timestamp != 0 {
				receiptTS = env.DataMessage.Timestamp
			}

			if err := b.client.SendReceipt(ctx, env.Source, receiptTS); err != nil {
				b.logger.Warn("signal read receipt failed",
					"sender", env.Source,
					"error", err,
				)
			}

			b.handleMessage(ctx, env)
		}
	}
}

// handleMessage processes a single inbound Signal message: runs it
// through the agent loop and sends the response back to the sender.
func (b *Bridge) handleMessage(ctx context.Context, env *Envelope) {
	ctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()

	sender := env.Source
	convID := fmt.Sprintf("signal-%s", sanitizePhone(sender))
	content := formatMessage(env)

	b.logger.Info("signal message received",
		"sender", sender,
		"conversation_id", convID,
		"message_len", len(env.DataMessage.Message),
	)

	// Rotate the session if the sender has been idle longer than
	// the configured timeout. The agent loop's deferred
	// EnsureSession call will create a fresh session automatically.
	if b.idleTimeout > 0 && b.rotator != nil {
		b.mu.Lock()
		lm, exists := b.lastInboundTS[sender]
		b.mu.Unlock()
		if exists && time.Since(lm.receivedAt) > b.idleTimeout {
			if b.rotator.RotateIdleSession(convID) {
				b.logger.Info("signal session rotated (idle)",
					"sender", sender,
					"conversation_id", convID,
					"idle_duration", time.Since(lm.receivedAt).Round(time.Second),
				)
			}
		}
	}

	// Send typing indicator before agent processing.
	if err := b.client.SendTyping(ctx, sender, false); err != nil {
		b.logger.Debug("signal typing indicator failed", "error", err)
	}

	req := &agent.Request{
		ConversationID: convID,
		Messages:       []agent.Message{{Role: "user", Content: content}},
		Model:          b.routing.Model,
		Hints: map[string]string{
			"source":                    "signal",
			"sender":                    sender,
			router.HintQualityFloor:     b.routing.QualityFloor,
			router.HintMission:          b.routing.Mission,
			router.HintDelegationGating: b.routing.DelegationGating,
		},
	}

	resp, err := b.runner.Run(ctx, req, nil)

	// Stop typing indicator regardless of outcome. Use a fresh
	// background context so this best-effort cleanup runs even if
	// the handler context has timed out or been cancelled.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if typErr := b.client.SendTyping(stopCtx, sender, true); typErr != nil {
		b.logger.Debug("signal typing stop failed", "error", typErr)
	}

	if err != nil {
		b.logger.Error("signal agent run failed",
			"sender", sender,
			"conversation_id", convID,
			"error", err,
		)
		return
	}

	b.logger.Info("signal agent run completed",
		"sender", sender,
		"conversation_id", convID,
		"response_len", len(resp.Content),
		"model", resp.Model,
	)

	// If the agent already called signal_send_message during its tool
	// loop, skip the bridge-level send to avoid duplicate messages.
	if agentAlreadySent(resp.ToolsUsed) {
		b.logger.Info("signal reply already sent by agent tool call",
			"sender", sender,
			"conversation_id", convID,
		)
		return
	}

	if resp.Content == "" {
		return
	}

	b.logger.Info("signal sending reply",
		"sender", sender,
		"conversation_id", convID,
		"response_len", len(resp.Content),
	)

	if _, err := b.client.Send(ctx, sender, resp.Content); err != nil {
		b.logger.Error("signal reply send failed",
			"sender", sender,
			"conversation_id", convID,
			"error", err,
		)
		return
	}

	b.logger.Info("signal reply sent",
		"sender", sender,
		"conversation_id", convID,
	)
}

// allowSender checks whether the sender is within the per-minute rate
// limit. Returns true if the message should be processed.
func (b *Bridge) allowSender(senderID string) bool {
	if b.rateLimit <= 0 {
		return true
	}

	now := time.Now()
	cutoff := now.Add(-rateWindow)

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
func (b *Bridge) maybeCleanupLocked(now time.Time) {
	if now.Sub(b.lastCleanup) < cleanupInterval {
		return
	}
	b.lastCleanup = now

	cutoff := now.Add(-2 * rateWindow)
	for sender, timestamps := range b.senderTimes {
		if len(timestamps) == 0 {
			delete(b.senderTimes, sender)
			continue
		}
		if timestamps[len(timestamps)-1].Before(cutoff) {
			delete(b.senderTimes, sender)
		}
	}

	// Evict stale lastInboundTS entries to prevent unbounded growth.
	// Entries older than 2× the cleanup interval are unlikely to be
	// useful reaction targets.
	tsCutoff := now.Add(-2 * cleanupInterval)
	for sender, lm := range b.lastInboundTS {
		if lm.receivedAt.Before(tsCutoff) {
			delete(b.lastInboundTS, sender)
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

// formatMessage builds the user-facing message content for the agent
// loop from a received Signal envelope. The [ts:...] tag provides the
// message timestamp so the agent can reference it for reactions.
func formatMessage(env *Envelope) string {
	var sb strings.Builder
	sender := env.Source
	if env.SourceName != "" {
		sender = fmt.Sprintf("%s (%s)", env.SourceName, env.Source)
	}

	// Use the data message timestamp when available; fall back to
	// the envelope timestamp.
	ts := env.Timestamp
	if env.DataMessage != nil && env.DataMessage.Timestamp != 0 {
		ts = env.DataMessage.Timestamp
	}

	if env.DataMessage.GroupInfo != nil {
		fmt.Fprintf(&sb, "Signal message from %s in group %s [ts:%d]:\n\n", sender, env.DataMessage.GroupInfo.GroupID, ts)
	} else {
		fmt.Fprintf(&sb, "Signal message from %s [ts:%d]:\n\n", sender, ts)
	}
	sb.WriteString(env.DataMessage.Message)
	return sb.String()
}

// agentAlreadySent reports whether the agent invoked a
// signal_send_message tool during its loop.
func agentAlreadySent(toolsUsed map[string]int) bool {
	for name, count := range toolsUsed {
		if count > 0 && strings.HasSuffix(name, "signal_send_message") {
			return true
		}
	}
	return false
}

// truncate returns s truncated to maxLen runes with an ellipsis if it
// exceeds the limit.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
