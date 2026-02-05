package checkpoint

import (
	"time"

	"github.com/google/uuid"
)

// ParseUUID parses a string to UUID, returning zero UUID on error.
func ParseUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

// MemoryStoreAdapter adapts a memory store to ConversationProvider.
type MemoryStoreAdapter struct {
	store interface {
		GetAllConversations() []*MemoryConversation
	}
}

// MemoryConversation is the memory package's conversation type.
// We define it here to avoid import cycles.
type MemoryConversation struct {
	ID        string
	Messages  []MemoryMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MemoryMessage is the memory package's message type.
type MemoryMessage struct {
	Role      string
	Content   string
	Timestamp time.Time
}

// ConversationProviderFunc is a function that provides conversations.
type ConversationProviderFunc func() ([]Conversation, error)

// GetConversations implements ConversationProvider.
func (f ConversationProviderFunc) GetConversations() ([]Conversation, error) {
	return f()
}

// FactProviderFunc is a function that provides facts.
type FactProviderFunc func() ([]Fact, error)

// GetFacts implements FactProvider.
func (f FactProviderFunc) GetFacts() ([]Fact, error) {
	return f()
}

// TaskProviderFunc is a function that provides tasks.
type TaskProviderFunc func() ([]Task, error)

// GetTasks implements TaskProvider.
func (f TaskProviderFunc) GetTasks() ([]Task, error) {
	return f()
}

// ConvertMemoryConversation converts from memory store format to checkpoint format.
func ConvertMemoryConversation(id string, messages []MemoryMessage, createdAt, updatedAt time.Time) Conversation {
	conv := Conversation{
		ID:        id,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Messages:  make([]Message, len(messages)),
	}

	for i, m := range messages {
		msgID, _ := uuid.NewV7()
		conv.Messages[i] = Message{
			ID:        msgID,
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.Timestamp,
		}
	}

	return conv
}
