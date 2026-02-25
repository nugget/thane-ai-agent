package memory

import (
	"log/slog"
	"sync"
	"time"
)

// ToolCallSource provides tool call records for archiving.
type ToolCallSource interface {
	GetToolCalls(conversationID string, limit int) []ToolCall
	ClearToolCalls(conversationID string) error
}

// MessageArchiver sets lifecycle status on messages in the unified table.
type MessageArchiver interface {
	ArchiveMessages(conversationID, sessionID, reason string) (int64, error)
}

// sessionEntry caches an active session's ID and start time to avoid
// repeated database lookups on the per-turn hot path.
type sessionEntry struct {
	id        string
	startedAt time.Time
}

// ArchiveAdapter bridges the ArchiveStore to the agent.SessionArchiver interface.
// It manages session lifecycle and coordinates message archival in the unified
// messages table.
type ArchiveAdapter struct {
	store      *ArchiveStore
	logger     *slog.Logger
	toolSource ToolCallSource  // optional — archives tool calls alongside messages
	msgStore   MessageArchiver // optional — sets status='archived' in unified table

	// Track active sessions in memory for fast lookup
	mu       sync.RWMutex
	sessions map[string]sessionEntry // conversationID -> cached session
}

// NewArchiveAdapter creates an adapter that implements agent.SessionArchiver.
func NewArchiveAdapter(store *ArchiveStore, logger *slog.Logger) *ArchiveAdapter {
	return &ArchiveAdapter{
		store:    store,
		logger:   logger,
		sessions: make(map[string]sessionEntry),
	}
}

// SetToolCallSource configures a source for tool call records to archive.
// Must be called during initialization before any concurrent access.
func (a *ArchiveAdapter) SetToolCallSource(source ToolCallSource) {
	a.toolSource = source
}

// SetMessageStore configures the unified message store for status-based
// archival. When set, ArchiveConversation uses UPDATE (status='archived')
// instead of cross-DB INSERT.
// Must be called during initialization before any concurrent access.
func (a *ArchiveAdapter) SetMessageStore(store MessageArchiver) {
	a.msgStore = store
}

// ArchiveConversation archives all messages from a conversation.
//
// In unified mode (msgStore set): updates message status to 'archived' in
// the unified table. Messages already exist — no cross-DB copy.
//
// In legacy mode: copies messages to archive_messages in archive.db.
func (a *ArchiveAdapter) ArchiveConversation(conversationID string, messages []Message, reason string) error {
	sessionID := a.ActiveSessionID(conversationID)

	// Unified mode: UPDATE status in the same table.
	if a.msgStore != nil {
		affected, err := a.msgStore.ArchiveMessages(conversationID, sessionID, reason)
		if err != nil {
			return err
		}

		a.archiveToolCalls(conversationID, sessionID)
		a.linkIterations(conversationID, sessionID)

		a.logger.Info("conversation archived",
			"conversation", conversationID,
			"messages", affected,
			"reason", reason,
		)
		return nil
	}

	// Legacy mode: INSERT into archive_messages.
	archived := make([]ArchivedMessage, len(messages))
	for i, m := range messages {
		archived[i] = ArchivedMessage{
			ID:             m.ID,
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

	a.archiveToolCalls(conversationID, sessionID)
	a.linkIterations(conversationID, sessionID)

	a.logger.Info("conversation archived",
		"conversation", conversationID,
		"messages", len(messages),
		"reason", reason,
	)
	return nil
}

// archiveToolCalls copies tool calls from the working store to the archive.
// Tool call unification happens in PR 2; for now we still copy.
func (a *ArchiveAdapter) archiveToolCalls(conversationID, sessionID string) {
	if a.toolSource == nil {
		return
	}

	calls := a.toolSource.GetToolCalls(conversationID, 10000)
	if len(calls) == 0 {
		return
	}

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
		return
	}

	// Clear archived tool calls from the working store so they
	// aren't re-archived on the next session boundary (#271).
	if err := a.toolSource.ClearToolCalls(conversationID); err != nil {
		a.logger.Warn("failed to clear tool calls after archiving",
			"conversation", conversationID,
			"error", err,
		)
	}
}

// linkIterations links archived tool calls to their parent iterations.
func (a *ArchiveAdapter) linkIterations(conversationID, sessionID string) {
	if sessionID == "" {
		return
	}
	if err := a.store.LinkPendingIterationToolCalls(sessionID); err != nil {
		a.logger.Warn("failed to link tool calls to iterations",
			"conversation", conversationID,
			"error", err,
		)
	}
}

// StartSession begins a new session and returns its ID.
func (a *ArchiveAdapter) StartSession(conversationID string) (string, error) {
	sess, err := a.store.StartSession(conversationID)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.sessions[conversationID] = sessionEntry{id: sess.ID, startedAt: sess.StartedAt}
	a.mu.Unlock()

	a.logger.Info("session started",
		"session", ShortID(sess.ID),
		"conversation", conversationID,
	)
	return sess.ID, nil
}

// EndSession ends a session. Session metadata is generated by the
// background summarizer worker, not here — this avoids a race with
// process shutdown that previously caused summaries to be lost.
func (a *ArchiveAdapter) EndSession(sessionID string, reason string) error {
	if err := a.store.EndSession(sessionID, reason); err != nil {
		return err
	}

	// Remove from active cache
	a.mu.Lock()
	for conv, entry := range a.sessions {
		if entry.id == sessionID {
			delete(a.sessions, conv)
			break
		}
	}
	a.mu.Unlock()

	a.logger.Info("session ended",
		"session", ShortID(sessionID),
		"reason", reason,
	)
	return nil
}

// ActiveSessionID returns the current session ID for a conversation, or empty.
func (a *ArchiveAdapter) ActiveSessionID(conversationID string) string {
	a.mu.RLock()
	entry := a.sessions[conversationID]
	a.mu.RUnlock()

	if entry.id != "" {
		return entry.id
	}

	// Fall back to database lookup
	sess, err := a.store.ActiveSession(conversationID)
	if err != nil || sess == nil {
		return ""
	}

	// Cache it
	a.mu.Lock()
	a.sessions[conversationID] = sessionEntry{id: sess.ID, startedAt: sess.StartedAt}
	a.mu.Unlock()

	return sess.ID
}

// OnMessage is a no-op retained for interface compatibility. Session
// message counts are now computed from the unified messages table.
func (a *ArchiveAdapter) OnMessage(_ string) {}

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

// ActiveSessionStartedAt returns when the active session for a conversation
// began, or the zero time if there is no active session. Uses the in-memory
// cache populated by StartSession and ActiveSessionID to avoid per-turn
// database lookups.
func (a *ArchiveAdapter) ActiveSessionStartedAt(conversationID string) time.Time {
	a.mu.RLock()
	entry := a.sessions[conversationID]
	a.mu.RUnlock()

	if entry.id != "" {
		return entry.startedAt
	}

	// Fall back to database lookup and cache the result.
	sess, err := a.store.ActiveSession(conversationID)
	if err != nil || sess == nil {
		return time.Time{}
	}

	a.mu.Lock()
	a.sessions[conversationID] = sessionEntry{id: sess.ID, startedAt: sess.StartedAt}
	a.mu.Unlock()

	return sess.StartedAt
}

// ArchiveIterations persists a batch of iteration records to the archive store.
func (a *ArchiveAdapter) ArchiveIterations(iterations []ArchivedIteration) error {
	return a.store.ArchiveIterations(iterations)
}

// LinkPendingIterationToolCalls links archived tool calls to their parent
// iterations using the tool_call_ids stored on the iteration records.
func (a *ArchiveAdapter) LinkPendingIterationToolCalls(sessionID string) error {
	return a.store.LinkPendingIterationToolCalls(sessionID)
}

// Store returns the underlying ArchiveStore for direct access (API endpoints, etc.)
func (a *ArchiveAdapter) Store() *ArchiveStore {
	return a.store
}
