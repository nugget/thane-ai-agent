// Package api implements the OpenAI-compatible HTTP API.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/loop"
)

// owuWork is a single request dispatched to a per-conversation child loop.
type owuWork struct {
	req      *agent.Request
	callback agent.StreamCallback
	respCh   chan owuResult
}

// owuResult carries the agent response (or error) back to the HTTP handler.
type owuResult struct {
	resp *agent.Response
	err  error
}

// OWUTracker registers a parent "owu" loop and lazily spawns
// per-conversation child loops so that Open WebUI sessions appear on
// the dashboard with full in-flight event visibility.
type OWUTracker struct {
	ctx      context.Context // long-lived context for spawning child loops
	registry *loop.Registry
	eventBus *events.Bus
	runner   *agent.Loop
	logger   *slog.Logger

	mu       sync.Mutex
	parentID string
	convChs  map[string]chan owuWork // conversation ID → work channel
}

// NewOWUTracker creates a tracker and spawns the parent "owu" loop.
// Pass a nil registry to disable loop integration (calls fall through
// to the agent loop directly).
func NewOWUTracker(ctx context.Context, registry *loop.Registry, eventBus *events.Bus, runner *agent.Loop, logger *slog.Logger) (*OWUTracker, error) {
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

// Dispatch routes an agent request through the per-conversation child loop.
// The supplied streamCallback receives tokens/tool events for HTTP streaming;
// the loop infrastructure receives the same events for dashboard visibility.
// displayName is a human-friendly label for the conversation node
// (e.g., a truncation of the first message).
func (t *OWUTracker) Dispatch(ctx context.Context, req *agent.Request, streamCallback agent.StreamCallback, displayName string) (*agent.Response, error) {
	if t.registry == nil {
		return t.runner.Run(ctx, req, streamCallback)
	}

	convID := req.ConversationID
	if convID == "" || convID == "owu-auxiliary" {
		// Auxiliary requests bypass loop tracking.
		return t.runner.Run(ctx, req, streamCallback)
	}

	ch := t.ensureConvLoop(ctx, convID, displayName)

	work := owuWork{
		req:      req,
		callback: streamCallback,
		respCh:   make(chan owuResult, 1),
	}

	select {
	case ch <- work:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case res := <-work.respCh:
		return res.resp, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const convChanSize = 4 // allow a small queue of concurrent requests

// ensureConvLoop lazily spawns a per-conversation child loop.
// Uses the tracker's long-lived context (not the HTTP request context)
// so the loop survives beyond the first request.
func (t *OWUTracker) ensureConvLoop(_ context.Context, convID, displayName string) chan owuWork {
	t.mu.Lock()
	if ch, ok := t.convChs[convID]; ok {
		t.mu.Unlock()
		return ch
	}
	ch := make(chan owuWork, convChanSize)
	t.convChs[convID] = ch
	parentID := t.parentID
	t.mu.Unlock()

	loopName := "owu/" + displayName

	_, err := t.registry.SpawnLoop(t.ctx, loop.Config{
		Name: loopName,
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
		Handler: func(hCtx context.Context, event any) error {
			w, ok := event.(owuWork)
			if !ok {
				return nil
			}
			progressFn := loop.ProgressFunc(hCtx)
			// Fan-out: forward agent stream events to both the HTTP
			// streaming callback and the loop progress func.
			combined := fanOutStream(w.callback, buildAgentStream(progressFn))
			resp, err := t.runner.Run(hCtx, w.req, combined)
			w.respCh <- owuResult{resp: resp, err: err}
			return err
		},
		ParentID: parentID,
		Metadata: map[string]string{
			"subsystem":       "owu",
			"category":        "channel",
			"conversation_id": convID,
		},
	}, loop.Deps{Logger: t.logger, EventBus: t.eventBus})
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
					resp, err := t.runner.Run(t.ctx, w.req, w.callback)
					w.respCh <- owuResult{resp: resp, err: err}
				}
			}
		}()
	}

	return ch
}

// fanOutStream creates a StreamCallback that forwards events to both a and b.
// Either may be nil.
func fanOutStream(a, b agent.StreamCallback) agent.StreamCallback {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func(e agent.StreamEvent) {
		a(e)
		b(e)
	}
}

const maxToolResultLen = 200

// buildAgentStream converts a loop progress func into an agent
// StreamCallback that forwards in-flight LLM and tool events to the
// event bus for dashboard visibility. Returns nil if progressFn is nil.
func buildAgentStream(progressFn func(string, map[string]any)) agent.StreamCallback {
	if progressFn == nil {
		return nil
	}
	return func(e agent.StreamEvent) {
		switch e.Kind {
		case agent.KindLLMStart:
			if e.Response != nil {
				data := map[string]any{
					"model": e.Response.Model,
				}
				for k, v := range e.Data {
					data[k] = v
				}
				progressFn(events.KindLoopLLMStart, data)
			}
		case agent.KindToolCallStart:
			if e.ToolCall != nil {
				data := map[string]any{
					"tool": e.ToolCall.Function.Name,
				}
				if len(e.ToolCall.Function.Arguments) > 0 {
					data["args"] = e.ToolCall.Function.Arguments
				}
				progressFn(events.KindLoopToolStart, data)
			}
		case agent.KindToolCallDone:
			data := map[string]any{"tool": e.ToolName}
			if e.ToolError != "" {
				data["error"] = e.ToolError
			}
			if e.ToolResult != "" {
				r := e.ToolResult
				if len(r) > maxToolResultLen {
					r = r[:maxToolResultLen] + "…"
				}
				data["result"] = r
			}
			progressFn(events.KindLoopToolDone, data)
		case agent.KindLLMResponse:
			if e.Response != nil {
				progressFn(events.KindLoopLLMResponse, map[string]any{
					"model":         e.Response.Model,
					"input_tokens":  e.Response.InputTokens,
					"output_tokens": e.Response.OutputTokens,
				})
			}
		}
	}
}
