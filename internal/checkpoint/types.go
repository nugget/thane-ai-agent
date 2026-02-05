// Package checkpoint provides state snapshotting and restoration for Thane.
package checkpoint

import (
	"time"

	"github.com/google/uuid"
)

// Trigger describes what caused a checkpoint to be created.
type Trigger string

const (
	TriggerManual     Trigger = "manual"      // Explicit API call
	TriggerPeriodic   Trigger = "periodic"    // Every N messages
	TriggerPreFailover Trigger = "pre-failover" // Before model switch
	TriggerShutdown   Trigger = "shutdown"    // Graceful shutdown
	TriggerPreCompact Trigger = "pre-compact" // Before memory compaction
)

// Checkpoint represents a point-in-time snapshot of agent state.
type Checkpoint struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Trigger   Trigger   `json:"trigger"`
	Note      string    `json:"note,omitempty"` // Optional human description

	// Captured state
	State *State `json:"state"`

	// Metadata
	ByteSize    int64  `json:"byte_size"`    // Compressed size
	MessageCount int   `json:"message_count"` // Total messages captured
	FactCount   int    `json:"fact_count"`   // Total facts captured
}

// State holds the actual restorable data.
type State struct {
	// Conversations with full message history
	Conversations []Conversation `json:"conversations"`

	// Long-term memory facts
	Facts []Fact `json:"facts"`

	// Pending scheduled tasks
	Tasks []Task `json:"tasks,omitempty"`

	// Agent configuration at checkpoint time
	Config *ConfigSnapshot `json:"config,omitempty"`
}

// Conversation represents a chat session.
type Conversation struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  []Message `json:"messages"`
}

// Message is a single turn in a conversation.
type Message struct {
	ID        uuid.UUID `json:"id"`
	Role      string    `json:"role"` // "system", "user", "assistant", "tool"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`

	// Tool-specific fields
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolID    string     `json:"tool_id,omitempty"`    // For tool responses
	ToolName  string     `json:"tool_name,omitempty"`  // For tool responses
}

// ToolCall represents a function call made by the assistant.
type ToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Fact is a piece of long-term memory.
type Fact struct {
	ID        uuid.UUID `json:"id"`
	Category  string    `json:"category"` // "user", "home", "preference", etc.
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Source    string    `json:"source,omitempty"`    // Where this fact came from
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Confidence float64  `json:"confidence,omitempty"` // 0-1, how sure we are
}

// Task is a scheduled action.
type Task struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Schedule    string    `json:"schedule"` // Cron expression or timestamp
	Action      string    `json:"action"`   // What to do
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

// ConfigSnapshot captures relevant config at checkpoint time.
type ConfigSnapshot struct {
	DefaultModel string `json:"default_model"`
	HAConfigured bool   `json:"ha_configured"`
	TalentCount  int    `json:"talent_count"`
}

// Summary returns a human-readable summary of the checkpoint.
func (c *Checkpoint) Summary() string {
	return c.ID.String()[:8] + " | " + 
		c.CreatedAt.Format("2006-01-02 15:04") + " | " +
		string(c.Trigger) + " | " +
		formatCount(c.MessageCount, "msg") + ", " +
		formatCount(c.FactCount, "fact")
}

func formatCount(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return string(rune('0'+n%10)) + " " + unit + "s"
}
