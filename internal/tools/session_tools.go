package tools

import "context"

// ConversationResetter is the interface for resetting conversations.
// Implemented by agent.Loop.
type ConversationResetter interface {
	ResetConversation(conversationID string) error
}

// SetConversationResetter adds conversation management tools to the registry.
func (r *Registry) SetConversationResetter(resetter ConversationResetter) {
	r.Register(&Tool{
		Name: "conversation_reset",
		Description: "Reset the current conversation, archiving all messages and starting fresh. " +
			"ONLY use when the user EXPLICITLY asks to clear history, start over, or reset. " +
			"NEVER call this tool on your own initiative. All messages are archived before clearing.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Brief reason for the reset (logged for debugging)",
				},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			reason, _ := args["reason"].(string)
			if reason == "" {
				reason = "user request"
			}

			if err := resetter.ResetConversation("default"); err != nil {
				return "", err
			}

			return "Conversation reset successfully. All previous messages have been archived. Reason: " + reason, nil
		},
	})
}
