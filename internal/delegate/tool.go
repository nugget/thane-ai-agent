package delegate

import (
	"context"
	"fmt"

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

		// Format the result with metadata header.
		header := fmt.Sprintf("[Delegate completed: profile=%s, model=%s, iter=%d, tokens=%s]",
			profileName, result.Model, result.Iterations, formatTokens(result.OutputTokens))
		if result.Exhausted {
			header = fmt.Sprintf("[Delegate budget exhausted: profile=%s, model=%s, iter=%d, tokens=%s]",
				profileName, result.Model, result.Iterations, formatTokens(result.OutputTokens))
		}

		if result.Content == "" {
			return header + "\n\nNo results returned.", nil
		}
		return header + "\n\n" + result.Content, nil
	}
}
