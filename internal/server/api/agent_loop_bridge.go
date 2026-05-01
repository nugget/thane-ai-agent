package api

import (
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

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

// cloneLoopRequest copies per-turn request data before a loop mutates
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

// loopStreamFromAgent adapts an API stream callback to the loop runner
// boundary. The application loop adapter sends [agent.StreamEvent] values
// through this channel.
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
