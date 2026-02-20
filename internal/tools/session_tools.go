package tools

import (
	"context"
	"fmt"
)

// ConversationResetter is the interface for resetting conversations.
// Implemented by agent.Loop.
type ConversationResetter interface {
	ResetConversation(conversationID string) error
}

// SessionManager provides granular session lifecycle control beyond
// the nuclear conversation_reset. Implemented by agent.Loop.
type SessionManager interface {
	// CloseSession gracefully closes the current session, archives messages,
	// injects a carry-forward handoff into the new session, and starts fresh.
	CloseSession(conversationID, reason, carryForward string) error
	// CheckpointSession snapshots current conversation state without ending
	// the session. A safety net against crashes or compaction losing state.
	CheckpointSession(conversationID, label string) error
	// SplitSession retroactively splits the current session at a past message
	// boundary. Everything before the split point is archived; everything
	// after becomes the current session. Exactly one of atIndex or atMessage
	// must be provided (atIndex is a negative offset from the end).
	SplitSession(conversationID string, atIndex int, atMessage string) error
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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			reason, _ := args["reason"].(string)
			if reason == "" {
				reason = "user request"
			}

			convID := ConversationIDFromContext(ctx)
			if err := resetter.ResetConversation(convID); err != nil {
				return "", err
			}

			return "Conversation " + convID + " reset successfully. All previous messages have been archived. Reason: " + reason, nil
		},
	})
}

// SetSessionManager adds granular session management tools to the registry.
func (r *Registry) SetSessionManager(mgr SessionManager) {
	r.registerSessionClose(mgr)
	r.registerSessionCheckpoint(mgr)
	r.registerSessionSplit(mgr)
}

// registerSessionClose registers the session_close tool.
func (r *Registry) registerSessionClose(mgr SessionManager) {
	r.Register(&Tool{
		Name: "session_close",
		Description: "Gracefully close the current session and start a fresh one. " +
			"Archives all messages, triggers summarization, and injects a carry-forward " +
			"handoff summary into the new session. Use when transitioning between distinct " +
			"topics or when context has grown stale but you want to preserve continuity. " +
			"Prefer this over conversation_reset when you want to maintain a thread of context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Why the session is being closed (e.g., 'topic change', 'context refresh')",
				},
				"carry_forward": map[string]any{
					"type": "string",
					"description": "Handoff summary for the new session. Include key context, decisions, " +
						"and open threads that the new session should know about. Write as notes " +
						"to your future self.",
				},
			},
			"required": []string{"carry_forward"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			convID := ConversationIDFromContext(ctx)
			reason, _ := args["reason"].(string)
			if reason == "" {
				reason = "session close"
			}
			carryForward, _ := args["carry_forward"].(string)
			if carryForward == "" {
				// Models frequently use natural aliases for the parameter name.
				for _, alias := range []string{"handoff_note", "handoff", "summary", "handoff_summary", "note", "context"} {
					if v, ok := args[alias].(string); ok && v != "" {
						carryForward = v
						break
					}
				}
			}

			if err := mgr.CloseSession(convID, reason, carryForward); err != nil {
				return "", fmt.Errorf("close session: %w", err)
			}

			if carryForward != "" {
				return fmt.Sprintf("Session closed (%s). Carry-forward injected into new session (%d chars).", reason, len(carryForward)), nil
			}
			return fmt.Sprintf("Session closed (%s). WARNING: No carry-forward content received — new session has no prior context.", reason), nil
		},
	})
}

// registerSessionCheckpoint registers the session_checkpoint tool.
func (r *Registry) registerSessionCheckpoint(mgr SessionManager) {
	r.Register(&Tool{
		Name: "session_checkpoint",
		Description: "Create a checkpoint of the current session state without ending it. " +
			"Archives a snapshot of all messages as a safety net. The current session " +
			"continues uninterrupted. Use before risky operations or when you want to " +
			"preserve state at a known-good point.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{
					"type":        "string",
					"description": "Short label for this checkpoint (e.g., 'pre-refactor', 'before migration')",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			convID := ConversationIDFromContext(ctx)
			label, _ := args["label"].(string)
			if label == "" {
				label = "manual"
			}

			if err := mgr.CheckpointSession(convID, label); err != nil {
				return "", fmt.Errorf("checkpoint session: %w", err)
			}

			return fmt.Sprintf("Checkpoint created (%s). Session continues.", label), nil
		},
	})
}

// registerSessionSplit registers the session_split tool.
func (r *Registry) registerSessionSplit(mgr SessionManager) {
	r.Register(&Tool{
		Name: "session_split",
		Description: "Retroactively split the current session at a past message boundary. " +
			"Everything before the split point is archived as a completed session; " +
			"everything after becomes the start of the current session. " +
			"Provide either at_index (negative offset from end) or at_message (substring match), not both.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"at_index": map[string]any{
					"type":        "integer",
					"description": "Split at this message offset from the end (negative, e.g., -5 means 5 messages back from the end)",
				},
				"at_message": map[string]any{
					"type":        "string",
					"description": "Split before the first message whose content contains this substring",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			convID := ConversationIDFromContext(ctx)

			var atIndex int
			var atMessage string

			if v, ok := args["at_index"]; ok {
				switch n := v.(type) {
				case float64:
					atIndex = int(n)
				case int:
					atIndex = n
				}
			}
			if v, ok := args["at_message"].(string); ok {
				atMessage = v
			}

			if atIndex == 0 && atMessage == "" {
				return "", fmt.Errorf("provide either at_index (negative offset) or at_message (substring match)")
			}
			if atIndex != 0 && atMessage != "" {
				return "", fmt.Errorf("provide either at_index or at_message, not both")
			}
			if atIndex > 0 {
				return "", fmt.Errorf("at_index must be negative (offset from end)")
			}

			if err := mgr.SplitSession(convID, atIndex, atMessage); err != nil {
				return "", fmt.Errorf("split session: %w", err)
			}

			return "Session split successfully. Pre-split messages archived; post-split messages retained in current session.", nil
		},
	})
}
