package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
)

// CompactionConfig controls compaction behavior.
type CompactionConfig struct {
	MaxTokens            int     // Conversation token budget compaction defends
	TriggerRatio         float64 // Trigger compaction at this ratio (e.g., 0.7 = 70%)
	KeepRecent           int     // Number of recent messages to always keep
	MinMessagesToCompact int     // Minimum messages before considering compaction
}

// DefaultCompactionConfig returns sensible defaults.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		MaxTokens:            32000, // See config `compaction.max_tokens` (#1168)
		TriggerRatio:         0.7,   // Trigger at 70% full
		KeepRecent:           10,    // Keep last 10 messages
		MinMessagesToCompact: 20,    // Don't compact tiny conversations
	}
}

// CompactionSummaryPrefix marks a stored system message as a compaction
// summary. It is both the render prefix and the discriminator that
// separates summaries from other system rows (e.g. session handoffs),
// so folding can find prior summaries without a schema change.
const CompactionSummaryPrefix = "[Conversation Summary]"

// CompactableStore is the interface for stores that support compaction.
type CompactableStore interface {
	GetTokenCount(conversationID string) int
	GetMessagesForCompaction(conversationID string, keep int) []Message
	GetActiveCompactionSummaries(conversationID string) []Message
	MarkCompactedByIDs(conversationID string, ids []string) error
	AddCompactionSummaryAt(conversationID, summary string, ts time.Time) error
}

// WorkingMemoryReader is the subset of WorkingMemoryStore needed by the
// compactor. Defined as an interface for testability and to avoid coupling
// the compactor to the concrete store type.
type WorkingMemoryReader interface {
	Get(conversationID string) (string, time.Time, error)
}

// Compactor handles conversation compaction.
type Compactor struct {
	store         CompactableStore
	config        CompactionConfig
	summarizer    Summarizer
	workingMemory WorkingMemoryReader // optional — include in compaction prompt
	logger        *slog.Logger

	// inFlight single-flights compaction per conversation. The
	// summarize step is a slow LLM call, and without the guard every
	// turn that lands during one compaction spawns another — all
	// reading overlapping history and each stacking its own summary
	// (#1168 defect 1).
	mu       sync.Mutex
	inFlight map[string]bool
}

// Summarizer generates summaries from messages. When workingMemory is
// non-empty, it is included in the prompt so the summarizer preserves
// experiential context through compaction.
type Summarizer interface {
	Summarize(ctx context.Context, messages []Message, workingMemory string) (string, error)
}

// NewCompactor creates a new compactor.
func NewCompactor(store CompactableStore, config CompactionConfig, summarizer Summarizer, logger *slog.Logger) *Compactor {
	return &Compactor{
		store:      store,
		config:     config,
		summarizer: summarizer,
		logger:     logger,
		inFlight:   make(map[string]bool),
	}
}

// SetWorkingMemoryStore configures a working memory store so that the
// compactor can include experiential context in the compaction prompt.
func (c *Compactor) SetWorkingMemoryStore(wm WorkingMemoryReader) {
	c.workingMemory = wm
}

// CompactionThreshold returns the token count at which compaction triggers.
func (c *Compactor) CompactionThreshold() int {
	return int(float64(c.config.MaxTokens) * c.config.TriggerRatio)
}

// NeedsCompaction checks if a conversation needs compaction.
func (c *Compactor) NeedsCompaction(conversationID string) bool {
	tokenCount := c.store.GetTokenCount(conversationID)
	return tokenCount > c.CompactionThreshold()
}

// tryAcquire marks the conversation as compacting, returning false when
// a compaction is already in flight for it.
func (c *Compactor) tryAcquire(conversationID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inFlight[conversationID] {
		return false
	}
	c.inFlight[conversationID] = true
	return true
}

func (c *Compactor) release(conversationID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inFlight, conversationID)
}

// Compact performs compaction on a conversation: it folds any prior
// summary together with the older messages into a single fresh summary,
// marks the folded rows compacted, and inserts the summary at the
// compacted region's temporal position so it renders at the head of
// surviving history rather than interleaved into the live dialogue.
// Concurrent calls for the same conversation coalesce — the extras
// return nil without doing work.
func (c *Compactor) Compact(ctx context.Context, conversationID string) error {
	if !c.tryAcquire(conversationID) {
		c.logger.Debug("compaction already in flight; skipping",
			"conversation_id", conversationID)
		return nil
	}
	defer c.release(conversationID)

	// Re-check under the flight guard: the trigger that queued this
	// call may predate a compaction that just finished.
	if !c.NeedsCompaction(conversationID) {
		return nil
	}

	// Get messages to compact (older ones)
	messages := c.store.GetMessagesForCompaction(conversationID, c.config.KeepRecent)

	// Snap the compaction boundary to a turn edge: a trailing user
	// message here means its reply sits in the keep window (or hasn't
	// arrived), and compacting the question while keeping the answer
	// orphans the reply (#1168 defect 4). Bounding memory outranks
	// turn integrity, though — if the trim would starve compaction
	// (e.g. a long user monologue), keep the untrimmed set.
	trimmed := messages
	for len(trimmed) > 0 && trimmed[len(trimmed)-1].Role == "user" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) >= c.config.MinMessagesToCompact || len(messages) < c.config.MinMessagesToCompact {
		messages = trimmed
	}

	c.logger.Debug("compaction check",
		"conversation_id", conversationID,
		"eligible_messages", len(messages),
		"min_required", c.config.MinMessagesToCompact,
		"keep_recent", c.config.KeepRecent,
		"token_count", c.store.GetTokenCount(conversationID),
		"max_tokens", c.config.MaxTokens,
	)

	if len(messages) < c.config.MinMessagesToCompact {
		c.logger.Debug("compaction skipped: not enough messages",
			"conversation_id", conversationID,
			"eligible", len(messages),
			"required", c.config.MinMessagesToCompact,
		)
		return nil // Not enough to bother
	}

	// Fold prior summaries into this pass so exactly one summary row
	// exists per conversation. Without the fold, summaries are
	// immortal (they are system rows, which compaction never selects)
	// and keep the token count pinned above the trigger — a ratchet
	// that squashes every ~15 fresh messages into yet another stacked
	// summary (#1168 defect 2). Priors prepend to the summarizer input
	// so their content carries forward through the new summary.
	priors := c.store.GetActiveCompactionSummaries(conversationID)
	folded := make([]Message, 0, len(priors)+len(messages))
	folded = append(folded, priors...)
	folded = append(folded, messages...)

	// Messages persist in the unified table with lifecycle status.
	// Compaction marks them as 'compacted' — they're never deleted and
	// remain searchable in the archive. No separate archive step needed.

	// Read working memory for inclusion in the compaction prompt.
	var workingMem string
	if c.workingMemory != nil {
		content, _, err := c.workingMemory.Get(conversationID)
		if err != nil {
			c.logger.Warn("failed to read working memory for compaction",
				"conversation_id", conversationID, "error", err)
			// Non-fatal — proceed without working memory context.
		} else {
			workingMem = content
		}
	}

	// Generate summary
	summary, err := c.summarizer.Summarize(ctx, folded, workingMem)
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}

	// Format as a system message
	formattedSummary := formatCompactionSummary(folded, summary)

	// Mark exactly the rows that were summarized — by ID, not by a
	// wall-clock cutoff that can slice through rows the summarizer
	// never saw.
	ids := make([]string, len(folded))
	for i, m := range folded {
		ids[i] = m.ID
	}
	if err := c.store.MarkCompactedByIDs(conversationID, ids); err != nil {
		return fmt.Errorf("mark compacted: %w", err)
	}

	// The summary takes the folded region's earliest timestamp so it
	// sorts where the region was, at the head of surviving history. A
	// now() stamp would interleave it into the middle of the live
	// exchange (#1168 defect 3).
	if err := c.store.AddCompactionSummaryAt(conversationID, formattedSummary, folded[0].Timestamp); err != nil {
		return fmt.Errorf("add summary: %w", err)
	}

	return nil
}

// formatCompactionSummary creates a structured summary message.
func formatCompactionSummary(messages []Message, summary string) string {
	if len(messages) == 0 {
		return summary
	}

	startTime := messages[0].Timestamp
	endTime := messages[len(messages)-1].Timestamp

	var sb strings.Builder
	sb.WriteString(CompactionSummaryPrefix + "\n")
	sb.WriteString(fmt.Sprintf("Period: %s to %s\n",
		startTime.Format("2006-01-02 15:04"),
		endTime.Format("2006-01-02 15:04")))
	sb.WriteString(fmt.Sprintf("Messages compacted: %d\n\n", len(messages)))
	sb.WriteString(summary)

	return sb.String()
}

// CompactionStats returns stats about compaction for a conversation.
func (c *Compactor) CompactionStats(conversationID string) map[string]any {
	tokenCount := c.store.GetTokenCount(conversationID)
	threshold := int(float64(c.config.MaxTokens) * c.config.TriggerRatio)

	return map[string]any{
		"token_count":      tokenCount,
		"max_tokens":       c.config.MaxTokens,
		"trigger_at":       threshold,
		"needs_compaction": tokenCount > threshold,
		"ratio":            float64(tokenCount) / float64(c.config.MaxTokens),
	}
}

// LLMSummarizer uses an LLM to generate summaries.
type LLMSummarizer struct {
	llmFunc func(ctx context.Context, prompt string) (string, error)
}

// NewLLMSummarizer creates a summarizer that uses an LLM.
func NewLLMSummarizer(llmFunc func(ctx context.Context, prompt string) (string, error)) *LLMSummarizer {
	return &LLMSummarizer{llmFunc: llmFunc}
}

// Summarize generates a summary of the messages using an LLM. When
// workingMemory is non-empty, it is included in the prompt so the
// summarizer preserves experiential context through compaction. Prior
// compaction summaries arrive as leading system-role messages and fold
// into the new summary through the same transcript rendering.
func (s *LLMSummarizer) Summarize(ctx context.Context, messages []Message, workingMemory string) (string, error) {
	// Build conversation text
	var sb strings.Builder
	for _, m := range messages {
		role := m.Role
		if len(role) > 0 {
			role = strings.ToUpper(role[:1]) + role[1:]
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n\n", role, m.Content))
	}

	return s.llmFunc(ctx, prompts.CompactionPrompt(sb.String(), workingMemory))
}

// SimpleSummarizer creates a basic summary without LLM (fallback).
type SimpleSummarizer struct{}

// Summarize creates a simple extractive summary.
func (s *SimpleSummarizer) Summarize(ctx context.Context, messages []Message, _ string) (string, error) {
	var topics []string
	var actions []string

	for _, m := range messages {
		// Extract user questions as topics
		if m.Role == "user" && len(m.Content) < 100 {
			topics = append(topics, "- "+m.Content)
		}
		// Note tool usage
		if m.Role == "tool" {
			actions = append(actions, "- Tool was called")
		}
	}

	var sb strings.Builder
	sb.WriteString("Topics discussed:\n")
	if len(topics) > 0 {
		for _, t := range topics[:min(5, len(topics))] {
			sb.WriteString(t + "\n")
		}
	} else {
		sb.WriteString("- General conversation\n")
	}

	if len(actions) > 0 {
		sb.WriteString("\nActions taken:\n")
		sb.WriteString(fmt.Sprintf("- %d tool calls\n", len(actions)))
	}

	return sb.String(), nil
}
