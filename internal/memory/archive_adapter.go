// Package memory provides conversation memory storage.
package memory

import (
	"log/slog"
	"sync"
)

// ToolCallSource provides tool call records for archiving.
type ToolCallSource interface {
	GetToolCalls(conversationID string, limit int) []ToolCall
}

// ArchiveAdapter bridges the ArchiveStore to the agent.SessionArchiver interface.
// It manages session lifecycle and converts between memory and archive message types.
type ArchiveAdapter struct {
	store      *ArchiveStore
	logger     *slog.Logger
	toolSource ToolCallSource // optional — archives tool calls alongside messages

	// Track active session IDs in memory for fast lookup
	mu       sync.RWMutex
	sessions map[string]string // conversationID -> sessionID
}

// NewArchiveAdapter creates an adapter that implements agent.SessionArchiver.
func NewArchiveAdapter(store *ArchiveStore, logger *slog.Logger) *ArchiveAdapter {
	return &ArchiveAdapter{
		store:    store,
		logger:   logger,
		sessions: make(map[string]string),
	}
}

// SetToolCallSource configures a source for tool call records to archive.
func (a *ArchiveAdapter) SetToolCallSource(source ToolCallSource) {
	a.toolSource = source
}

// ArchiveConversation archives all messages from a conversation.
func (a *ArchiveAdapter) ArchiveConversation(conversationID string, messages []Message, reason string) error {
	sessionID := a.ActiveSessionID(conversationID)

	archived := make([]ArchivedMessage, len(messages))
	for i, m := range messages {
		archived[i] = ArchivedMessage{
			ConversationID: conversationID,
			SessionID:      sessionID,
			Role:           m.Role,
			Content:        m.Content,
			Timestamp:      m.Timestamp,
			TokenCount:     estimateTokens(m.Content),
			ToolCalls:      m.ToolCalls,
			ToolCallID:     m.ToolCallID,
			ArchiveReason:  reason,
		}
	}

	if err := a.store.ArchiveMessages(archived); err != nil {
		return err
	}

	// Archive associated tool calls if a source is configured
	toolCallCount := 0
	if a.toolSource != nil {
		calls := a.toolSource.GetToolCalls(conversationID, 10000)
		if len(calls) > 0 {
			archivedCalls := make([]ArchivedToolCall, len(calls))
			for i, tc := range calls {
				archivedCalls[i] = ArchivedToolCall{
					ID:             tc.ID,
					ConversationID: tc.ConversationID,
					SessionID:      sessionID,
					ToolName:       tc.ToolName,
					Arguments:      tc.Arguments,
					Result:         tc.Result,
					Error:          tc.Error,
					StartedAt:      tc.StartedAt,
					CompletedAt:    tc.CompletedAt,
					DurationMs:     tc.DurationMs,
				}
			}
			if err := a.store.ArchiveToolCalls(archivedCalls); err != nil {
				a.logger.Error("failed to archive tool calls", "error", err)
				// Don't fail the whole archive for tool calls
			} else {
				toolCallCount = len(archivedCalls)
			}
		}
	}

	a.logger.Info("conversation archived",
		"conversation", conversationID,
		"messages", len(messages),
		"tool_calls", toolCallCount,
		"reason", reason,
	)
	return nil
}

// StartSession begins a new session and returns its ID.
func (a *ArchiveAdapter) StartSession(conversationID string) (string, error) {
	sess, err := a.store.StartSession(conversationID)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.sessions[conversationID] = sess.ID
	a.mu.Unlock()

	a.logger.Info("session started",
		"session", sess.ID[:8],
		"conversation", conversationID,
	)
	return sess.ID, nil
}

// EndSession ends a session.
func (a *ArchiveAdapter) EndSession(sessionID string, reason string) error {
	if err := a.store.EndSession(sessionID, reason); err != nil {
		return err
	}

	// Remove from active cache
	a.mu.Lock()
	for conv, sid := range a.sessions {
		if sid == sessionID {
			delete(a.sessions, conv)
			break
		}
	}
	a.mu.Unlock()

	a.logger.Info("session ended",
		"session", sessionID[:8],
		"reason", reason,
	)
	return nil
}

// ActiveSessionID returns the current session ID for a conversation, or empty.
func (a *ArchiveAdapter) ActiveSessionID(conversationID string) string {
	a.mu.RLock()
	sid := a.sessions[conversationID]
	a.mu.RUnlock()

	if sid != "" {
		return sid
	}

	// Fall back to database lookup
	sess, err := a.store.ActiveSession(conversationID)
	if err != nil || sess == nil {
		return ""
	}

	// Cache it
	a.mu.Lock()
	a.sessions[conversationID] = sess.ID
	a.mu.Unlock()

	return sess.ID
}

// OnMessage tracks message count for the active session.
func (a *ArchiveAdapter) OnMessage(conversationID string) {
	sid := a.ActiveSessionID(conversationID)
	if sid == "" {
		return
	}
	// Best-effort — don't propagate errors for a counter
	_ = a.store.IncrementSessionCount(sid)
}

// EnsureSession starts a session if none is active for the conversation.
func (a *ArchiveAdapter) EnsureSession(conversationID string) string {
	if sid := a.ActiveSessionID(conversationID); sid != "" {
		return sid
	}

	sid, err := a.StartSession(conversationID)
	if err != nil {
		a.logger.Error("failed to start session", "error", err)
		return ""
	}
	return sid
}

// Store returns the underlying ArchiveStore for direct access (API endpoints, etc.)
func (a *ArchiveAdapter) Store() *ArchiveStore {
	return a.store
}
