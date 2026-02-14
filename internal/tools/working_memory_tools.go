package tools

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// SetWorkingMemoryStore adds the session_working_memory tool to the registry.
// This tool allows the agent to read and write free-form experiential notes
// for the current conversation, capturing texture that mechanical compaction
// destroys.
func (r *Registry) SetWorkingMemoryStore(store *memory.WorkingMemoryStore) {
	r.Register(&Tool{
		Name: "session_working_memory",
		Description: "Read or write your working memory for this conversation. " +
			"Working memory is your private scratchpad for experiential context: " +
			"emotional tone, conversational arc, relationship dynamics, and unresolved threads. " +
			"It persists across compaction and is auto-injected into your context each turn. " +
			"Use 'read' to see current contents, 'write' to replace entirely.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"read", "write"},
					"description": "Action to perform: 'read' returns current working memory, 'write' replaces it entirely",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "New working memory content (required for 'write'). Write in first person as notes to your future self.",
				},
			},
			"required": []string{"action"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			action, _ := args["action"].(string)
			convID := ConversationIDFromContext(ctx)

			switch action {
			case "read":
				content, updatedAt, err := store.Get(convID)
				if err != nil {
					return "", fmt.Errorf("read working memory: %w", err)
				}
				if content == "" {
					return "(no working memory for this conversation)", nil
				}
				return fmt.Sprintf("Last updated: %s\n\n%s", updatedAt.Format("2006-01-02 15:04:05"), content), nil

			case "write":
				content, _ := args["content"].(string)
				if content == "" {
					return "", fmt.Errorf("content is required for write action")
				}
				if err := store.Set(convID, content); err != nil {
					return "", fmt.Errorf("write working memory: %w", err)
				}
				return "Working memory updated.", nil

			default:
				return "", fmt.Errorf("unknown action %q: expected 'read' or 'write'", action)
			}
		},
	})
}
