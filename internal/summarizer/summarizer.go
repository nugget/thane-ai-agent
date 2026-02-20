// Package summarizer provides a background worker that generates session
// metadata (titles, tags, summaries) for archived sessions that lack it.
// This decouples metadata generation from session lifecycle events,
// ensuring summaries are always produced even when sessions end during
// process shutdown.
package summarizer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// Config controls the summarizer worker behavior.
type Config struct {
	// Interval between periodic scans for unsummarized sessions.
	// Default: 5 minutes.
	Interval time.Duration

	// Timeout per individual session summarization LLM call.
	// Default: 60 seconds.
	Timeout time.Duration

	// PauseBetween is the delay between processing consecutive sessions
	// to avoid overwhelming the LLM or starving interactive requests.
	// Default: 5 seconds.
	PauseBetween time.Duration

	// BatchSize is the max number of unsummarized sessions to fetch per scan.
	// Default: 10.
	BatchSize int

	// ModelPreference is a soft hint for which model to use.
	// Passed as HintModelPreference to the router. If empty, the router
	// picks freely based on other hints.
	ModelPreference string
}

// DefaultConfig returns sensible defaults for the summarizer worker.
func DefaultConfig() Config {
	return Config{
		Interval:     5 * time.Minute,
		Timeout:      60 * time.Second,
		PauseBetween: 5 * time.Second,
		BatchSize:    10,
	}
}

func (c *Config) applyDefaults() {
	d := DefaultConfig()
	if c.Interval <= 0 {
		c.Interval = d.Interval
	}
	if c.Timeout <= 0 {
		c.Timeout = d.Timeout
	}
	if c.PauseBetween <= 0 {
		c.PauseBetween = d.PauseBetween
	}
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
}

// maxTranscriptBytes is the maximum transcript size sent to the LLM.
const maxTranscriptBytes = 8000

// Worker periodically scans for unsummarized sessions and generates
// metadata using an LLM via the model router.
type Worker struct {
	store     *memory.ArchiveStore
	llmClient llm.Client
	router    *router.Router
	logger    *slog.Logger
	config    Config
	startTime time.Time // process start time — sessions older than this are orphan candidates

	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a summarizer worker. Call Start to begin processing.
func New(store *memory.ArchiveStore, llmClient llm.Client, rtr *router.Router, logger *slog.Logger, cfg Config) *Worker {
	cfg.applyDefaults()
	return &Worker{
		store:     store,
		llmClient: llmClient,
		router:    rtr,
		logger:    logger.With("component", "summarizer"),
		config:    cfg,
		startTime: time.Now().UTC(),
		done:      make(chan struct{}),
	}
}

// Start begins the background summarization worker. It performs an
// immediate scan on startup (to catch up on missed sessions), then
// scans periodically at the configured interval.
func (w *Worker) Start(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(workerCtx)
}

// Stop cancels the worker and waits for its goroutine to exit.
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)

	// Phase 0: close orphaned sessions from prior crashes.
	// If the process was killed (SIGKILL, OOM, panic), EndSession never
	// ran and ended_at stays NULL. Close any session started before this
	// process so it becomes eligible for summarization.
	closed, err := w.store.CloseOrphanedSessions(w.startTime)
	if err != nil {
		w.logger.Error("failed to close orphaned sessions", "error", err)
	} else if closed > 0 {
		w.logger.Warn("closed orphaned sessions from prior crash",
			"count", closed,
		)
	}

	// Phase 1: startup catch-up scan.
	w.logger.Info("summarizer starting, scanning for unsummarized sessions")
	w.scan(ctx)

	// Phase 2: periodic tick.
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("summarizer stopped")
			return
		case <-ticker.C:
			w.scan(ctx)
		}
	}
}

func (w *Worker) scan(ctx context.Context) {
	sessions, err := w.store.UnsummarizedSessions(w.config.BatchSize)
	if err != nil {
		w.logger.Error("failed to query unsummarized sessions", "error", err)
		return
	}
	if len(sessions) == 0 {
		return
	}

	w.logger.Info("found unsummarized sessions", "count", len(sessions))

	for _, sess := range sessions {
		if ctx.Err() != nil {
			return
		}
		w.summarizeSession(ctx, sess)

		// Rate-limit: pause between sessions to avoid starving
		// interactive requests.
		if !sleepCtx(ctx, w.config.PauseBetween) {
			return
		}
	}
}

func (w *Worker) summarizeSession(ctx context.Context, sess *memory.Session) {
	ctx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()

	// Route model selection through the router.
	hints := map[string]string{
		router.HintMission:      "background",
		router.HintLocalOnly:    "true",
		router.HintQualityFloor: "7",
	}
	if w.config.ModelPreference != "" {
		hints[router.HintModelPreference] = w.config.ModelPreference
	}

	model, _ := w.router.Route(ctx, router.Request{
		Query:    "session metadata generation",
		Priority: router.PriorityBackground,
		Hints:    hints,
	})

	// Fetch transcript.
	messages, err := w.store.GetSessionTranscript(sess.ID)
	if err != nil || len(messages) == 0 {
		w.logger.Warn("no transcript for session",
			"session", memory.ShortID(sess.ID),
			"error", err,
		)
		return
	}

	// Fetch tool calls (optional — not fatal if unavailable).
	toolCalls, err := w.store.GetSessionToolCalls(sess.ID)
	if err != nil {
		toolCalls = nil
	}

	// Build condensed transcript for the LLM.
	transcript := buildTranscript(messages)

	// Build tool usage summary.
	toolUsage := make(map[string]int)
	for _, tc := range toolCalls {
		toolUsage[tc.ToolName]++
	}

	prompt := prompts.MetadataPrompt(transcript)
	msgs := []llm.Message{{Role: "user", Content: prompt}}

	resp, err := w.llmClient.Chat(ctx, model, msgs, nil)
	if err != nil {
		w.logger.Warn("failed to generate session metadata",
			"session", memory.ShortID(sess.ID),
			"model", model,
			"error", err,
		)
		return
	}

	meta, title, tags := parseMetadataResponse(resp.Message.Content, toolUsage, w.logger)

	if err := w.store.SetSessionMetadata(sess.ID, meta, title, tags); err != nil {
		w.logger.Warn("failed to save session metadata",
			"session", memory.ShortID(sess.ID),
			"error", err,
		)
		return
	}

	w.logger.Info("session metadata generated",
		"session", memory.ShortID(sess.ID),
		"title", title,
		"model", model,
		"tags", len(tags),
	)
}

// buildTranscript creates a condensed transcript from archived messages,
// truncated at maxTranscriptBytes.
func buildTranscript(messages []memory.ArchivedMessage) string {
	var b strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s] %s: %s\n",
			m.Timestamp.Format("15:04"), m.Role, m.Content))
		if b.Len() > maxTranscriptBytes {
			b.WriteString("\n... (truncated)\n")
			break
		}
	}
	return b.String()
}

// parseMetadataResponse parses the LLM's JSON response into session
// metadata. Falls back to using the raw text as a paragraph summary if
// JSON parsing fails.
func parseMetadataResponse(content string, toolUsage map[string]int, logger *slog.Logger) (*memory.SessionMetadata, string, []string) {
	// Strip markdown code fences if present.
	content = strings.TrimPrefix(content, "```json\n")
	content = strings.TrimPrefix(content, "```\n")
	content = strings.TrimSuffix(content, "\n```")
	content = strings.TrimSpace(content)

	var result struct {
		Title        string   `json:"title"`
		Tags         []string `json:"tags"`
		OneLiner     string   `json:"one_liner"`
		Paragraph    string   `json:"paragraph"`
		Detailed     string   `json:"detailed"`
		KeyDecisions []string `json:"key_decisions"`
		Participants []string `json:"participants"`
		SessionType  string   `json:"session_type"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		logger.Warn("session metadata JSON parse failed, using raw summary",
			"error", err,
		)
		meta := &memory.SessionMetadata{Paragraph: content}
		return meta, "", nil
	}

	meta := &memory.SessionMetadata{
		OneLiner:     result.OneLiner,
		Paragraph:    result.Paragraph,
		Detailed:     result.Detailed,
		KeyDecisions: result.KeyDecisions,
		Participants: result.Participants,
		SessionType:  result.SessionType,
		ToolsUsed:    toolUsage,
	}

	return meta, result.Title, result.Tags
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns true if the
// sleep completed, false if the context was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
