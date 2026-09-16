// Package memory provides conversation memory storage and session archiving.
//
// The package has two main subsystems:
//
// Active memory (SQLiteStore) manages the working conversation context —
// messages that are actively used for LLM context windows. Messages can be
// compacted (summarized) when the context grows too large.
//
// Session archive (ArchiveStore) reads the same durable message rows as active
// memory. Compaction, reset, and shutdown change lifecycle state rather than
// deleting or copying transcripts. [SessionLifecycle] commits boundaries and
// row ownership together; checkpoints bookmark existing message IDs without
// changing the active context. The archive supports full-text search with gap-aware context
// expansion — search results include surrounding conversation bounded by
// natural silence gaps rather than rigid message counts.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is the interface for memory storage backends (in-memory
// and SQLite). Both [Store] and [SQLiteStore] satisfy it.
type MemoryStore interface {
	GetMessages(ctx context.Context, conversationID string) ([]Message, error)
	AddMessage(conversationID, role, content, origin string) error
	GetConversation(ctx context.Context, id string) (*Conversation, error)
	Clear(conversationID string) error
	Stats() map[string]any
}

// Origin values for [Message.Origin]. Each enqueue site stamps the value
// it knows to be true; a site that cannot know passes "" rather than
// guessing, so an empty origin always means "unstamped", never "wrong".
const (
	// OriginChannel marks a message that crossed a channel transport
	// to or from the conversation's human counterparty — an inbound
	// message recorded by a channel bridge, or an outbound send
	// recorded by a delivery path. Channel-shaped turn builders MUST
	// declare this on their requests; undeclared turn-builder turns
	// default to OriginWake.
	OriginChannel = "channel"

	// OriginWake marks an internally-originated wake prompt: a turn
	// opened by loop_wake, a subscription fire, a scheduled trigger,
	// or another internal source rather than by counterparty contact.
	OriginWake = "wake"

	// OriginAPI marks a message that entered through an operator-facing
	// API surface (REST endpoints, foreign-protocol compat shims).
	OriginAPI = "api"

	// OriginInternal marks rows the system authored into the
	// conversation itself: the loop's own generated output, detached
	// completion injections, and system notices. Whether such a row was
	// also delivered to a channel counterparty is a delivery-path fact,
	// recorded separately (see OriginChannel).
	OriginInternal = "internal"
)

// Message represents a conversation message. This is the unified type for
// both active working-memory messages and archived session transcripts.
// Reader projections may omit ownership and archival fields. A SessionID
// alone does not imply archival: active rows can already belong to a session
// after a checkpoint, split, or carry-forward handoff.
type Message struct {
	ID             string    `json:"id"`                        // Stable UUIDv7 assigned at creation time
	ConversationID string    `json:"conversation_id,omitempty"` // Owning conversation, when included by the reader
	SessionID      string    `json:"session_id,omitempty"`      // Owning session, independent of lifecycle status
	Role           string    `json:"role"`                      // system, user, assistant, tool
	Content        string    `json:"content"`
	Timestamp      time.Time `json:"timestamp"`
	TokenCount     int       `json:"token_count,omitempty"`    // Estimated token count
	ToolCalls      string    `json:"tool_calls,omitempty"`     // JSON array of tool calls (assistant messages)
	ToolCallID     string    `json:"tool_call_id,omitempty"`   // Tool call ID (tool response messages)
	ArchivedAt     time.Time `json:"archived_at,omitzero"`     // When the message was archived
	ArchiveReason  string    `json:"archive_reason,omitempty"` // Why: compaction, reset, shutdown, import
	// MidTurn marks a user message that arrived mid-turn and was merged into
	// an in-flight turn at an iteration boundary (#1221/#1230), rather than
	// opening its own turn. The structured contract that replaces
	// substring-matching the channel-rendered arrival marker.
	MidTurn bool `json:"mid_turn,omitempty"`
	// Origin records how the row entered the conversation — see the
	// Origin* constants. Stamped at ingest by the enqueue site; empty on
	// rows written before provenance stamping existed and on paths that
	// cannot know their provenance (notably mid-turn mailbox merges,
	// whose per-item origin is flattened before recording). Same
	// structured-contract lineage as MidTurn: a stamped value is the
	// source of truth, and content sniffing is permissible only as a
	// documented fallback for rows whose Origin is empty.
	Origin string `json:"origin,omitempty"`
}

// Conversation holds the state of a single conversation.
type Conversation struct {
	ID        string                `json:"id"`
	Messages  []Message             `json:"messages"`
	Metadata  *ConversationMetadata `json:"metadata,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// Store is an in-memory conversation memory backend. For persistent
// storage see [SQLiteStore].
type Store struct {
	mu            sync.RWMutex
	conversations map[string]*Conversation
	maxMessages   int // per conversation
}

// NewStore creates a new memory store.
func NewStore(maxMessages int) *Store {
	if maxMessages <= 0 {
		maxMessages = 100
	}
	return &Store{
		conversations: make(map[string]*Conversation),
		maxMessages:   maxMessages,
	}
}

// GetConversation retrieves a conversation by ID.
// Returns nil without an error if not found, or an error on cancellation.
func (s *Store) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conv, ok := s.conversations[id]
	if !ok {
		return nil, nil
	}

	// Return a copy to avoid race conditions
	return conv.copy(), nil
}

// GetOrCreateConversation retrieves or creates a conversation.
func (s *Store) GetOrCreateConversation(id string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()

	conv, ok := s.conversations[id]
	if !ok {
		conv = &Conversation{
			ID:        id,
			Messages:  []Message{},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		s.conversations[id] = conv
	}

	return conv.copy()
}

// AddMessage adds a message to a conversation. origin records how the
// message entered the conversation (see the Origin* constants); pass ""
// when the enqueue site cannot know.
func (s *Store) AddMessage(conversationID string, role, content, origin string) error {
	return s.addMessage(conversationID, role, content, origin, false)
}

// AddMidTurnMessage adds a message that arrived mid-turn and was merged into
// an in-flight turn (#1230), tagging it so consumers can identify the
// injection without substring-matching the rendered arrival marker. origin
// follows the same contract as [Store.AddMessage].
func (s *Store) AddMidTurnMessage(conversationID string, role, content, origin string) error {
	return s.addMessage(conversationID, role, content, origin, true)
}

func (s *Store) addMessage(conversationID string, role, content, origin string, midTurn bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conv, ok := s.conversations[conversationID]
	if !ok {
		conv = &Conversation{
			ID:        conversationID,
			Messages:  []Message{},
			CreatedAt: time.Now(),
		}
		s.conversations[conversationID] = conv
	}

	msgID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate message ID: %w", err)
	}
	conv.Messages = append(conv.Messages, Message{
		ID:        msgID.String(),
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
		MidTurn:   midTurn,
		Origin:    origin,
	})
	conv.UpdatedAt = time.Now()

	// Trim if over max (keep system messages + recent)
	if len(conv.Messages) > s.maxMessages {
		// Find system messages
		var systemMsgs []Message
		var otherMsgs []Message
		for _, m := range conv.Messages {
			if m.Role == "system" {
				systemMsgs = append(systemMsgs, m)
			} else {
				otherMsgs = append(otherMsgs, m)
			}
		}

		// Keep system + last N-len(system) messages
		keep := s.maxMessages - len(systemMsgs)
		if keep < 10 {
			keep = 10
		}
		if len(otherMsgs) > keep {
			otherMsgs = otherMsgs[len(otherMsgs)-keep:]
		}

		conv.Messages = append(systemMsgs, otherMsgs...)
	}

	return nil
}

// GetMessages retrieves messages for a conversation.
// Returns an empty slice if the conversation does not exist, or an error on cancellation.
func (s *Store) GetMessages(ctx context.Context, conversationID string) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conv, ok := s.conversations[conversationID]
	if !ok {
		return []Message{}, nil
	}

	// Return a copy
	msgs := make([]Message, len(conv.Messages))
	copy(msgs, conv.Messages)
	return msgs, nil
}

// Clear removes a conversation.
func (s *Store) Clear(conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conversations, conversationID)
	return nil
}

// GetTokenCount returns the estimated token count, or an error on cancellation.
func (s *Store) GetTokenCount(ctx context.Context, conversationID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	conv, ok := s.conversations[conversationID]
	if !ok {
		return 0, nil
	}

	total := 0
	for _, m := range conv.Messages {
		total += len(m.Content) / 4 // Rough estimate: 4 chars per token
	}
	return total, nil
}

// Stats returns memory statistics.
func (s *Store) Stats() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totalMessages := 0
	for _, conv := range s.conversations {
		totalMessages += len(conv.Messages)
	}

	return map[string]any{
		"conversations": len(s.conversations),
		"messages":      totalMessages,
		"max_per_conv":  s.maxMessages,
	}
}

func (c *Conversation) copy() *Conversation {
	msgs := make([]Message, len(c.Messages))
	copy(msgs, c.Messages)
	return &Conversation{
		ID:        c.ID,
		Messages:  msgs,
		Metadata:  c.Metadata.Clone(),
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// PutConversationMetadata replaces the typed metadata for a
// conversation, creating the conversation record if needed.
func (s *Store) PutConversationMetadata(conversationID string, metadata *ConversationMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conv, ok := s.conversations[conversationID]
	if !ok {
		conv = &Conversation{
			ID:        conversationID,
			Messages:  []Message{},
			CreatedAt: time.Now(),
		}
		s.conversations[conversationID] = conv
	}
	conv.Metadata = metadata.Clone()
	conv.UpdatedAt = time.Now()
	return nil
}

// BindConversationChannel updates only the channel-binding
// portion of a conversation's typed metadata.
func (s *Store) BindConversationChannel(conversationID string, binding *ChannelBinding) error {
	var metadata *ConversationMetadata
	conv, err := s.GetConversation(context.Background(), conversationID)
	if err != nil {
		return fmt.Errorf("read conversation before binding channel: %w", err)
	}
	if conv != nil && conv.Metadata != nil {
		metadata = conv.Metadata.Clone()
	}
	if metadata == nil {
		metadata = &ConversationMetadata{}
	}
	metadata.ChannelBinding = binding.Normalize()
	return s.PutConversationMetadata(conversationID, metadata)
}
