package tools

import (
	"context"
	"fmt"
	"strings"

	routepkg "github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

func (r *Registry) handleModelRouteExplain(_ context.Context, args map[string]any) (string, error) {
	if r.modelRegistry == nil || r.modelRouter == nil {
		return "", fmt.Errorf("model registry router not configured")
	}

	toolCount, ok := toolargs.IntOK(args, "tool_count")
	if !ok || toolCount < 0 {
		toolCount = len(r.tools)
	}
	priority := mrParseRoutePriority(toolargs.String(args, "priority"))
	hints := mrExtractRouteHints(args)

	req := routeRequestForExplanation(args, toolCount, priority, hints)
	decision := r.modelRouter.ExplainRequest(req)
	if decision == nil {
		return "", fmt.Errorf("failed to explain route")
	}

	return mrMarshalToolJSON(map[string]any{
		"request": map[string]any{
			"query":           req.Query,
			"context_size":    req.ContextSize,
			"needs_tools":     req.NeedsTools,
			"needs_streaming": req.NeedsStreaming,
			"needs_images":    req.NeedsImages,
			"tool_count":      req.ToolCount,
			"priority":        mrPriorityString(req.Priority),
			"hints":           req.RoutingFactors,
		},
		"default_model": r.modelRegistry.Snapshot().DefaultModel,
		"decision":      decision,
	})
}

func routeRequestForExplanation(args map[string]any, toolCount int, priority routepkg.Priority, hints map[string]string) routepkg.Request {
	return routepkg.Request{
		Query:          strings.TrimSpace(toolargs.String(args, "query")),
		ContextSize:    toolargs.IntOr(args, "context_size", 0),
		NeedsTools:     toolargs.Bool(args, "needs_tools"),
		NeedsStreaming: toolargs.Bool(args, "needs_streaming"),
		NeedsImages:    toolargs.Bool(args, "needs_images"),
		ToolCount:      toolCount,
		Priority:       priority,
		RoutingFactors: hints,
	}
}
