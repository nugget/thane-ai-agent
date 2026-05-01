package api

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// owuWork is a single synchronous HTTP request dispatched to a
// per-conversation child loop.
type owuWork struct {
	// reqCtx is the HTTP request context for cancellation.
	reqCtx context.Context
	// req is the loop-facing request captured at ingress.
	req loop.Request
	// callback is the caller-facing stream sink.
	callback loop.StreamCallback
	// respCh is buffered so the loop can finish if the handler returns.
	respCh chan owuResult
}

// owuResult carries the agent response (or error) back to the HTTP handler.
type owuResult struct {
	resp *loop.Response
	err  error
}

// OWUTracker tracks Open WebUI conversations as loop nodes while keeping
// the HTTP handler's synchronous request/reply contract. It owns the
// queueing and type adaptation around OWU sessions; model execution stays
// on the common loop runner path through [loop.AgentTurn].
type OWUTracker struct {
	ctx              context.Context // long-lived context for spawning child loops
	registry         *loop.Registry
	eventBus         *events.Bus
	runner           loop.Runner
	logger           *slog.Logger
	bindConversation func(conversationID string, binding *memory.ChannelBinding) error

	mu       sync.Mutex
	parentID string
	convChs  map[string]chan owuWork // conversation ID → work channel
}

// NewOWUTracker creates an Open WebUI tracker and registers the parent
// "owu" loop when a registry is available. Pass a nil registry to disable
// loop visibility while still routing calls through the configured runner.
// The runner is the loop-facing runner used by child conversation loops.
func NewOWUTracker(ctx context.Context, registry *loop.Registry, eventBus *events.Bus, runner loop.Runner, logger *slog.Logger) (*OWUTracker, error) {
	t := &OWUTracker{
		ctx:      ctx,
		registry: registry,
		eventBus: eventBus,
		runner:   runner,
		logger:   logger,
		convChs:  make(map[string]chan owuWork),
	}

	if registry == nil {
		return t, nil
	}

	parentID, err := registry.SpawnLoop(ctx, loop.Config{
		Name:     "owu",
		Handler:  func(context.Context, any) error { return nil },
		Metadata: map[string]string{"subsystem": "owu", "category": "channel"},
	}, loop.Deps{Logger: logger, EventBus: eventBus})
	if err != nil {
		return nil, fmt.Errorf("spawn owu parent loop: %w", err)
	}
	t.parentID = parentID
	logger.Info("owu tracker registered with loop infrastructure", "parent_id", parentID)
	return t, nil
}

// UseConversationBindingWriter configures durable conversation binding
// persistence for OWU-backed conversations.
func (t *OWUTracker) UseConversationBindingWriter(bind func(string, *memory.ChannelBinding) error) {
	if t == nil {
		return
	}
	t.bindConversation = bind
}

// Dispatch routes an agent request through the per-conversation child loop
// and waits for the loop-owned model result. Auxiliary OWU requests bypass
// conversation tracking but still use the configured loop runner. The
// supplied streamCallback is converted to [loop.StreamCallback] for
// caller-facing HTTP streaming; loop telemetry is emitted independently by
// the loop runtime. displayName is a human-friendly label for the
// conversation node, such as a truncation of the first message.
func (t *OWUTracker) Dispatch(ctx context.Context, req *agent.Request, streamCallback agent.StreamCallback, displayName string) (*agent.Response, error) {
	loopReq := loopRequestFromAgent(req)
	convID := loopReq.ConversationID
	if convID == "" || convID == "owu-auxiliary" {
		// Auxiliary requests bypass loop tracking.
		resp, err := t.run(ctx, loopReq, loopStreamFromAgent(streamCallback))
		return agentResponseFromLoop(resp), err
	}
	if loopReq.ChannelBinding == nil {
		loopReq.ChannelBinding = (&memory.ChannelBinding{Channel: "owu", IsOwner: true}).Normalize()
	} else {
		loopReq.ChannelBinding = loopReq.ChannelBinding.Normalize()
		if loopReq.ChannelBinding != nil && loopReq.ChannelBinding.Channel == "owu" {
			loopReq.ChannelBinding.IsOwner = true
		}
	}
	if req != nil {
		req.ChannelBinding = loopReq.ChannelBinding
	}
	if t.bindConversation != nil && loopReq.ChannelBinding != nil {
		if err := t.bindConversation(convID, loopReq.ChannelBinding); err != nil {
			t.logger.Warn("failed to persist owu conversation binding",
				"conversation_id", convID,
				"error", err,
			)
		}
	}
	if t.registry == nil {
		resp, err := t.run(ctx, loopReq, loopStreamFromAgent(streamCallback))
		return agentResponseFromLoop(resp), err
	}

	ch := t.ensureConvLoop(ctx, convID, displayName, loopReq.ChannelBinding != nil && loopReq.ChannelBinding.IsOwner)

	work := owuWork{
		reqCtx:   ctx,
		req:      loopReq,
		callback: loopStreamFromAgent(streamCallback),
		respCh:   make(chan owuResult, 1),
	}

	select {
	case ch <- work:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case res := <-work.respCh:
		return agentResponseFromLoop(res.resp), res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const convChanSize = 4 // allow a small queue of concurrent requests

// ensureConvLoop lazily spawns a per-conversation child loop.
// Uses the tracker's long-lived context (not the HTTP request context)
// so the loop survives beyond the first request.
func (t *OWUTracker) ensureConvLoop(_ context.Context, convID, displayName string, isOwner bool) chan owuWork {
	t.mu.Lock()
	if ch, ok := t.convChs[convID]; ok {
		t.mu.Unlock()
		return ch
	}
	ch := make(chan owuWork, convChanSize)
	t.convChs[convID] = ch
	parentID := t.parentID
	t.mu.Unlock()

	loopName := "owu/" + sanitizeOWULoopName(displayName)

	loopID, err := t.registry.SpawnLoop(t.ctx, loop.Config{
		Name: loopName,
		Tags: []string{"owu"},
		WaitFunc: func(wCtx context.Context) (any, error) {
			select {
			case <-wCtx.Done():
				return nil, wCtx.Err()
			case w, ok := <-ch:
				if !ok {
					return nil, fmt.Errorf("conversation channel closed")
				}
				return w, nil
			}
		},
		TurnBuilder: func(_ context.Context, input loop.TurnInput) (*loop.AgentTurn, error) {
			w, ok := input.Event.(owuWork)
			if !ok {
				return nil, nil
			}
			req := cloneLoopRequest(w.req)
			if req.FallbackContent == "" {
				req.FallbackContent = prompts.InteractiveEmptyResponseFallback
			}
			return &loop.AgentTurn{
				Request:    req,
				RunContext: w.reqCtx,
				Stream:     w.callback,
				ResultSink: func(resp *loop.Response, err error) {
					w.respCh <- owuResult{resp: resp, err: err}
				},
			}, nil
		},
		ParentID:        parentID,
		FallbackContent: prompts.InteractiveEmptyResponseFallback,
		Metadata: map[string]string{
			"subsystem":       "owu",
			"category":        "channel",
			"conversation_id": convID,
			"is_owner":        strconv.FormatBool(isOwner),
		},
	}, loop.Deps{Runner: t.runner, Logger: t.logger, EventBus: t.eventBus})
	if err != nil {
		t.logger.Error("failed to spawn owu conversation loop",
			"conversation_id", convID,
			"error", err,
		)
		// Fall back: requests will still go through the channel but
		// without loop visibility. Remove the mapping so we retry next time.
		t.mu.Lock()
		delete(t.convChs, convID)
		t.mu.Unlock()

		// Return the channel anyway so the current request isn't lost,
		// and start an inline goroutine to drain it.
		go func() {
			for {
				select {
				case <-t.ctx.Done():
					return
				case w, ok := <-ch:
					if !ok {
						return
					}
					runCtx, cancel := mergeOWUContexts(t.ctx, w.reqCtx)
					resp, err := t.run(runCtx, w.req, w.callback)
					cancel()
					w.respCh <- owuResult{resp: resp, err: err}
				}
			}
		}()
		return ch
	}

	// Clean up the channel mapping when the child loop exits so that
	// the next request for this conversation respawns a fresh loop
	// instead of sending into an orphaned channel.
	l := t.registry.Get(loopID)
	if l != nil {
		go func(convID string, ch chan owuWork) {
			<-l.Done()
			t.mu.Lock()
			if t.convChs[convID] == ch {
				delete(t.convChs, convID)
			}
			t.mu.Unlock()
		}(convID, ch)
	}

	return ch
}

// run is the direct runner path used for auxiliary requests and fallback
// paths that intentionally do not create a child conversation loop.
func (t *OWUTracker) run(ctx context.Context, req loop.Request, stream loop.StreamCallback) (*loop.Response, error) {
	if t.runner == nil {
		return nil, fmt.Errorf("owu runner is not configured")
	}
	return t.runner.Run(ctx, req, stream)
}

// loopRequestFromAgent converts the API package's legacy agent.Request
// boundary into loop.Request while deep-copying mutable request data.
func loopRequestFromAgent(req *agent.Request) loop.Request {
	if req == nil {
		return loop.Request{}
	}
	msgs := make([]loop.Message, len(req.Messages))
	for i, msg := range req.Messages {
		msgs[i] = loop.Message{
			Role:    msg.Role,
			Content: msg.Content,
			Images:  append(msg.Images[:0:0], msg.Images...),
		}
	}
	runtimeTools := make([]loop.RuntimeTool, 0, len(req.RuntimeTools))
	for _, tool := range req.RuntimeTools {
		if tool == nil {
			continue
		}
		runtimeTools = append(runtimeTools, loop.RuntimeTool{
			Name:                 tool.Name,
			Description:          tool.Description,
			Parameters:           cloneAnyMap(tool.Parameters),
			Handler:              tool.Handler,
			SkipContentResolve:   tool.SkipContentResolve,
			ContentResolveExempt: append([]string(nil), tool.ContentResolveExempt...),
		})
	}
	return loop.Request{
		Model:                 req.Model,
		ConversationID:        req.ConversationID,
		ChannelBinding:        req.ChannelBinding.Clone(),
		Messages:              msgs,
		SkipContext:           req.SkipContext,
		AllowedTools:          append([]string(nil), req.AllowedTools...),
		ExcludeTools:          append([]string(nil), req.ExcludeTools...),
		SkipTagFilter:         req.SkipTagFilter,
		Hints:                 cloneStringMap(req.Hints),
		InitialTags:           append([]string(nil), req.InitialTags...),
		RuntimeTags:           append([]string(nil), req.RuntimeTags...),
		RuntimeTools:          runtimeTools,
		MaxIterations:         req.MaxIterations,
		MaxOutputTokens:       req.MaxOutputTokens,
		ToolTimeout:           req.ToolTimeout,
		UsageRole:             req.UsageRole,
		UsageTaskName:         req.UsageTaskName,
		SystemPrompt:          req.SystemPrompt,
		FallbackContent:       req.FallbackContent,
		PromptMode:            req.PromptMode,
		SuppressAlwaysContext: req.SuppressAlwaysContext,
	}
}

// cloneLoopRequest copies per-turn request data before a child loop mutates
// runtime defaults, fallback content, progress callbacks, or tag state.
func cloneLoopRequest(req loop.Request) loop.Request {
	cloned := req
	cloned.ChannelBinding = req.ChannelBinding.Clone()
	cloned.Messages = append([]loop.Message(nil), req.Messages...)
	for i := range cloned.Messages {
		cloned.Messages[i].Images = append(cloned.Messages[i].Images[:0:0], cloned.Messages[i].Images...)
	}
	cloned.AllowedTools = append([]string(nil), req.AllowedTools...)
	cloned.ExcludeTools = append([]string(nil), req.ExcludeTools...)
	cloned.Hints = cloneStringMap(req.Hints)
	cloned.InitialTags = append([]string(nil), req.InitialTags...)
	cloned.RuntimeTags = append([]string(nil), req.RuntimeTags...)
	cloned.RuntimeTools = append([]loop.RuntimeTool(nil), req.RuntimeTools...)
	for i := range cloned.RuntimeTools {
		cloned.RuntimeTools[i].Parameters = cloneAnyMap(req.RuntimeTools[i].Parameters)
		cloned.RuntimeTools[i].ContentResolveExempt = append([]string(nil), req.RuntimeTools[i].ContentResolveExempt...)
	}
	return cloned
}

// agentResponseFromLoop adapts loop.Response back to the API handler's
// legacy agent.Response type.
func agentResponseFromLoop(resp *loop.Response) *agent.Response {
	if resp == nil {
		return nil
	}
	return &agent.Response{
		Content:                  resp.Content,
		Model:                    resp.Model,
		FinishReason:             resp.FinishReason,
		InputTokens:              resp.InputTokens,
		OutputTokens:             resp.OutputTokens,
		CacheCreationInputTokens: resp.CacheCreationInputTokens,
		CacheReadInputTokens:     resp.CacheReadInputTokens,
		ContextWindow:            resp.ContextWindow,
		ToolsUsed:                cloneToolCounts(resp.ToolsUsed),
		EffectiveTools:           append([]string(nil), resp.EffectiveTools...),
		LoadedCapabilities:       append([]toolcatalog.LoadedCapabilityEntry(nil), resp.LoadedCapabilities...),
		Iterations:               resp.Iterations,
		Exhausted:                resp.Exhausted,
		RequestID:                resp.RequestID,
		ActiveTags:               append([]string(nil), resp.ActiveTags...),
	}
}

// loopStreamFromAgent adapts the HTTP handler's stream callback to the
// loop runner boundary. The application loop adapter sends [agent.StreamEvent]
// values through this channel.
func loopStreamFromAgent(cb agent.StreamCallback) loop.StreamCallback {
	if cb == nil {
		return nil
	}
	return func(event any) {
		streamEvent, ok := event.(agent.StreamEvent)
		if !ok {
			return
		}
		cb(streamEvent)
	}
}

// mergeOWUContexts combines the long-lived tracker context with an HTTP
// request context for direct runner fallback paths.
func mergeOWUContexts(base context.Context, extra context.Context) (context.Context, context.CancelFunc) {
	if extra == nil {
		return base, func() {}
	}
	ctx, cancel := context.WithCancel(base)
	if extra.Err() != nil {
		cancel()
		return ctx, cancel
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-extra.Done():
			cancel()
		}
	}()
	return ctx, cancel
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneToolCounts(src map[string]int) map[string]int {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// sanitizeOWULoopName strips characters from a display name that could
// confuse the loop hierarchy ("/") or produce unreadable node labels.
func sanitizeOWULoopName(name string) string {
	name = strings.TrimSpace(name)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1 // drop control characters
		}
		if r == '/' {
			return '_'
		}
		return r
	}, name)
}
