package signal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// ContactResolver resolves a phone number to a contact name. The bridge
// uses this to inject a sender_name hint into agent requests so the
// channel provider can greet users by name.
type ContactResolver interface {
	// ResolvePhone returns the contact name for the given phone number.
	// Returns ("", false) if no matching contact is found.
	ResolvePhone(phone string) (name string, ok bool)
}

// handleTimeout bounds how long a single inbound message may be
// processed (agent loop + response send).
const handleTimeout = 5 * time.Minute

// rateWindow is the sliding window for per-sender rate limiting.
const rateWindow = time.Minute

// cleanupInterval controls how often stale rate-limit entries are
// evicted.
const cleanupInterval = 10 * time.Minute

// typingRefreshInterval is how often the typing indicator is re-sent
// during long-running agent processing. Signal's typing indicator
// times out after ~15 seconds, so 10 seconds keeps it alive.
const typingRefreshInterval = 10 * time.Second

// lastMessage tracks a sender's most recent inbound message timestamp
// along with when we received it, for bounded cleanup.
type lastMessage struct {
	signalTS   int64     // signal-cli message timestamp
	receivedAt time.Time // wall clock when we stored it
}

// AttachmentConfig configures how the bridge handles received
// attachments from Signal.
type AttachmentConfig struct {
	// SourceDir is the directory where signal-cli stores downloaded
	// attachments (e.g., "~/.local/share/signal-cli/attachments").
	SourceDir string

	// DestDir is the workspace subdirectory where attachments are
	// copied for agent access.
	DestDir string

	// MaxSize is the maximum attachment size in bytes that will be
	// copied. Attachments exceeding this are noted but not copied.
	// Zero means no limit.
	MaxSize int64
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
	Resolver    ContactResolver            // nil disables phone→name resolution
	Attachments AttachmentConfig           // attachment storage configuration
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
	resolver    ContactResolver
	attachments AttachmentConfig

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
	if cfg.Attachments.DestDir != "" {
		if err := os.MkdirAll(cfg.Attachments.DestDir, 0750); err != nil {
			logger.Warn("signal attachment dest dir creation failed",
				"dir", cfg.Attachments.DestDir,
				"error", err,
			)
		}
	}
	return &Bridge{
		client:        cfg.Client,
		runner:        cfg.Runner,
		logger:        logger,
		rateLimit:     cfg.RateLimit,
		routing:       cfg.Routing,
		rotator:       cfg.Rotator,
		idleTimeout:   cfg.IdleTimeout,
		resolver:      cfg.Resolver,
		attachments:   cfg.Attachments,
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

			// Reactions are carried inside dataMessage but have no
			// text. Intercept them before the content filter.
			if env.DataMessage != nil && env.DataMessage.Reaction != nil {
				if !b.allowSender(env.Source) {
					b.logger.Warn("signal reaction rate-limited",
						"sender", env.Source,
					)
					continue
				}
				b.handleReaction(ctx, env)
				continue
			}

			// For DataMessages with neither text nor attachments,
			// track the signal timestamp so the reaction tool's
			// "latest" resolution still works. Messages with content
			// update lastInboundTS inside handleMessage (after the
			// idle rotation check).
			hasContent := env.DataMessage != nil &&
				(env.DataMessage.Message != "" || len(env.DataMessage.Attachments) > 0)
			if env.DataMessage != nil && !hasContent {
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

			if !hasContent {
				b.logger.Debug("signal ignoring envelope without content",
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

	// Process attachments before formatting the message.
	var attachmentDescs []string
	if len(env.DataMessage.Attachments) > 0 {
		if env.DataMessage.ViewOnce {
			attachmentDescs = []string{"[View-once attachment — not available]"}
		} else {
			attachmentDescs = b.processAttachments(env.DataMessage.Attachments)
		}
	}
	content := formatMessage(env, attachmentDescs)

	b.logger.Info("signal message received",
		"sender", sender,
		"conversation_id", convID,
		"message_len", len(env.DataMessage.Message),
		"attachments", len(env.DataMessage.Attachments),
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

	// Update lastInboundTS *after* the idle check so the check
	// reads the previous message's wall-clock time, not the current
	// one. This also serves the reaction tool's "latest" resolution.
	ts := env.Timestamp
	if env.DataMessage != nil && env.DataMessage.Timestamp != 0 {
		ts = env.DataMessage.Timestamp
	}
	b.mu.Lock()
	b.lastInboundTS[sender] = lastMessage{
		signalTS:   ts,
		receivedAt: time.Now(),
	}
	b.mu.Unlock()

	// Start typing indicator refresh loop. The goroutine sends an
	// initial indicator and then re-sends every 10s to keep it
	// visible during long agent processing.
	stopTyping := b.startTypingRefresh(ctx, sender)

	hints := map[string]string{
		"source":                    "signal",
		"sender":                    sender,
		router.HintQualityFloor:     b.routing.QualityFloor,
		router.HintMission:          b.routing.Mission,
		router.HintDelegationGating: b.routing.DelegationGating,
	}

	// Resolve sender phone number to a contact name when available.
	if b.resolver != nil {
		if name, ok := b.resolver.ResolvePhone(sender); ok {
			hints["sender_name"] = name
		}
	}

	req := &agent.Request{
		ConversationID: convID,
		Messages:       []agent.Message{{Role: "user", Content: content}},
		Model:          b.routing.Model,
		Hints:          hints,
	}

	resp, err := b.runner.Run(ctx, req, nil)

	// Stop the typing refresh goroutine, then send a definitive
	// typing stop. Use a fresh background context so this
	// best-effort cleanup runs even if the handler context has
	// timed out or been cancelled.
	stopTyping()
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

// handleReaction processes an inbound emoji reaction. Reaction
// removals are logged but do not wake the agent. Non-removal
// reactions are forwarded to the agent loop with contextual hints.
func (b *Bridge) handleReaction(ctx context.Context, env *Envelope) {
	ctx, cancel := context.WithTimeout(ctx, handleTimeout)
	defer cancel()

	sender := env.Source
	reaction := env.DataMessage.Reaction
	convID := fmt.Sprintf("signal-%s", sanitizePhone(sender))

	if reaction.IsRemove {
		b.logger.Info("signal reaction removed",
			"sender", sender,
			"emoji", reaction.Emoji,
			"target_author", reaction.TargetAuthor,
			"target_timestamp", reaction.TargetSentTimestamp,
		)
		return
	}

	b.logger.Info("signal reaction received",
		"sender", sender,
		"emoji", reaction.Emoji,
		"target_author", reaction.TargetAuthor,
		"target_timestamp", reaction.TargetSentTimestamp,
		"conversation_id", convID,
	)

	stopTyping := b.startTypingRefresh(ctx, sender)

	content := formatReaction(env)

	hints := map[string]string{
		"source":                    "signal",
		"sender":                    sender,
		"event_type":                "reaction",
		"reaction_emoji":            reaction.Emoji,
		"target_sent_timestamp":     fmt.Sprintf("%d", reaction.TargetSentTimestamp),
		router.HintQualityFloor:     b.routing.QualityFloor,
		router.HintMission:          b.routing.Mission,
		router.HintDelegationGating: b.routing.DelegationGating,
	}

	if b.resolver != nil {
		if name, ok := b.resolver.ResolvePhone(sender); ok {
			hints["sender_name"] = name
		}
	}

	req := &agent.Request{
		ConversationID: convID,
		Messages:       []agent.Message{{Role: "user", Content: content}},
		Model:          b.routing.Model,
		Hints:          hints,
	}

	resp, err := b.runner.Run(ctx, req, nil)

	stopTyping()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if typErr := b.client.SendTyping(stopCtx, sender, true); typErr != nil {
		b.logger.Debug("signal typing stop failed", "error", typErr)
	}

	if err != nil {
		b.logger.Error("signal agent run failed (reaction)",
			"sender", sender,
			"conversation_id", convID,
			"error", err,
		)
		return
	}

	if agentAlreadySent(resp.ToolsUsed) || resp.Content == "" {
		return
	}

	if _, err := b.client.Send(ctx, sender, resp.Content); err != nil {
		b.logger.Error("signal reply send failed (reaction)",
			"sender", sender,
			"conversation_id", convID,
			"error", err,
		)
	}
}

// startTypingRefresh sends a typing indicator immediately, then
// refreshes it on a ticker until the returned cancel function is
// called. The caller must call the cancel function when processing
// is complete.
func (b *Bridge) startTypingRefresh(ctx context.Context, recipient string) context.CancelFunc {
	refreshCtx, cancel := context.WithCancel(ctx)

	// Send initial typing indicator.
	if err := b.client.SendTyping(ctx, recipient, false); err != nil {
		b.logger.Debug("signal typing indicator failed", "error", err)
	}

	go func() {
		ticker := time.NewTicker(typingRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				if err := b.client.SendTyping(refreshCtx, recipient, false); err != nil {
					b.logger.Debug("signal typing refresh failed", "error", err)
				}
			}
		}
	}()

	return cancel
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
// Attachment descriptions are prepended before the message text.
func formatMessage(env *Envelope, attachmentDescs []string) string {
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

	for _, desc := range attachmentDescs {
		sb.WriteString(desc)
		sb.WriteString("\n")
	}
	if len(attachmentDescs) > 0 && env.DataMessage.Message != "" {
		sb.WriteString("\n")
	}

	sb.WriteString(env.DataMessage.Message)
	return sb.String()
}

// formatReaction builds the user-facing message content for a
// reaction envelope. The output identifies the sender, the emoji,
// and the target message timestamp.
func formatReaction(env *Envelope) string {
	var sb strings.Builder
	sender := env.Source
	if env.SourceName != "" {
		sender = fmt.Sprintf("%s (%s)", env.SourceName, env.Source)
	}
	fmt.Fprintf(&sb, "Signal reaction from %s: %s on message [ts:%d] from %s",
		sender,
		env.DataMessage.Reaction.Emoji,
		env.DataMessage.Reaction.TargetSentTimestamp,
		env.DataMessage.Reaction.TargetAuthor,
	)
	return sb.String()
}

// processAttachments copies received attachments from signal-cli's
// storage to the workspace and returns a human-readable description
// of each attachment. Files that cannot be copied (missing, too
// large) are described but marked as unavailable.
func (b *Bridge) processAttachments(attachments []Attachment) []string {
	descs := make([]string, 0, len(attachments))
	for _, a := range attachments {
		if b.attachments.MaxSize > 0 && a.Size > b.attachments.MaxSize {
			descs = append(descs, describeAttachment(a, "exceeds size limit"))
			continue
		}

		if b.attachments.SourceDir == "" || b.attachments.DestDir == "" {
			descs = append(descs, describeAttachment(a, ""))
			continue
		}

		srcPath := filepath.Join(b.attachments.SourceDir, a.ID)
		if _, err := os.Stat(srcPath); err != nil {
			b.logger.Warn("signal attachment not found",
				"id", a.ID,
				"path", srcPath,
				"error", err,
			)
			descs = append(descs, describeAttachment(a, "file not available"))
			continue
		}

		// Use the original filename if available; fall back to ID.
		destName := a.ID
		if a.Filename != "" {
			destName = a.Filename
		}
		destPath := filepath.Join(b.attachments.DestDir, destName)

		if err := copyFile(srcPath, destPath); err != nil {
			b.logger.Warn("signal attachment copy failed",
				"src", srcPath,
				"dest", destPath,
				"error", err,
			)
			descs = append(descs, describeAttachment(a, "copy failed"))
			continue
		}

		b.logger.Info("signal attachment saved",
			"id", a.ID,
			"dest", destPath,
			"size", a.Size,
			"content_type", a.ContentType,
		)
		descs = append(descs, describeAttachment(a, destPath))
	}
	return descs
}

// describeAttachment builds a human-readable description of a single
// attachment for inclusion in the agent wake message.
func describeAttachment(a Attachment, pathOrNote string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Attachment: %s", a.ContentType)
	if a.Filename != "" {
		fmt.Fprintf(&sb, ", filename=%q", a.Filename)
	}
	if a.Size > 0 {
		fmt.Fprintf(&sb, ", %d bytes", a.Size)
	}
	if a.Width > 0 && a.Height > 0 {
		fmt.Fprintf(&sb, ", %dx%d", a.Width, a.Height)
	}
	if pathOrNote != "" {
		fmt.Fprintf(&sb, " — %s", pathOrNote)
	}
	sb.WriteString("]")
	return sb.String()
}

// copyFile copies src to dst. The destination file is created with
// mode 0644. Existing files are overwritten.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
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
