package checkpoint

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StateProvider is implemented by components that contribute to checkpoints.
type StateProvider interface {
	// CheckpointState returns the current state for checkpointing.
	CheckpointState() (interface{}, error)
}

// ConversationProvider provides conversation data.
type ConversationProvider interface {
	GetConversations() ([]Conversation, error)
}

// FactProvider provides memory facts.
type FactProvider interface {
	GetFacts() ([]Fact, error)
}

// TaskProvider provides scheduled tasks.
type TaskProvider interface {
	GetTasks() ([]Task, error)
}

// Checkpointer manages automatic and manual checkpointing.
type Checkpointer struct {
	store *Store
	log   *slog.Logger

	// Providers for collecting state
	conversations ConversationProvider
	facts         FactProvider
	tasks         TaskProvider

	// Config
	periodicInterval int // Create checkpoint every N messages (0 = disabled)

	// State
	mu            sync.Mutex
	messagesSince int // Messages since last checkpoint
}

// Config for the checkpointer.
type Config struct {
	PeriodicMessages int // Checkpoint every N messages (0 = disabled)
}

// NewCheckpointer creates a new checkpointer.
func NewCheckpointer(db *sql.DB, cfg Config, log *slog.Logger) (*Checkpointer, error) {
	store, err := NewStore(db)
	if err != nil {
		return nil, err
	}

	return &Checkpointer{
		store:            store,
		log:              log,
		periodicInterval: cfg.PeriodicMessages,
	}, nil
}

// SetProviders configures where to get state from.
func (c *Checkpointer) SetProviders(conv ConversationProvider, facts FactProvider, tasks TaskProvider) {
	c.conversations = conv
	c.facts = facts
	c.tasks = tasks
}

// OnMessage should be called after each message is processed.
// It triggers periodic checkpointing if configured.
func (c *Checkpointer) OnMessage() {
	if c.periodicInterval <= 0 {
		return
	}

	c.mu.Lock()
	c.messagesSince++
	shouldCheckpoint := c.messagesSince >= c.periodicInterval
	if shouldCheckpoint {
		c.messagesSince = 0
	}
	c.mu.Unlock()

	if shouldCheckpoint {
		go func() {
			if _, err := c.Create(TriggerPeriodic, ""); err != nil {
				c.log.Error("periodic checkpoint failed", "error", err)
			}
		}()
	}
}

// Create makes a new checkpoint with the given trigger and optional note.
func (c *Checkpointer) Create(trigger Trigger, note string) (*Checkpoint, error) {
	state, err := c.collectState()
	if err != nil {
		return nil, fmt.Errorf("collect state: %w", err)
	}

	cp, err := c.store.Create(trigger, note, state)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	c.log.Info("checkpoint created",
		"id", cp.ID.String()[:8],
		"trigger", trigger,
		"messages", cp.MessageCount,
		"facts", cp.FactCount,
		"bytes", cp.ByteSize,
	)

	return cp, nil
}

// CreatePreFailover creates a checkpoint before switching models.
func (c *Checkpointer) CreatePreFailover(fromModel, toModel string) (*Checkpoint, error) {
	note := fmt.Sprintf("failover: %s → %s", fromModel, toModel)
	return c.Create(TriggerPreFailover, note)
}

// CreateShutdown creates a checkpoint during graceful shutdown.
func (c *Checkpointer) CreateShutdown() (*Checkpoint, error) {
	return c.Create(TriggerShutdown, "graceful shutdown")
}

// OnFailover implements agent.FailoverHandler for checkpoint creation before model switches.
func (c *Checkpointer) OnFailover(ctx context.Context, fromModel, toModel, reason string) error {
	_, err := c.CreatePreFailover(fromModel, toModel)
	return err
}

// Get retrieves a checkpoint by ID.
func (c *Checkpointer) Get(id uuid.UUID) (*Checkpoint, error) {
	return c.store.Get(id)
}

// List returns recent checkpoints.
func (c *Checkpointer) List(limit int) ([]*Checkpoint, error) {
	return c.store.List(limit)
}

// Latest returns the most recent checkpoint.
func (c *Checkpointer) Latest() (*Checkpoint, error) {
	return c.store.Latest()
}

// Delete removes a checkpoint.
func (c *Checkpointer) Delete(id uuid.UUID) error {
	return c.store.Delete(id)
}

// Prune removes old checkpoints.
func (c *Checkpointer) Prune(olderThan time.Duration, minKeep int) (int, error) {
	return c.store.Prune(olderThan, minKeep)
}

// Restore applies a checkpoint's state to the providers.
// This is a placeholder — actual restoration depends on provider implementations.
func (c *Checkpointer) Restore(id uuid.UUID) error {
	cp, err := c.store.Get(id)
	if err != nil {
		return fmt.Errorf("get checkpoint: %w", err)
	}

	c.log.Info("restoring checkpoint",
		"id", cp.ID.String()[:8],
		"created", cp.CreatedAt.Format(time.RFC3339),
		"messages", cp.MessageCount,
		"facts", cp.FactCount,
	)

	// TODO: Implement actual restoration by calling provider restore methods
	// For now, we just validate the checkpoint can be loaded

	return nil
}

// StartupStatus returns info about persisted state for logging at startup.
// Since SQLite persists automatically, this just reports what exists.
type StartupStatus struct {
	Conversations  int        `json:"conversations"`
	Messages       int        `json:"messages"`
	Facts          int        `json:"facts"`
	LastCheckpoint *time.Time `json:"last_checkpoint,omitempty"`
}

// GetStartupStatus collects state info for startup logging.
func (c *Checkpointer) GetStartupStatus() (*StartupStatus, error) {
	status := &StartupStatus{}

	// Get conversation/message counts from provider
	if c.conversations != nil {
		convs, err := c.conversations.GetConversations()
		if err == nil {
			status.Conversations = len(convs)
			for _, conv := range convs {
				status.Messages += len(conv.Messages)
			}
		}
	}

	// Get fact count from provider
	if c.facts != nil {
		facts, err := c.facts.GetFacts()
		if err == nil {
			status.Facts = len(facts)
		}
	}

	// Get latest checkpoint time
	latest, err := c.store.Latest()
	if err == nil && latest != nil {
		status.LastCheckpoint = &latest.CreatedAt
	}

	return status, nil
}

// LogStartupStatus logs the current persisted state.
func (c *Checkpointer) LogStartupStatus() {
	status, err := c.GetStartupStatus()
	if err != nil {
		c.log.Warn("failed to get startup status", "error", err)
		return
	}

	if status.Conversations == 0 && status.Facts == 0 {
		c.log.Info("starting fresh (no persisted state)")
		return
	}

	attrs := []any{
		"conversations", status.Conversations,
		"messages", status.Messages,
		"facts", status.Facts,
	}
	if status.LastCheckpoint != nil {
		attrs = append(attrs, "last_checkpoint", status.LastCheckpoint.Format(time.RFC3339))
	}

	c.log.Info("resuming from persisted state", attrs...)
}

func (c *Checkpointer) collectState() (*State, error) {
	state := &State{}

	if c.conversations != nil {
		convs, err := c.conversations.GetConversations()
		if err != nil {
			return nil, fmt.Errorf("get conversations: %w", err)
		}
		state.Conversations = convs
	}

	if c.facts != nil {
		facts, err := c.facts.GetFacts()
		if err != nil {
			return nil, fmt.Errorf("get facts: %w", err)
		}
		state.Facts = facts
	}

	if c.tasks != nil {
		tasks, err := c.tasks.GetTasks()
		if err != nil {
			return nil, fmt.Errorf("get tasks: %w", err)
		}
		state.Tasks = tasks
	}

	return state, nil
}
