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

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// AgentRunner abstracts the loop-owned agent runner for testability.
// Production passes the app loop adapter so Signal requests share the
// same execution path as other loop turns.
type AgentRunner interface {
	Run(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error)
}

// ContactResolver resolves a channel/address pair to a typed channel
// binding. The bridge uses this to inject sender identity into agent
// requests and to persist contact-backed bindings on the conversation.
type ContactResolver interface {
	ResolveChannelBinding(channel, address string) *memory.ChannelBinding
}

// defaultHandleTimeout bounds how long a single inbound message may be
// processed (agent loop + response send) when no explicit timeout is
// configured.
const defaultHandleTimeout = 10 * time.Minute

// rateWindow is the sliding window for per-sender rate limiting.
const rateWindow = time.Minute

// cleanupInterval controls how often stale rate-limit entries are
// evicted.
const cleanupInterval = 10 * time.Minute

// typingRefreshInterval is how often the typing indicator is re-sent
// during long-running agent processing. Signal's typing indicator
// times out after ~15 seconds, so 10 seconds keeps it alive.
const typingRefreshInterval = 10 * time.Second

// senderChanSize is the buffer size for per-sender message channels.
// A small buffer prevents back-pressure on the parent loop while the
// child processes a message.
const senderChanSize = 4

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

// VisionAnalyzer describes an optional component that analyzes image
// attachments using a vision-capable LLM. When non-nil on the bridge,
// images are analyzed on ingest and descriptions enriched.
type VisionAnalyzer interface {
	Analyze(ctx context.Context, rec *attachments.Record) (string, error)
}

// BridgeConfig holds the dependencies for a Bridge.
type BridgeConfig struct {
	Client           *Client
	Runner           AgentRunner
	Logger           *slog.Logger
	RateLimit        int                                                               // per sender per minute; 0 = unlimited
	HandleTimeout    time.Duration                                                     // per-message processing timeout; 0 = defaultHandleTimeout
	Routing          config.SignalRoutingConfig                                        // model selection and routing hints
	Resolver         ContactResolver                                                   // nil disables phone→name resolution
	BindConversation func(conversationID string, binding *memory.ChannelBinding) error // nil disables conversation binding persistence
	Attachments      AttachmentConfig                                                  // attachment storage configuration
	AttachmentStore  *attachments.Store                                                // content-addressed store; nil = legacy copy
	VisionAnalyzer   VisionAnalyzer                                                    // nil disables vision analysis
	Registry         *loop.Registry                                                    // loop registry for dashboard visibility
	EventBus         *events.Bus                                                       // event bus for in-flight events
}

// Bridge receives Signal messages from the signal-cli client, routes
// them through the agent loop, and sends responses back via Signal.
type Bridge struct {
	client           *Client
	runner           AgentRunner
	logger           *slog.Logger
	rateLimit        int
	handleTimeout    time.Duration
	routing          config.SignalRoutingConfig
	resolver         ContactResolver
	bindConversation func(conversationID string, binding *memory.ChannelBinding) error
	attachments      AttachmentConfig
	attachmentStore  *attachments.Store
	visionAnalyzer   VisionAnalyzer
	registry         *loop.Registry
	eventBus         *events.Bus

	mu            sync.Mutex
	senderTimes   map[string][]time.Time
	lastCleanup   time.Time
	lastInboundTS map[string]lastMessage // most recent message per sender
	senderChans   map[string]chan *Envelope
	parentID      string // loop ID of the parent signal node
}

// NewBridge creates a Signal message bridge.
func NewBridge(cfg BridgeConfig) *Bridge {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Attachments.DestDir != "" {
		if err := os.MkdirAll(cfg.Attachments.DestDir, 0o750); err != nil {
			logger.Warn("signal attachment dest dir creation failed",
				"dir", cfg.Attachments.DestDir,
				"error", err,
			)
		}
	}
	handleTimeout := cfg.HandleTimeout
	if handleTimeout <= 0 {
		handleTimeout = defaultHandleTimeout
	}

	return &Bridge{
		client:           cfg.Client,
		runner:           cfg.Runner,
		logger:           logger,
		rateLimit:        cfg.RateLimit,
		handleTimeout:    handleTimeout,
		routing:          cfg.Routing,
		resolver:         cfg.Resolver,
		bindConversation: cfg.BindConversation,
		attachments:      cfg.Attachments,
		attachmentStore:  cfg.AttachmentStore,
		visionAnalyzer:   cfg.VisionAnalyzer,
		registry:         cfg.Registry,
		eventBus:         cfg.EventBus,
		senderTimes:      make(map[string][]time.Time),
		lastInboundTS:    make(map[string]lastMessage),
		senderChans:      make(map[string]chan *Envelope),
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

// Register spawns the parent signal loop and returns. Inbound messages
// are received event-driven via the signal-cli client and dispatched
// to per-sender child loops. Returns an error if the parent loop
// cannot be started.
//
// If no registry is configured, Register falls back to the legacy
// blocking Start() behavior.
func (b *Bridge) Register(ctx context.Context) error {
	if b.registry == nil {
		go b.Start(ctx)
		return nil
	}

	parentID, err := b.registry.SpawnLoop(ctx, loop.Config{
		Name: "signal",
		WaitFunc: func(wCtx context.Context) (any, error) {
			select {
			case <-wCtx.Done():
				return nil, wCtx.Err()
			case env, ok := <-b.client.Messages():
				if !ok {
					return nil, fmt.Errorf("signal message channel closed")
				}
				return env, nil
			}
		},
		Handler:  b.dispatch,
		Metadata: map[string]string{"subsystem": "signal", "category": "channel"},
	}, loop.Deps{Logger: b.logger, EventBus: b.eventBus})
	if err != nil {
		return fmt.Errorf("spawn signal parent loop: %w", err)
	}

	b.mu.Lock()
	b.parentID = parentID
	b.mu.Unlock()

	b.logger.Info("signal bridge registered with loop infrastructure",
		"parent_id", parentID,
		"handle_timeout", b.handleTimeout,
	)
	return nil
}

// Start receives messages from the signal-cli client and routes them
// through the agent loop until ctx is cancelled. This is the legacy
// blocking path; prefer Register() for loop-integrated operation.
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

			if err := b.dispatch(ctx, env); err != nil {
				b.logger.Error("signal dispatch error", "error", err)
			}
		}
	}
}

// dispatch is the parent loop handler. It filters envelopes
// (empty source, reactions, no content, rate limits) and fans out
// valid messages to per-sender child loops.
func (b *Bridge) dispatch(ctx context.Context, event any) error {
	env, ok := event.(*Envelope)
	if !ok {
		b.logger.Warn("signal dispatch received non-envelope event",
			"type", fmt.Sprintf("%T", event),
		)
		return nil
	}

	summary := loop.IterationSummary(ctx)

	if env.Source == "" {
		b.logger.Debug("signal ignoring envelope with empty source")
		if summary != nil {
			summary["action"] = "ignored_empty_source"
		}
		return nil
	}

	// Reactions are carried inside dataMessage but have no
	// text. Intercept them before the content filter.
	if env.DataMessage != nil && env.DataMessage.Reaction != nil {
		if env.DataMessage.Reaction.IsRemove {
			b.handleReaction(ctx, env)
			if summary != nil {
				summary["action"] = "reaction_removed"
				summary["sender"] = env.Source
				summary["emoji"] = env.DataMessage.Reaction.Emoji
			}
			return nil
		}
		if !b.allowSender(env.Source) {
			b.logger.Warn("signal reaction rate-limited",
				"sender", env.Source,
			)
			if summary != nil {
				summary["action"] = "rate_limited"
				summary["sender"] = env.Source
			}
			return nil
		}
		if !b.enqueueSenderEnvelope(ctx, env) {
			return nil
		}
		if summary != nil {
			summary["action"] = "reaction"
			summary["sender"] = env.Source
			summary["emoji"] = env.DataMessage.Reaction.Emoji
		}
		return nil
	}

	// For DataMessages with neither text nor attachments,
	// track the signal timestamp so the reaction tool's
	// "latest" resolution still works. Messages with content update
	// lastInboundTS during turn preparation.
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
		if summary != nil {
			summary["action"] = "ignored_no_content"
			summary["sender"] = env.Source
		}
		return nil
	}

	if !b.allowSender(env.Source) {
		b.logger.Warn("signal message rate-limited",
			"sender", env.Source,
		)
		if summary != nil {
			summary["action"] = "rate_limited"
			summary["sender"] = env.Source
		}
		return nil
	}

	// Fan out to per-sender child loop.
	if !b.enqueueSenderEnvelope(ctx, env) {
		// Don't send a read receipt for messages we failed to enqueue.
		return nil
	}

	if summary != nil {
		summary["action"] = "dispatched"
		summary["sender"] = env.Source
	}

	// Acknowledge receipt only after successful enqueue so we don't
	// ack messages that were silently dropped.
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

	return nil
}

func (b *Bridge) enqueueSenderEnvelope(ctx context.Context, env *Envelope) bool {
	if env == nil {
		return false
	}
	b.ensureSenderLoop(ctx, env.Source)

	b.mu.Lock()
	ch, ok := b.senderChans[env.Source]
	b.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case ch <- env:
		return true
	case <-ctx.Done():
		b.logger.Warn("signal context cancelled before enqueue, dropping message",
			"sender", env.Source,
			"error", ctx.Err(),
		)
		return false
	}
}

// ensureSenderLoop creates a per-sender child loop if one does not
// already exist. The child loop blocks on a per-sender channel and
// processes envelopes sequentially via TurnBuilder.
func (b *Bridge) ensureSenderLoop(ctx context.Context, sender string) {
	b.mu.Lock()
	if _, exists := b.senderChans[sender]; exists {
		b.mu.Unlock()
		return
	}
	ch := make(chan *Envelope, senderChanSize)
	b.senderChans[sender] = ch
	parentID := b.parentID
	b.mu.Unlock()

	// Resolve a display name and trust zone for the loop node.
	loopName := "signal/" + sanitizePhone(sender)
	trustZone := "unknown"
	binding := b.resolveBinding(sender)
	if binding != nil {
		if binding.ContactName != "" {
			loopName = "signal/" + sanitizeLoopName(binding.ContactName)
		}
		if binding.TrustZone != "" {
			trustZone = binding.TrustZone
		}
	}

	if b.registry == nil {
		// No registry — just run the handler inline in a goroutine.
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case env, ok := <-ch:
					if !ok {
						return
					}
					b.handleEnvelope(ctx, env, nil)
				}
			}
		}()
		return
	}

	_, err := b.registry.SpawnLoop(ctx, loop.Config{
		Name: loopName,
		WaitFunc: func(wCtx context.Context) (any, error) {
			select {
			case <-wCtx.Done():
				return nil, wCtx.Err()
			case env, ok := <-ch:
				if !ok {
					return nil, fmt.Errorf("sender channel closed")
				}
				return env, nil
			}
		},
		TurnBuilder: func(tCtx context.Context, input loop.TurnInput) (*loop.AgentTurn, error) {
			env, ok := input.Event.(*Envelope)
			if !ok {
				if len(input.NotifyEnvelopes) > 0 {
					return b.prepareLoopNotificationTurn(tCtx, sender, input.NotifyEnvelopes)
				}
				return nil, nil
			}
			return b.prepareSignalTurn(tCtx, env)
		},
		ParentID:        parentID,
		FallbackContent: prompts.InteractiveEmptyResponseFallback,
		Metadata: map[string]string{
			"subsystem":  "signal",
			"category":   "channel",
			"sender":     sender,
			"trust_zone": trustZone,
			"is_owner":   fmt.Sprintf("%t", binding != nil && binding.IsOwner),
			"contact_id": bindingValue(binding, func(b *memory.ChannelBinding) string { return b.ContactID }),
			"contact_name": bindingValue(binding, func(b *memory.ChannelBinding) string {
				return b.ContactName
			}),
			"binding_source": bindingValue(binding, func(b *memory.ChannelBinding) string {
				return b.LinkSource
			}),
		},
	}, loop.Deps{
		Runner:   signalResponseRunner{bridge: b, runner: b.runner},
		Logger:   b.logger,
		EventBus: b.eventBus,
	})
	if err != nil {
		b.logger.Error("failed to spawn sender loop",
			"sender", sender,
			"error", err,
		)
		// Fall back to inline goroutine.
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case env, ok := <-ch:
					if !ok {
						return
					}
					b.handleEnvelope(ctx, env, nil)
				}
			}
		}()
	}
}

// handleMessage processes a single inbound Signal message through the
// loop-facing request path. The progressFn, if non-nil, is used by the
// legacy no-registry path to forward in-flight events to loop telemetry.
func (b *Bridge) handleMessage(ctx context.Context, env *Envelope, progressFn func(string, map[string]any)) {
	b.handleEnvelope(ctx, env, progressFn)
}

// handleReaction processes an inbound emoji reaction. Reaction
// removals are logged but do not wake the agent. Non-removal
// reactions are forwarded to the agent loop with contextual hints.
func (b *Bridge) handleReaction(ctx context.Context, env *Envelope) {
	sender := env.Source
	reaction := signalReactionEvent(env)

	if reaction.Removed {
		b.logger.Info("signal reaction removed",
			"sender", sender,
			"emoji", reaction.Emoji,
			"target_author", reaction.TargetAuthor,
			"target_timestamp", reaction.TargetTimestamp,
		)
		return
	}

	b.handleEnvelope(ctx, env, nil)
}

func (b *Bridge) handleEnvelope(ctx context.Context, env *Envelope, progressFn func(string, map[string]any)) {
	turn, err := b.prepareSignalTurn(ctx, env)
	if err != nil {
		b.logger.Error("signal turn preparation failed", "error", err)
		return
	}
	if turn == nil {
		return
	}
	req := turn.Request
	if progressFn != nil {
		req.OnProgress = progressFn
	}
	_, _ = signalResponseRunner{bridge: b, runner: b.runner}.Run(ctx, req, nil)
}

func (b *Bridge) prepareSignalTurn(ctx context.Context, env *Envelope) (*loop.AgentTurn, error) {
	if env == nil || env.DataMessage == nil {
		return nil, nil
	}
	if env.DataMessage.Reaction != nil {
		if env.DataMessage.Reaction.IsRemove {
			b.handleReaction(ctx, env)
			return nil, nil
		}
		return b.prepareReactionTurn(ctx, env)
	}
	if env.DataMessage.Message == "" && len(env.DataMessage.Attachments) == 0 {
		return nil, nil
	}
	return b.prepareMessageTurn(ctx, env)
}

func (b *Bridge) prepareLoopNotificationTurn(ctx context.Context, sender string, envs []messages.Envelope) (*loop.AgentTurn, error) {
	summary := loop.FormatNotifyEnvelopes(envs)
	if summary == "" {
		return nil, nil
	}
	convID := fmt.Sprintf("signal-%s", sanitizePhone(sender))
	channelBinding := b.resolveBinding(sender)
	if b.bindConversation != nil && channelBinding != nil {
		if err := b.bindConversation(convID, channelBinding); err != nil {
			b.logger.Warn("failed to persist signal conversation binding", "error", err)
		}
	}

	content := "A delegated or subsystem loop requested core attention through the loop bus. Review the notification(s), then decide whether any human-facing message is appropriate now. If no immediate Signal reply should be sent, leave the final response empty.\n\n" + summary
	opts := b.requestOptions(sender, map[string]string{
		"source":      "signal",
		"sender":      sender,
		"wake_reason": "core_attention",
	})
	turn := b.agentTurn(convID, channelBinding, content, opts, map[string]any{
		"event_type":          "loop_notification",
		"sender":              sender,
		"notification_count":  len(envs),
		"core_attention_wake": true,
		"fallback_suppressed": true,
	})
	turn.Request.FallbackContent = ""

	log := b.logger.With(
		"subsystem", logging.SubsystemSignal,
		"conversation_id", convID,
		"sender", sender,
	)
	log.InfoContext(ctx, "signal loop notification received", "notifications", len(envs))
	return turn, nil
}

func (b *Bridge) prepareMessageTurn(ctx context.Context, env *Envelope) (*loop.AgentTurn, error) {
	sender := env.Source
	convID := fmt.Sprintf("signal-%s", sanitizePhone(sender))
	log := b.logger.With(
		"subsystem", logging.SubsystemSignal,
		"conversation_id", convID,
		"sender", sender,
	)

	var attachmentDescs []string
	if len(env.DataMessage.Attachments) > 0 {
		if env.DataMessage.ViewOnce {
			attachmentDescs = []string{"[View-once attachment — not available]"}
		} else {
			msgTS := env.Timestamp
			if env.DataMessage.Timestamp != 0 {
				msgTS = env.DataMessage.Timestamp
			}
			receivedAt := time.UnixMilli(msgTS)
			attachmentDescs = b.processAttachments(ctx, env.DataMessage.Attachments, sender, convID, receivedAt)
		}
	}
	content := formatMessage(env, attachmentDescs)
	channelBinding := b.resolveBinding(sender)
	if b.bindConversation != nil && channelBinding != nil {
		if err := b.bindConversation(convID, channelBinding); err != nil {
			log.Warn("failed to persist signal conversation binding", "error", err)
		}
	}

	log.Info("signal message received",
		"message_len", len(env.DataMessage.Message),
		"attachments", len(env.DataMessage.Attachments),
	)

	ts := env.Timestamp
	if env.DataMessage.Timestamp != 0 {
		ts = env.DataMessage.Timestamp
	}
	b.mu.Lock()
	b.lastInboundTS[sender] = lastMessage{
		signalTS:   ts,
		receivedAt: time.Now(),
	}
	b.mu.Unlock()

	opts := b.requestOptions(sender, map[string]string{
		"source": "signal",
		"sender": sender,
	})

	return b.agentTurn(convID, channelBinding, content, opts, map[string]any{
		"message_len": len(content),
		"sender":      sender,
		"attachments": len(env.DataMessage.Attachments),
	}), nil
}

func (b *Bridge) prepareReactionTurn(_ context.Context, env *Envelope) (*loop.AgentTurn, error) {
	sender := env.Source
	reaction := signalReactionEvent(env)
	convID := fmt.Sprintf("signal-%s", sanitizePhone(sender))

	b.logger.Info("signal reaction received",
		"sender", sender,
		"emoji", reaction.Emoji,
		"target_author", reaction.TargetAuthor,
		"target_timestamp", reaction.TargetTimestamp,
		"conversation_id", convID,
	)

	content := reaction.Prompt()
	channelBinding := b.resolveBinding(sender)
	if b.bindConversation != nil && channelBinding != nil {
		if err := b.bindConversation(convID, channelBinding); err != nil {
			b.logger.Warn("failed to persist signal conversation binding", "error", err)
		}
	}

	hints := reaction.Hints()
	hints["source"] = "signal"
	hints["sender"] = sender
	opts := b.requestOptions(sender, hints)

	return b.agentTurn(convID, channelBinding, content, opts, map[string]any{
		"event_type":            "reaction",
		"sender":                sender,
		"reaction_emoji":        reaction.Emoji,
		"target_sent_timestamp": reaction.TargetTimestamp,
	}), nil
}

func (b *Bridge) agentTurn(convID string, binding *memory.ChannelBinding, content string, opts router.RequestOptions, summary map[string]any) *loop.AgentTurn {
	fallbackContent := prompts.InteractiveEmptyResponseFallback
	return &loop.AgentTurn{
		Request: loop.Request{
			ConversationID:  convID,
			ChannelBinding:  binding,
			Messages:        []loop.Message{{Role: "user", Content: content}},
			Model:           opts.Model,
			Hints:           opts.Hints,
			ExcludeTools:    opts.ExcludeTools,
			InitialTags:     []string{"signal"},
			RuntimeTags:     []string{"message_channel"},
			FallbackContent: fallbackContent,
		},
		Summary: summary,
	}
}

type signalResponseRunner struct {
	bridge *Bridge
	runner AgentRunner
}

func (r signalResponseRunner) Run(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error) {
	if r.runner == nil {
		return nil, fmt.Errorf("signal runner is not configured")
	}
	b := r.bridge
	if b == nil {
		return r.runner.Run(ctx, req, stream)
	}

	sender := req.Hints["sender"]
	log := b.logger.With(
		"subsystem", logging.SubsystemSignal,
		"conversation_id", req.ConversationID,
		"sender", sender,
	)
	runCtx, cancel := context.WithTimeout(ctx, b.handleTimeout)
	defer cancel()
	runCtx = logging.WithLogger(runCtx, log)

	indicator := b.activityIndicator(sender)
	stopActivity := indicator.Begin(runCtx)
	resp, err := r.runner.Run(runCtx, req, stream)
	stopActivity()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	indicator.End(stopCtx)
	stopCancel()

	if resp == nil {
		resp = &loop.Response{}
	}
	if resp.RequestID != "" {
		log = log.With("request_id", resp.RequestID)
	}
	if err != nil {
		log.Error("signal agent run failed", "error", err)
		return resp, err
	}
	if strings.TrimSpace(resp.Content) == "" && req.FallbackContent != "" {
		resp.Content = req.FallbackContent
	}

	log.Info("signal agent run completed",
		"response_len", len(resp.Content),
		"model", resp.Model,
	)

	if agentAlreadySent(resp.ToolsUsed) {
		log.Info("signal reply already sent by agent tool call")
		return resp, nil
	}
	if resp.Content == "" || sender == "" {
		return resp, nil
	}

	log.Info("signal sending reply",
		"response_len", len(resp.Content),
	)
	if b.client == nil {
		log.Warn("signal reply send skipped because client is not configured")
		return resp, nil
	}
	if _, err := b.client.Send(runCtx, sender, resp.Content); err != nil {
		log.Error("signal reply send failed", "error", err)
		return resp, fmt.Errorf("send signal reply: %w", err)
	}

	log.Info("signal reply sent")
	return resp, nil
}

func (b *Bridge) activityIndicator(recipient string) messages.ActivityIndicator {
	sendTyping := func(ctx context.Context, stop bool) error {
		if b.client == nil || recipient == "" {
			return nil
		}
		return b.client.SendTyping(ctx, recipient, stop)
	}
	return messages.ActivityIndicator{
		Name:     "signal_typing",
		Interval: typingRefreshInterval,
		Start: func(ctx context.Context) error {
			return sendTyping(ctx, false)
		},
		Refresh: func(ctx context.Context) error {
			return sendTyping(ctx, false)
		},
		Stop: func(ctx context.Context) error {
			return sendTyping(ctx, true)
		},
		Logger: b.logger,
	}
}

func (b *Bridge) requestOptions(sender string, extraHints map[string]string) router.RequestOptions {
	seed := b.routing.LoopProfile()
	opts := seed.RequestOptions()
	if len(extraHints) > 0 {
		if opts.Hints == nil {
			opts.Hints = make(map[string]string, len(extraHints))
		}
		for k, v := range extraHints {
			opts.Hints[k] = v
		}
	}

	if b.resolver != nil {
		if binding := b.resolveBinding(sender); binding != nil && binding.ContactName != "" {
			if opts.Hints == nil {
				opts.Hints = make(map[string]string, 1)
			}
			opts.Hints["sender_name"] = binding.ContactName
		}
	}

	return opts
}

func (b *Bridge) resolveBinding(sender string) *memory.ChannelBinding {
	binding := (&memory.ChannelBinding{
		Channel: "signal",
		Address: sender,
	}).Normalize()
	if b == nil || b.resolver == nil {
		return binding
	}
	resolved := b.resolver.ResolveChannelBinding("signal", sender)
	if resolved == nil {
		return binding
	}
	if resolved.Channel == "" {
		resolved.Channel = "signal"
	}
	if resolved.Address == "" {
		resolved.Address = sender
	}
	return resolved.Normalize()
}

func bindingValue(binding *memory.ChannelBinding, pick func(*memory.ChannelBinding) string) string {
	if binding == nil || pick == nil {
		return ""
	}
	return pick(binding)
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

// sanitizeLoopName strips characters from a contact display name that
// could confuse the loop hierarchy (e.g. "/" which is the parent/child
// separator) or produce unreadable node labels (control characters).
func sanitizeLoopName(name string) string {
	name = strings.TrimSpace(name)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1 // drop control characters
		}
		if r == '/' {
			return '_' // avoid hierarchy separator
		}
		return r
	}, name)
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
	return signalReactionEvent(env).Prompt()
}

func signalReactionEvent(env *Envelope) messages.ReactionEvent {
	if env == nil || env.DataMessage == nil || env.DataMessage.Reaction == nil {
		return messages.ReactionEvent{ChannelName: "Signal"}
	}
	reaction := env.DataMessage.Reaction
	return messages.ReactionEvent{
		ChannelName:     "Signal",
		SenderID:        env.Source,
		SenderName:      env.SourceName,
		Emoji:           reaction.Emoji,
		TargetAuthor:    reaction.TargetAuthor,
		TargetTimestamp: reaction.TargetSentTimestamp,
		Removed:         reaction.IsRemove,
	}
}

// processAttachments stores received attachments and returns a
// human-readable description of each. When the content-addressed
// attachment store is configured, files are ingested via [Store.Ingest];
// otherwise they are copied to the legacy destination directory.
// Files that cannot be processed (missing, too large) are described
// but marked as unavailable.
func (b *Bridge) processAttachments(ctx context.Context, atts []Attachment, sender, convID string, receivedAt time.Time) []string {
	descs := make([]string, 0, len(atts))
	for _, a := range atts {
		if b.attachments.MaxSize > 0 && a.Size > b.attachments.MaxSize {
			descs = append(descs, describeAttachment(a, "exceeds size limit"))
			continue
		}

		// Content-addressed store path.
		if b.attachmentStore != nil {
			descs = append(descs, b.ingestAttachment(ctx, a, sender, convID, receivedAt))
			continue
		}

		// Legacy copy path.
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
		// Sanitize with filepath.Base to prevent path traversal.
		destName := a.ID
		if a.Filename != "" {
			destName = filepath.Base(a.Filename)
		}
		destPath := filepath.Join(b.attachments.DestDir, destName)

		// Avoid overwriting an existing file with the same name by
		// appending a timestamp suffix when a collision is detected.
		if _, err := os.Stat(destPath); err == nil {
			ext := filepath.Ext(destName)
			base := strings.TrimSuffix(destName, ext)
			suffix := time.Now().Format("20060102-150405.000000000")
			destName = fmt.Sprintf("%s-%s%s", base, suffix, ext)
			destPath = filepath.Join(b.attachments.DestDir, destName)
		}

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

// ingestAttachment stores a single attachment via the content-addressed
// store and returns a human-readable description.
func (b *Bridge) ingestAttachment(ctx context.Context, a Attachment, sender, convID string, receivedAt time.Time) string {
	if b.attachments.SourceDir == "" {
		return describeAttachment(a, "source dir not configured")
	}

	srcPath := filepath.Join(b.attachments.SourceDir, a.ID)
	f, err := os.Open(srcPath)
	if err != nil {
		b.logger.Warn("signal attachment not found",
			"id", a.ID,
			"path", srcPath,
			"error", err,
		)
		return describeAttachment(a, "file not available")
	}
	defer f.Close()

	rec, err := b.attachmentStore.Ingest(ctx, attachments.IngestParams{
		Source:         f,
		OriginalName:   a.Filename,
		ContentType:    a.ContentType,
		Size:           a.Size,
		Width:          a.Width,
		Height:         a.Height,
		Channel:        "signal",
		Sender:         sender,
		ConversationID: convID,
		ReceivedAt:     receivedAt,
	})
	if err != nil {
		b.logger.Warn("signal attachment ingest failed",
			"id", a.ID,
			"error", err,
		)
		return describeAttachment(a, "ingest failed")
	}

	absPath := b.attachmentStore.AbsPath(rec)
	b.logger.Info("signal attachment ingested",
		"id", a.ID,
		"hash", rec.Hash,
		"store_path", rec.StorePath,
		"size", rec.Size,
		"content_type", rec.ContentType,
	)

	desc := describeAttachment(a, absPath)

	// Vision analysis: analyze images on ingest and enrich the
	// description with the LLM-generated summary.
	if b.visionAnalyzer != nil && strings.HasPrefix(a.ContentType, "image/") {
		visionDesc, err := b.visionAnalyzer.Analyze(ctx, rec)
		if err != nil {
			b.logger.Warn("vision analysis failed",
				"id", a.ID,
				"hash", rec.Hash,
				"error", err,
			)
		}
		if visionDesc != "" {
			desc += "\n[Vision: " + visionDesc + "]"
		}
	}

	return desc
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
// mode 0640. On failure, the partial destination file is removed.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
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
