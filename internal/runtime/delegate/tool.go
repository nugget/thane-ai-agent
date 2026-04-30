package delegate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// NowToolDescription is the LLM-facing description for thane_now, the
// sync member of the thane_* family.
const NowToolDescription = "Synchronously delegate a bounded task to a sub-agent and return the result inline. " +
	"Use when the calling model needs the answer in this turn — investigation, research, summarization, controlled tool execution. " +
	"Uses compact task context by default; set context_mode=full only for continuity-sensitive work. " +
	"Blocks until the delegate completes or exhausts its budget. " +
	"For fire-and-forget background work, use thane_assign instead. " +
	"For recurring document-anchored work on a schedule, use thane_curate."

// AssignToolDescription is the LLM-facing description for thane_assign,
// the async one-shot member of the thane_* family.
const AssignToolDescription = "Assign a bounded task to a sub-agent that runs in the background and reports its result back through the current conversation or interactive channel when complete. " +
	"Use when the work will take long enough that the calling model should not block waiting — multi-step investigation, deferred report generation, anything where the caller wants to move on while the delegate completes. " +
	"Uses compact task context by default; set context_mode=full only for continuity-sensitive work. " +
	"For an answer needed in this turn, use thane_now. " +
	"For recurring scheduled work, use thane_curate."

// NowToolDefinition returns the JSON schema for thane_now.
func NowToolDefinition() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": commonDelegateProperties(),
		"required":   []string{"task"},
	}
}

// AssignToolDefinition returns the JSON schema for thane_assign. The
// schema is identical to thane_now today; future revisions will add an
// output target parameter so async work can land in a document or
// directory tree instead of the current conversation/channel.
func AssignToolDefinition() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": commonDelegateProperties(),
		"required":   []string{"task"},
	}
}

// commonDelegateProperties returns the JSON schema property block
// shared by thane_now and thane_assign.
func commonDelegateProperties() map[string]any {
	return map[string]any{
		"task": map[string]any{
			"type":        "string",
			"description": "Plain English description of what to accomplish.",
		},
		"guidance": map[string]any{
			"type":        "string",
			"description": "Optional hints to steer execution (entity names, what to focus on, output format preferences).",
		},
		"tags": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
			"description": "Optional capability tags to scope the delegate's tools. " +
				"When provided, the delegate only sees tools from these tags " +
				"(plus inherited elective caller tags and always-active tags). " +
				"Use root entry-point tags when the delegate should choose a narrower branch; use leaf tags when you already know the needed toolset. " +
				"Omit to inherit the caller's elective task context.",
		},
		"inherit_caller_tags": map[string]any{
			"type":        "boolean",
			"default":     true,
			"description": "Whether to inherit elective capability tags from the caller. Runtime and channel affordance tags such as message_channel are never inherited.",
		},
		"context_mode": map[string]any{
			"type":        "string",
			"enum":        []string{"task", "full"},
			"default":     "task",
			"description": "Prompt/context shape for the delegate. task is compact and omits full identity files, inject files, always-on talents, and conversation-history dressing. full opts into the normal Thane prompt when the delegate truly needs that continuity.",
		},
	}
}

// NowToolHandler returns the handler for thane_now.
func NowToolHandler(exec *Executor) func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		req, errMsg := parseDelegateArgs(args)
		if errMsg != "" {
			return errMsg, nil
		}
		return runNow(ctx, exec, req), nil
	}
}

// AssignToolHandler returns the handler for thane_assign.
func AssignToolHandler(exec *Executor) func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		req, errMsg := parseDelegateArgs(args)
		if errMsg != "" {
			return errMsg, nil
		}
		return runAssign(ctx, exec, req), nil
	}
}

// delegateRequest captures the parsed arguments common to all
// thane_* delegation tools.
type delegateRequest struct {
	task              string
	profileName       string
	guidance          string
	inheritCallerTags bool
	tags              []string
	tagsProvided      bool
	contextMode       agentctx.PromptMode
}

// parseDelegateArgs extracts the shared args for the family.
func parseDelegateArgs(args map[string]any) (delegateRequest, string) {
	req := delegateRequest{inheritCallerTags: true, profileName: "general", contextMode: agentctx.PromptModeTask}

	task, _ := args["task"].(string)
	if task == "" {
		return req, "Error: task is required"
	}
	req.task = task

	req.guidance, _ = args["guidance"].(string)
	if rawInherit, ok := args["inherit_caller_tags"].(bool); ok {
		req.inheritCallerTags = rawInherit
	}
	if rawMode, ok := args["context_mode"].(string); ok && rawMode != "" {
		mode, err := agentctx.ParsePromptMode(rawMode)
		if err != nil {
			return req, "Error: context_mode must be one of [task, full]"
		}
		req.contextMode = mode
	}
	if rawTags, ok := args["tags"].([]any); ok {
		req.tagsProvided = true
		req.tags = make([]string, 0, len(rawTags))
		for _, rt := range rawTags {
			if s, ok := rt.(string); ok {
				req.tags = append(req.tags, s)
			}
		}
	}
	return req, ""
}

// runAssign executes the async path for thane_assign.
func runAssign(ctx context.Context, exec *Executor, req delegateRequest) string {
	opts := executionOptions{
		inheritCallerTags: req.inheritCallerTags,
		explicitTagScope:  req.tagsProvided,
		promptMode:        req.contextMode,
	}
	loopID, profileName, err := exec.startBackground(ctx, req.task, req.profileName, req.guidance, req.tags, opts)
	if profileName == "" {
		profileName = req.profileName
	}
	if err != nil {
		return fmt.Sprintf("[Delegate error: profile=%s, mode=async] %s", profileName, err.Error())
	}
	return fmt.Sprintf("[Delegate STARTED: profile=%s, mode=async, loop_id=%s]\n\nBackground delegate launched. Its result will be delivered back through the current conversation or interactive channel when it completes.", profileName, loopID)
}

// runNow executes the sync path for thane_now. Returns a fully
// formatted tool-result string with success/exhaustion headers and
// execution summary.
func runNow(ctx context.Context, exec *Executor, req delegateRequest) string {
	opts := executionOptions{
		inheritCallerTags: req.inheritCallerTags,
		explicitTagScope:  req.tagsProvided,
		promptMode:        req.contextMode,
	}
	result, err := exec.execute(ctx, req.task, req.profileName, req.guidance, req.tags, opts)
	if err != nil {
		return fmt.Sprintf("[Delegate error: profile=%s] %s", req.profileName, err.Error())
	}
	profileName := req.profileName
	if result.ProfileName != "" {
		profileName = result.ProfileName
	}
	summary := formatExecSummary(result)

	// Format the result with explicit success/failure headers so the
	// calling model can distinguish outcomes unambiguously.
	if !result.Exhausted {
		if result.Content == "" {
			// Safety net — should be rare after delegate.go now flags
			// empty-after-tool-calls as ExhaustNoOutput.
			return fmt.Sprintf("[Delegate FAILED: profile=%s, model=%s, reason=no_output, iter=%d]"+
				"\n\nDelegate completed without producing results.\n\n%s",
				profileName, result.Model, result.Iterations, summary)
		}
		header := fmt.Sprintf("[Delegate SUCCEEDED: profile=%s, model=%s, iter=%d, tokens=%s]",
			profileName, result.Model, result.Iterations, formatTokens(result.OutputTokens))
		return header + "\n\n" + result.Content + "\n\n" + summary
	}

	// Exhausted delegation — provide actionable context for retry.
	header := fmt.Sprintf("[Delegate FAILED: profile=%s, model=%s, reason=%s, iter=%d, tokens_in=%s, tokens_out=%s]",
		profileName, result.Model, result.ExhaustReason, result.Iterations,
		formatTokens(result.InputTokens), formatTokens(result.OutputTokens))

	var out strings.Builder
	out.WriteString(header)
	out.WriteString("\n\n")
	if result.Content != "" {
		out.WriteString(result.Content)
		out.WriteString("\n\n")
	}
	out.WriteString("[Exhaustion note: ")
	switch result.ExhaustReason {
	case ExhaustNoOutput:
		out.WriteString("The delegate completed all tool calls but produced no text output. Retry with more specific guidance — tell the delegate exactly what information to return.")
	case ExhaustWallClock:
		out.WriteString("The delegate exceeded its wall clock time limit before completing the task.")
	case ExhaustTokenBudget:
		out.WriteString("The delegate exceeded its output token budget before completing the task.")
	case ExhaustIllegalTool:
		out.WriteString("The delegate attempted to call a tool it does not have access to and was stopped.")
	default:
		out.WriteString("The delegate used all available iterations before completing the task.")
	}
	if result.ExhaustReason != ExhaustNoOutput {
		out.WriteString(" If retrying, provide more specific guidance to narrow the scope — ")
		out.WriteString("e.g., exact file paths, entity IDs, or which step to focus on.")
	}
	out.WriteString("]\n\n")
	out.WriteString(summary)
	return out.String()
}

// formatExecSummary produces a structured execution summary block from a
// delegate [Result]. The format is designed for the orchestrator model to
// learn which tools were called and whether they succeeded.
func formatExecSummary(r *Result) string {
	var sb strings.Builder
	sb.WriteString("--- execution summary ---\n")
	sb.WriteString(fmt.Sprintf("iterations: %d\n", r.Iterations))
	sb.WriteString(fmt.Sprintf("duration: %s\n", formatDuration(r.Duration)))

	if len(r.ToolCalls) == 0 {
		sb.WriteString("tool_calls: (none)\n")
		sb.WriteString("errors: 0\n")
	} else {
		var errs int
		parts := make([]string, len(r.ToolCalls))
		for i, tc := range r.ToolCalls {
			tag := "ok"
			if !tc.Success {
				tag = "err"
				errs++
			}
			parts[i] = fmt.Sprintf("%s(%s)", tc.Name, tag)
		}
		sb.WriteString(fmt.Sprintf("tool_calls: %s\n", strings.Join(parts, " → ")))
		sb.WriteString(fmt.Sprintf("errors: %d\n", errs))
	}

	return sb.String()
}

// formatDuration renders a duration as a compact human-readable string
// (e.g. "8.2s", "1m12s").
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}
