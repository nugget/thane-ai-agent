package loop

import (
	"context"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
)

type iterSummaryKey struct{}

// IterationSummary returns the summary map for the current iteration
// from the handler context, or nil if not available. Handler
// implementations call this to report per-iteration metrics to the
// dashboard timeline. Values should be small scalars (int, string,
// bool) to keep SSE payloads compact.
func IterationSummary(ctx context.Context) map[string]any {
	if m, ok := ctx.Value(iterSummaryKey{}).(map[string]any); ok {
		return m
	}
	return nil
}

// AgentRunSummary holds the subset of agent response fields that
// handler-only loops report to the dashboard timeline. It exists in
// the loop package (rather than accepting an agent.Response directly)
// to avoid an import cycle — agent imports loop, not the other way
// around.
type AgentRunSummary struct {
	RequestID          string
	Model              string
	InputTokens        int
	OutputTokens       int
	ActiveTags         []string
	EffectiveTools     []string
	LoadedCapabilities []toolcatalog.LoadedCapabilityEntry
}

// ReportAgentRun writes standard agent-run metrics into the current
// iteration's summary map. It is the canonical way for handler-only
// loops that call runner.Run() to surface request_id, model, and
// token counts on the dashboard.
//
// Callers may add additional custom fields to the summary map after
// this call (e.g., sender, message_len).
//
// Returns the summary map for chaining, or nil if the context has no
// iteration summary (e.g., called outside a loop handler).
func ReportAgentRun(ctx context.Context, s AgentRunSummary) map[string]any {
	summary := IterationSummary(ctx)
	if summary == nil {
		return nil
	}
	summary["request_id"] = s.RequestID
	summary["model"] = s.Model
	summary["input_tokens"] = s.InputTokens
	summary["output_tokens"] = s.OutputTokens
	if len(s.ActiveTags) > 0 {
		summary["active_tags"] = append([]string(nil), s.ActiveTags...)
	}
	if len(s.EffectiveTools) > 0 {
		summary["effective_tools"] = append([]string(nil), s.EffectiveTools...)
	}
	if len(s.LoadedCapabilities) > 0 {
		summary["loaded_capabilities"] = append([]toolcatalog.LoadedCapabilityEntry(nil), s.LoadedCapabilities...)
	}
	return summary
}

// ReportConversationID overrides the loop-visible conversation ID for the
// current handler iteration. Handler-only loops normally generate an internal
// conversation ID before dispatch; handlers that proxy a nested agent.Run can
// call this so the dashboard timeline and log lookups follow the real child
// conversation instead.
func ReportConversationID(ctx context.Context, conversationID string) map[string]any {
	summary := IterationSummary(ctx)
	if summary == nil || conversationID == "" {
		return summary
	}
	summary["conversation_id"] = conversationID
	return summary
}

// ReportObservation records the handler-generated observation text for the
// current iteration so built-in loop output targets can fan it out to
// journals, MQTT, or the structured observation log without each handler
// inventing its own dispatch path.
func ReportObservation(ctx context.Context, content string) map[string]any {
	summary := IterationSummary(ctx)
	if summary == nil || strings.TrimSpace(content) == "" {
		return summary
	}
	summary["observation"] = content
	return summary
}
