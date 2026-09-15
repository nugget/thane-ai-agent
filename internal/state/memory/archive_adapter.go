package memory

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// MessageArchiver sets lifecycle status on messages in the unified table.
type MessageArchiver interface {
	ArchiveMessages(conversationID, sessionID, reason string) (int64, error)
}

// ToolCallArchiver sets lifecycle status on tool calls in the unified table.
type ToolCallArchiver interface {
	ArchiveToolCalls(conversationID, sessionID string) (int64, error)
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
	store    *ArchiveStore
	logger   *slog.Logger
	msgStore MessageArchiver  // sets status='archived' in unified messages table
	tcStore  ToolCallArchiver // sets status='archived' in unified tool_calls table

	// Gate every session lookup and creation against durable boundaries
	// and their cache publication. Readers hold RLock; mutations hold
	// Lock. Always acquire lifecycleMu before mu, and notify callbacks
	// only after releasing both locks so callbacks can re-enter.
	lifecycleMu sync.RWMutex

	// Track active sessions in memory for fast lookup
	mu       sync.RWMutex
	sessions map[string]sessionEntry // conversationID -> cached session
}

// ResetSession atomically closes the current session and opens its successor,
// optionally inserting a carry-forward system message into the new window.
func (a *ArchiveAdapter) ResetSession(conversationID, reason, carryForward string) error {
	return a.transitionSession(conversationID, sessionTransition{reason: reason, carryForward: carryForward, restart: true})
}

// CloseConversation atomically archives the active window and closes its
// session, retaining the conversation's identity, metadata, and transcript.
func (a *ArchiveAdapter) CloseConversation(conversationID, reason string) error {
	return a.transitionSession(conversationID, sessionTransition{reason: reason})
}

// CheckpointSession persists a bookmark of current message IDs and the active
// window without ending the session or removing any messages from context.
func (a *ArchiveAdapter) CheckpointSession(conversationID, label string) error {
	return a.transitionSession(conversationID, sessionTransition{checkpoint: true, label: label})
}

// SplitSession moves the suffix starting at boundaryMessageID into a new
// session and closes the prefix, preserving row identities and timestamps.
// Boundaries retaining compacted sources are rejected without changes.
func (a *ArchiveAdapter) SplitSession(conversationID, boundaryMessageID string) error {
	if boundaryMessageID == "" {
		return fmt.Errorf("split boundary message ID is required")
	}
	return a.transitionSession(conversationID, sessionTransition{reason: "split", boundaryID: boundaryMessageID})
}

func (a *ArchiveAdapter) transitionSession(conversationID string, op sessionTransition) error {
	a.lifecycleMu.Lock()
	result, err := a.store.transitionSession(conversationID, op)
	if err != nil {
		a.lifecycleMu.Unlock()
		return err
	}
	a.publishSessionTransitionLocked(conversationID, result)
	a.lifecycleMu.Unlock()
	if result.closedID != "" {
		if store, ok := a.msgStore.(*SQLiteStore); ok {
			store.forgetClipWarning(conversationID)
		}
		a.store.notifySessionClosed(result.closedID, op.reason)
	}
	a.logger.Info("session transition committed", "conversation_id", conversationID,
		"reason", op.reason, "checkpoint", op.checkpoint, "closed_session_id", result.closedID)
	return nil
}

// publishSessionTransitionLocked makes the committed boundary visible before
// releasing lifecycleMu. A failed transaction never reaches publication.
func (a *ArchiveAdapter) publishSessionTransitionLocked(conversationID string, result *sessionTransitionResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, conversationID)
	if result.current != nil {
		a.sessions[conversationID] = sessionEntry{id: result.current.ID, startedAt: result.current.StartedAt}
	}
}

// NewArchiveAdapter creates an adapter that implements agent.SessionArchiver.
// The msgStore and tcStore are used to set lifecycle status when archiving
// messages and tool calls in the unified table.
func NewArchiveAdapter(store *ArchiveStore, msgStore MessageArchiver, tcStore ToolCallArchiver, logger *slog.Logger) *ArchiveAdapter {
	return &ArchiveAdapter{
		store:    store,
		logger:   logger,
		msgStore: msgStore,
		tcStore:  tcStore,
		sessions: make(map[string]sessionEntry),
	}
}

// ArchiveConversation archives all messages and tool calls from a
// conversation by setting their status to 'archived' in the unified table.
func (a *ArchiveAdapter) ArchiveConversation(conversationID string, messages []Message, reason string) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	sessionID := a.activeSessionLocked(conversationID).id

	affected, err := a.msgStore.ArchiveMessages(conversationID, sessionID, reason)
	if err != nil {
		return err
	}

	a.archiveToolCalls(conversationID, sessionID)
	a.linkIterations(conversationID, sessionID)

	a.logger.Info("conversation archived",
		"conversation_id", conversationID,
		"messages", affected,
		"reason", reason,
	)
	return nil
}

// archiveToolCalls archives tool calls for a conversation by setting their
// status to 'archived' in the unified table.
func (a *ArchiveAdapter) archiveToolCalls(conversationID, sessionID string) {
	affected, err := a.tcStore.ArchiveToolCalls(conversationID, sessionID)
	if err != nil {
		a.logger.Error("failed to archive tool calls", "error", err)
		return
	}
	if affected > 0 {
		a.logger.Info("tool calls archived",
			"count", affected,
			"conversation_id", conversationID,
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
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

// StartSession begins a new session and returns its ID.
func (a *ArchiveAdapter) StartSession(conversationID string) (string, error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	return a.startSessionLocked(conversationID)
}

func (a *ArchiveAdapter) startSessionLocked(conversationID string) (string, error) {
	var opts []SessionOption
	if binding := a.conversationChannelBinding(conversationID); binding != nil {
		opts = append(opts, WithChannelBinding(binding))
	}
	sess, err := a.store.StartSessionWithOptions(conversationID, opts...)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.sessions[conversationID] = sessionEntry{id: sess.ID, startedAt: sess.StartedAt}
	a.mu.Unlock()

	a.logger.Info("session started",
		"session_id", sess.ID,
		"conversation_id", conversationID,
	)
	return sess.ID, nil
}

// EndSession ends a session. Session metadata is generated by the
// background summarizer worker, not here — this avoids a race with
// process shutdown that previously caused summaries to be lost.
func (a *ArchiveAdapter) EndSession(sessionID string, reason string) error {
	a.lifecycleMu.Lock()
	if err := a.store.endSessionAt(sessionID, reason, time.Now().UTC()); err != nil {
		a.lifecycleMu.Unlock()
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
	a.lifecycleMu.Unlock()
	a.store.notifySessionClosed(sessionID, reason)

	a.logger.Info("session ended",
		"session_id", sessionID,
		"reason", reason,
	)
	return nil
}

// ActiveSessionID returns the current session ID for a conversation, or empty.
func (a *ArchiveAdapter) ActiveSessionID(conversationID string) string {
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	return a.activeSessionLocked(conversationID).id
}

// activeSessionLocked requires lifecycleMu (shared or exclusive) so a cache
// miss cannot repopulate an old session across a committed transition.
func (a *ArchiveAdapter) activeSessionLocked(conversationID string) sessionEntry {
	a.mu.RLock()
	entry := a.sessions[conversationID]
	a.mu.RUnlock()

	if entry.id != "" {
		return entry
	}

	// Fall back to database lookup
	sess, err := a.store.ActiveSession(conversationID)
	if err != nil || sess == nil {
		return sessionEntry{}
	}

	// Cache it
	entry = sessionEntry{id: sess.ID, startedAt: sess.StartedAt}
	a.mu.Lock()
	a.sessions[conversationID] = entry
	a.mu.Unlock()

	return entry
}

// OnMessage is a no-op retained for interface compatibility. Session
// message counts are now computed from the unified messages table.
func (a *ArchiveAdapter) OnMessage(_ string) {}

// EnsureSession starts a session if none is active for the conversation.
func (a *ArchiveAdapter) EnsureSession(conversationID string) string {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if sid := a.activeSessionLocked(conversationID).id; sid != "" {
		return sid
	}

	sid, err := a.startSessionLocked(conversationID)
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
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	return a.activeSessionLocked(conversationID).startedAt
}

// ActiveConversationIDs returns the conversation IDs with currently open
// sessions. It prefers the in-memory cache for hot-path accuracy and
// merges in any active sessions found in the store so startup recovery
// and direct store writes are also reflected.
func (a *ArchiveAdapter) ActiveConversationIDs() []string {
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	ids := make(map[string]struct{})

	a.mu.RLock()
	for conversationID, entry := range a.sessions {
		if entry.id == "" {
			continue
		}
		ids[conversationID] = struct{}{}
	}
	a.mu.RUnlock()

	sessions, err := a.store.ActiveSessionsWithLastActivity()
	if err != nil {
		a.logger.Warn("failed to enumerate active sessions", "error", err)
	} else {
		for _, sess := range sessions {
			if sess.SessionID == "" || sess.ConversationID == "" {
				continue
			}
			ids[sess.ConversationID] = struct{}{}
		}
	}

	out := make([]string, 0, len(ids))
	for conversationID := range ids {
		out = append(out, conversationID)
	}
	slices.Sort(out)
	return out
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

func (a *ArchiveAdapter) conversationChannelBinding(conversationID string) *ChannelBinding {
	if conversationID == "" {
		return nil
	}
	switch store := a.msgStore.(type) {
	case *SQLiteStore:
		conv := store.GetConversation(conversationID)
		if conv == nil || conv.Metadata == nil {
			return nil
		}
		return conv.Metadata.ChannelBinding.Clone()
	default:
		return nil
	}
}
