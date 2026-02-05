// Package memory provides conversation memory storage.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CompactionConfig controls compaction behavior.
type CompactionConfig struct {
	MaxTokens       int     // Context window size
	TriggerRatio    float64 // Trigger compaction at this ratio (e.g., 0.7 = 70%)
	KeepRecent      int     // Number of recent messages to always keep
	MinMessagesToCompact int // Minimum messages before considering compaction
}

// DefaultCompactionConfig returns sensible defaults.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		MaxTokens:            8000,  // Conservative default
		TriggerRatio:         0.7,   // Trigger at 70% full
		KeepRecent:           10,    // Keep last 10 messages
		MinMessagesToCompact: 20,    // Don't compact tiny conversations
	}
}

// Compactor handles conversation compaction.
type Compactor struct {
	store      *SQLiteStore
	config     CompactionConfig
	summarizer Summarizer
}

// Summarizer generates summaries from messages.
type Summarizer interface {
	Summarize(ctx context.Context, messages []Message) (string, error)
}

// NewCompactor creates a new compactor.
func NewCompactor(store *SQLiteStore, config CompactionConfig, summarizer Summarizer) *Compactor {
	return &Compactor{
		store:      store,
		config:     config,
		summarizer: summarizer,
	}
}

// NeedsCompaction checks if a conversation needs compaction.
func (c *Compactor) NeedsCompaction(conversationID string) bool {
	tokenCount := c.store.GetTokenCount(conversationID)
	threshold := int(float64(c.config.MaxTokens) * c.config.TriggerRatio)
	return tokenCount > threshold
}

// Compact performs compaction on a conversation.
func (c *Compactor) Compact(ctx context.Context, conversationID string) error {
	// Get messages to compact (older ones)
	messages := c.store.GetMessagesForCompaction(conversationID, c.config.KeepRecent)
	
	if len(messages) < c.config.MinMessagesToCompact {
		return nil // Not enough to bother
	}

	// Find the cutoff time (last message being compacted)
	var cutoffTime time.Time
	if len(messages) > 0 {
		cutoffTime = messages[len(messages)-1].Timestamp
	}

	// Generate summary
	summary, err := c.summarizer.Summarize(ctx, messages)
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}

	// Format as a system message
	formattedSummary := formatCompactionSummary(messages, summary)

	// Mark old messages as compacted
	if err := c.store.MarkCompacted(conversationID, cutoffTime.Add(time.Millisecond)); err != nil {
		return fmt.Errorf("mark compacted: %w", err)
	}

	// Add summary message
	if err := c.store.AddCompactionSummary(conversationID, formattedSummary); err != nil {
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
	sb.WriteString("[Conversation Summary]\n")
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
		"token_count":     tokenCount,
		"max_tokens":      c.config.MaxTokens,
		"trigger_at":      threshold,
		"needs_compaction": tokenCount > threshold,
		"ratio":           float64(tokenCount) / float64(c.config.MaxTokens),
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

// Summarize generates a summary of the messages using an LLM.
func (s *LLMSummarizer) Summarize(ctx context.Context, messages []Message) (string, error) {
	// Build conversation text
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(fmt.Sprintf("%s: %s\n\n", strings.Title(m.Role), m.Content))
	}

	prompt := fmt.Sprintf(`Summarize this conversation concisely. Focus on:
1. Key topics discussed
2. Decisions made or preferences expressed
3. Actions taken (tool calls, state changes)
4. Any open items or things to remember

Keep the summary under 500 words. Use bullet points.

Conversation:
%s

Summary:`, sb.String())

	return s.llmFunc(ctx, prompt)
}

// SimpleSummarizer creates a basic summary without LLM (fallback).
type SimpleSummarizer struct{}

// Summarize creates a simple extractive summary.
func (s *SimpleSummarizer) Summarize(ctx context.Context, messages []Message) (string, error) {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
