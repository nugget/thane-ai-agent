// Package memory provides conversation memory storage.
package memory

import (
	"sync"
	"time"
)

// Store is the interface for memory storage.
type MemoryStore interface {
	GetMessages(conversationID string) []Message
	AddMessage(conversationID, role, content string) error
	GetConversation(id string) *Conversation
	Clear(conversationID string) error
	Stats() map[string]any
}

// Message represents a conversation message.
type Message struct {
	Role      string    `json:"role"` // system, user, assistant, tool
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Conversation holds the state of a single conversation.
type Conversation struct {
	ID        string    `json:"id"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store manages conversation memory.
// Currently in-memory; will add SQLite persistence later.
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
// Returns nil if not found.
func (s *Store) GetConversation(id string) *Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conv, ok := s.conversations[id]
	if !ok {
		return nil
	}

	// Return a copy to avoid race conditions
	return conv.copy()
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

// AddMessage adds a message to a conversation.
func (s *Store) AddMessage(conversationID string, role, content string) error {
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

	conv.Messages = append(conv.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
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
// Returns empty slice if conversation doesn't exist.
func (s *Store) GetMessages(conversationID string) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conv, ok := s.conversations[conversationID]
	if !ok {
		return []Message{}
	}

	// Return a copy
	msgs := make([]Message, len(conv.Messages))
	copy(msgs, conv.Messages)
	return msgs
}

// Clear removes a conversation.
func (s *Store) Clear(conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conversations, conversationID)
}

// GetTokenCount returns estimated token count for a conversation.
func (s *Store) GetTokenCount(conversationID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conv, ok := s.conversations[conversationID]
	if !ok {
		return 0
	}

	total := 0
	for _, m := range conv.Messages {
		total += len(m.Content) / 4 // Rough estimate: 4 chars per token
	}
	return total
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

// GetAllConversations returns all conversations for checkpointing.
func (s *Store) GetAllConversations() []*Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	convs := make([]*Conversation, 0, len(s.conversations))
	for _, conv := range s.conversations {
		convs = append(convs, conv.copy())
	}
	return convs
}

// RestoreConversations replaces all conversations from a checkpoint.
func (s *Store) RestoreConversations(convs []*Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.conversations = make(map[string]*Conversation, len(convs))
	for _, conv := range convs {
		s.conversations[conv.ID] = conv.copy()
	}
}

func (c *Conversation) copy() *Conversation {
	msgs := make([]Message, len(c.Messages))
	copy(msgs, c.Messages)
	return &Conversation{
		ID:        c.ID,
		Messages:  msgs,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}
