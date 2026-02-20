package delegate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/prompts"
)

// ToolDefinition returns the JSON schema parameters for the thane_delegate tool.
func ToolDefinition() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "Plain English description of what to accomplish",
			},
			"profile": map[string]any{
				"type":        "string",
				"enum":        []string{"general", "ha"},
				"default":     "general",
				"description": "Delegation profile — controls which tools and context the delegate receives",
			},
			"guidance": map[string]any{
				"type":        "string",
				"description": "Optional hints to steer execution (entity names, what to focus on, output format preferences)",
			},
		},
		"required": []string{"task"},
	}
}

// ToolDescription is the LLM-facing description for the thane_delegate tool.
var ToolDescription = prompts.DelegateToolDescription

// ToolHandler returns a tool handler function bound to the given executor.
// Errors from the delegate are returned as tool result strings (not Go errors)
// so the calling model can decide what to do.
func ToolHandler(exec *Executor) func(ctx context.Context, args map[string]any) (string, error) {
	return func(ctx context.Context, args map[string]any) (string, error) {
		task, _ := args["task"].(string)
		if task == "" {
			return "Error: task is required", nil
		}

		profileName, _ := args["profile"].(string)
		if profileName == "" {
			profileName = "general"
		}

		guidance, _ := args["guidance"].(string)

		result, err := exec.Execute(ctx, task, profileName, guidance)
		if err != nil {
			return fmt.Sprintf("[Delegate error: profile=%s] %s", profileName, err.Error()), nil
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
					profileName, result.Model, result.Iterations, summary), nil
			}
			header := fmt.Sprintf("[Delegate SUCCEEDED: profile=%s, model=%s, iter=%d, tokens=%s]",
				profileName, result.Model, result.Iterations, formatTokens(result.OutputTokens))
			return header + "\n\n" + result.Content + "\n\n" + summary, nil
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
		default:
			out.WriteString("The delegate used all available iterations before completing the task.")
		}
		if result.ExhaustReason != ExhaustNoOutput {
			out.WriteString(" If retrying, provide more specific guidance to narrow the scope — ")
			out.WriteString("e.g., exact file paths, entity IDs, or which step to focus on.")
		}
		out.WriteString("]\n\n")
		out.WriteString(summary)
		return out.String(), nil
	}
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
