package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// SessionLifecycle changes the active conversation window without deleting
// its durable transcript. Implementations commit session boundaries, message
// ownership, and tool-call lifecycle state together before reporting success.
type SessionLifecycle interface {
	// ResetSession closes the current session and starts its successor. A
	// non-empty carryForward becomes the successor's first system message.
	ResetSession(conversationID, reason, carryForward string) error
	// CloseConversation closes the current session without starting another.
	CloseConversation(conversationID, reason string) error
	// CheckpointSession records a durable bookmark of the current message
	// IDs and active window without changing message lifecycle statuses.
	CheckpointSession(conversationID, label string) error
	// SplitSession closes the prefix before boundaryMessageID and retains
	// the suffix in a new session, preserving message IDs and timestamps.
	// A suffix containing compacted sources is rejected without changes;
	// callers must choose a boundary after the compacted history.
	SplitSession(conversationID, boundaryMessageID string) error
}

type sessionTransition struct {
	reason       string
	carryForward string
	restart      bool
	checkpoint   bool
	label        string
	boundaryID   string
}

type sessionTransitionResult struct {
	closedID string
	current  *Session
}

type lifecycleMessage struct {
	id     string
	at     time.Time
	active bool
}

// transitionSession is deliberately restricted to the production shared
// database. Cross-database copy semantics cannot implement these guarantees.
func (s *ArchiveStore) transitionSession(conversationID string, op sessionTransition) (*sessionTransitionResult, error) {
	if s.messagesDB != s.db || s.msgTableName != "messages" || s.tcTableName != "tool_calls" {
		return nil, fmt.Errorf("session lifecycle requires the shared conversation database")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin session transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !op.restart && !op.checkpoint && op.boundaryID == "" {
		var pending bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sessions WHERE conversation_id = ? AND ended_at IS NULL)
			OR EXISTS(SELECT 1 FROM messages WHERE conversation_id = ? AND status IN ('active', 'compacted'))`,
			conversationID, conversationID).Scan(&pending); err != nil {
			return nil, fmt.Errorf("check pending session close: %w", err)
		}
		if !pending {
			return &sessionTransitionResult{}, nil
		}
	}

	now := time.Now().UTC()
	current, err := lifecycleSession(tx, conversationID, now)
	if err != nil {
		return nil, err
	}
	messages, err := lifecycleMessages(tx, conversationID)
	if err != nil {
		return nil, err
	}
	result := &sessionTransitionResult{current: current}
	if op.checkpoint {
		if len(messages) == 0 {
			return nil, fmt.Errorf("no messages to checkpoint")
		}
		if err := claimLifecycleRows(tx, conversationID, current.ID); err != nil {
			return nil, err
		}
		if err := recordSessionCheckpoint(tx, current.ID, conversationID, op.label, now, messages); err != nil {
			return nil, err
		}
	} else {
		boundary := len(messages)
		endedAt := now
		if op.boundaryID != "" {
			boundary = -1
			for i, msg := range messages {
				if msg.id == op.boundaryID {
					boundary, endedAt = i, msg.at
					break
				}
			}
			if boundary <= 0 {
				return nil, fmt.Errorf("split boundary is no longer within the current conversation; read the current messages and retry")
			}
			for _, message := range messages[boundary:] {
				if !message.active {
					return nil, fmt.Errorf("cannot split inside compacted history; choose a later boundary after the compacted messages so the retained conversation keeps its context")
				}
			}
		}
		if err := claimLifecycleRows(tx, conversationID, current.ID); err != nil {
			return nil, err
		}
		if err := archiveLifecyclePrefix(tx, conversationID, messages[:boundary], op.reason, now); err != nil {
			return nil, err
		}
		// Tool executions retain the session that produced their iteration.
		// A split can move the associated message, but moving its execution
		// would detach (session_id, iteration_index) from the original trace.
		if _, err := tx.Exec(`UPDATE tool_calls SET status = 'archived', archived_at = ?
			WHERE conversation_id = ? AND status = 'active'`, now, conversationID); err != nil {
			return nil, fmt.Errorf("archive session tool calls: %w", err)
		}
		if _, err := tx.Exec(`UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ?`, endedAt.Format(time.RFC3339Nano), op.reason, current.ID); err != nil {
			return nil, fmt.Errorf("close session: %w", err)
		}
		result.closedID, result.current = current.ID, nil
		if op.restart || op.boundaryID != "" {
			next, err := insertLifecycleSession(tx, conversationID, endedAt, current)
			if err != nil {
				return nil, err
			}
			result.current = next
			if op.boundaryID != "" {
				if err := moveLifecycleSuffix(tx, conversationID, next.ID, messages[boundary:]); err != nil {
					return nil, err
				}
			}
			if op.carryForward != "" {
				if err := insertLifecycleHandoff(tx, conversationID, next.ID, op.carryForward, now); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session transition: %w", err)
	}
	return result, nil
}

func lifecycleSession(tx *sql.Tx, conversationID string, now time.Time) (*Session, error) {
	var session Session
	var started string
	var metadata, parentSession, parentTool sql.NullString
	err := tx.QueryRow(`SELECT id, started_at, metadata, parent_session_id, parent_tool_call_id
		FROM sessions WHERE conversation_id = ? AND ended_at IS NULL ORDER BY started_at DESC, id DESC LIMIT 1`,
		conversationID).Scan(&session.ID, &started, &metadata, &parentSession, &parentTool)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read current session: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		var first sql.NullString
		if err := tx.QueryRow(`SELECT MIN(timestamp) FROM messages WHERE conversation_id = ? AND status IN ('active', 'compacted')`, conversationID).Scan(&first); err != nil {
			return nil, fmt.Errorf("read first session message: %w", err)
		}
		if first.Valid {
			firstAt, err := database.ParseTimestamp(first.String)
			if err != nil {
				return nil, fmt.Errorf("parse first session message: %w", err)
			}
			now = firstAt
		}
		return insertLifecycleSession(tx, conversationID, now, nil)
	}
	session.ConversationID = conversationID
	session.StartedAt, err = database.ParseTimestamp(started)
	if err != nil {
		return nil, fmt.Errorf("parse session start: %w", err)
	}
	session.ParentSessionID, session.ParentToolCallID = parentSession.String, parentTool.String
	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &session.Metadata); err != nil {
			return nil, fmt.Errorf("read current session metadata: %w", err)
		}
	}
	return &session, nil
}

func insertLifecycleSession(tx *sql.Tx, conversationID string, at time.Time, previous *Session) (*Session, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate session ID: %w", err)
	}
	session := &Session{ID: id.String(), ConversationID: conversationID, StartedAt: at}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO conversations (id, created_at, updated_at) VALUES (?, ?, ?)`, conversationID, at, at); err != nil {
		return nil, fmt.Errorf("ensure conversation identity: %w", err)
	}
	var rawMetadata sql.NullString
	if err := tx.QueryRow(`SELECT metadata FROM conversations WHERE id = ?`, conversationID).Scan(&rawMetadata); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read conversation metadata: %w", err)
	}
	if rawMetadata.Valid {
		metadata, err := parseConversationMetadata(rawMetadata.String)
		if err != nil {
			return nil, fmt.Errorf("read conversation metadata: %w", err)
		}
		if metadata != nil && metadata.ChannelBinding != nil {
			WithChannelBinding(metadata.ChannelBinding)(session)
		}
	}
	if previous != nil {
		// A session split/rotation is still within the same delegation and
		// channel. Retain identity linkage, not the old session's synthesis.
		session.ParentSessionID = previous.ParentSessionID
		session.ParentToolCallID = previous.ParentToolCallID
		if session.Metadata == nil && previous.Metadata != nil && previous.Metadata.ChannelBinding != nil {
			WithChannelBinding(previous.Metadata.ChannelBinding)(session)
		}
	}
	metadataJSON, err := sessionMetadataJSON(session.Metadata)
	if err != nil {
		return nil, fmt.Errorf("encode session metadata: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO sessions (id, conversation_id, started_at, metadata, parent_session_id, parent_tool_call_id)
		VALUES (?, ?, ?, ?, ?, ?)`, session.ID, conversationID, at.Format(time.RFC3339Nano), nullString(string(metadataJSON)),
		nullString(session.ParentSessionID), nullString(session.ParentToolCallID)); err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	return session, nil
}

func lifecycleMessages(tx *sql.Tx, conversationID string) ([]lifecycleMessage, error) {
	rows, err := tx.Query(`SELECT id, timestamp, status FROM messages
		WHERE conversation_id = ? AND status IN ('active', 'compacted') ORDER BY timestamp, id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("read current session messages: %w", err)
	}
	defer rows.Close()
	var messages []lifecycleMessage
	for rows.Next() {
		var message lifecycleMessage
		var timestamp, status string
		if err := rows.Scan(&message.id, &timestamp, &status); err != nil {
			return nil, fmt.Errorf("scan current session message: %w", err)
		}
		message.at, err = database.ParseTimestamp(timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse message timestamp: %w", err)
		}
		message.active = status == "active"
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func claimLifecycleRows(tx *sql.Tx, conversationID, sessionID string) error {
	for _, table := range []string{"messages", "tool_calls"} {
		if _, err := tx.Exec(`UPDATE `+table+` SET session_id = ? WHERE conversation_id = ?
			AND session_id IS NULL AND status IN ('active', 'compacted')`, sessionID, conversationID); err != nil {
			return fmt.Errorf("claim session %s: %w", table, err)
		}
	}
	return linkLifecycleIterations(tx, conversationID, sessionID)
}

func linkLifecycleIterations(tx *sql.Tx, conversationID, sessionID string) error {
	rows, err := tx.Query(`SELECT iteration_index, tool_call_ids FROM archive_iterations
		WHERE session_id = ? AND tool_call_ids IS NOT NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("read session iteration links: %w", err)
	}
	type iterationLink struct {
		index int
		ids   []string
	}
	var links []iterationLink
	for rows.Next() {
		var link iterationLink
		var raw string
		if err := rows.Scan(&link.index, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("scan session iteration links: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &link.ids); err != nil {
			rows.Close()
			return fmt.Errorf("decode session iteration links: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read session iteration links: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close session iteration links: %w", err)
	}
	for _, link := range links {
		for _, id := range link.ids {
			if _, err := tx.Exec(`UPDATE tool_calls SET iteration_index = ? WHERE id = ? AND conversation_id = ? AND session_id = ?`,
				link.index, id, conversationID, sessionID); err != nil {
				return fmt.Errorf("link session tool call: %w", err)
			}
		}
	}
	return nil
}

func archiveLifecyclePrefix(tx *sql.Tx, conversationID string, messages []lifecycleMessage, reason string, at time.Time) error {
	for _, message := range messages {
		if _, err := tx.Exec(`UPDATE messages SET status = 'archived', archived_at = ?, archive_reason = ?
			WHERE id = ? AND conversation_id = ?`, at, reason, message.id, conversationID); err != nil {
			return fmt.Errorf("archive session message: %w", err)
		}
	}
	return nil
}

func moveLifecycleSuffix(tx *sql.Tx, conversationID, newSessionID string, messages []lifecycleMessage) error {
	for _, message := range messages {
		if _, err := tx.Exec(`UPDATE messages SET session_id = ? WHERE id = ? AND conversation_id = ?`, newSessionID, message.id, conversationID); err != nil {
			return fmt.Errorf("move session message: %w", err)
		}
	}
	return nil
}

func insertLifecycleHandoff(tx *sql.Tx, conversationID, sessionID, handoff string, at time.Time) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate handoff ID: %w", err)
	}
	content := "[Session Handoff]\n" + handoff
	if _, err := tx.Exec(`INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp, token_count, origin)
		VALUES (?, ?, ?, 'system', ?, ?, ?, ?)`, id.String(), conversationID, sessionID, content, at,
		llm.EstimateTokens(content), OriginInternal); err != nil {
		return fmt.Errorf("write session handoff: %w", err)
	}
	return nil
}

func recordSessionCheckpoint(tx *sql.Tx, sessionID, conversationID, label string, at time.Time, messages []lifecycleMessage) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate checkpoint ID: %w", err)
	}
	messageIDs := make([]string, 0, len(messages))
	activeIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		messageIDs = append(messageIDs, message.id)
		if message.active {
			activeIDs = append(activeIDs, message.id)
		}
	}
	allJSON, err := json.Marshal(messageIDs)
	if err != nil {
		return fmt.Errorf("encode checkpoint messages: %w", err)
	}
	activeJSON, err := json.Marshal(activeIDs)
	if err != nil {
		return fmt.Errorf("encode checkpoint active window: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO session_checkpoints (id, conversation_id, session_id, label, created_at, message_ids, active_message_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id.String(), conversationID, sessionID, label, at, string(allJSON), string(activeJSON)); err != nil {
		return fmt.Errorf("record session checkpoint: %w", err)
	}
	return nil
}
