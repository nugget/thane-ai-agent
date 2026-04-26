package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// SummarizerConfig controls the summarizer worker behavior.
type SummarizerConfig struct {
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

	// IdleTimeout is the duration of inactivity after which an open
	// session is silently closed by the summarizer. Zero disables
	// idle session closing. The summarizer worker is the sole owner
	// of session idle close — message-channel continuity across the
	// rotation boundary is delivered via the message_channel context
	// provider's verbatim tail, not an LLM-driven carry-forward.
	IdleTimeout time.Duration
}

// DefaultSummarizerConfig returns sensible defaults for the summarizer worker.
func DefaultSummarizerConfig() SummarizerConfig {
	return SummarizerConfig{
		Interval:     5 * time.Minute,
		Timeout:      60 * time.Second,
		PauseBetween: 5 * time.Second,
		BatchSize:    10,
	}
}

func (c *SummarizerConfig) applyDefaults() {
	d := DefaultSummarizerConfig()
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

// InteractionCallback is called after successful session
// summarization to update the contact's last interaction metadata.
// Parameters: conversationID, sessionID, endedAt, topics (tags).
type InteractionCallback func(conversationID string, sessionID string, endedAt time.Time, topics []string)

// SummarizerWorker periodically scans for unsummarized sessions and
// generates metadata (title, tags, summaries) using an LLM via the
// model router.
type SummarizerWorker struct {
	store         *ArchiveStore
	llmClient     llm.Client
	router        *router.Router
	logger        *slog.Logger
	config        SummarizerConfig
	startTime     time.Time // process start time — sessions older than this are orphan candidates
	interactionCB InteractionCallback

	cancel context.CancelFunc
	done   chan struct{}
}

// NewSummarizerWorker creates a summarizer worker. Call Start to begin
// processing.
func NewSummarizerWorker(store *ArchiveStore, llmClient llm.Client, rtr *router.Router, logger *slog.Logger, cfg SummarizerConfig) *SummarizerWorker {
	cfg.applyDefaults()
	return &SummarizerWorker{
		store:     store,
		llmClient: llmClient,
		router:    rtr,
		logger:    logger.With("component", "summarizer"),
		config:    cfg,
		startTime: time.Now().UTC(),
		done:      make(chan struct{}),
	}
}

// SetInteractionCallback registers a callback invoked after each
// successful session summarization. The callback receives the
// conversation ID, session ID, session end time, and LLM-generated
// topic tags, allowing callers to update contact interaction history
// without coupling the memory package to the contacts package.
func (w *SummarizerWorker) SetInteractionCallback(cb InteractionCallback) {
	w.interactionCB = cb
}

// Start begins the background summarization worker. It performs an
// immediate scan on startup (to catch up on missed sessions), then
// scans periodically at the configured interval.
func (w *SummarizerWorker) Start(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(workerCtx)
}

// Stop cancels the worker and waits for its goroutine to exit.
func (w *SummarizerWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *SummarizerWorker) run(ctx context.Context) {
	defer close(w.done)

	// Phase 0: recover unclosed sessions from a prior run.
	// If the process was killed (SIGKILL, OOM, panic), EndSession never
	// ran and ended_at stays NULL. Graceful shutdown can also leave an
	// open session behind if the conversation was still active. Close any
	// session started before this process so it becomes eligible for
	// summarization.
	closed, err := w.store.CloseOrphanedSessions(w.startTime)
	if err != nil {
		w.logger.Error("failed to close orphaned sessions", "error", err)
	} else if closed > 0 {
		w.logger.Info("recovered unclosed sessions from prior run",
			"count", closed,
		)
	}

	// Phase 0.5: close sessions idle beyond the timeout. On startup this
	// catches sessions that went idle while the process was down.
	w.closeIdleSessions(ctx)

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
			w.closeIdleSessions(ctx)
			w.scan(ctx)
		}
	}
}

// closeIdleSessions silently ends any active sessions whose last
// activity is older than the configured idle timeout. Closed sessions
// become eligible for summarization on the next scan cycle. This is
// the sole owner of session idle close — message-channel continuity
// is delivered via the message_channel context provider's verbatim
// tail, not an interactive farewell.
func (w *SummarizerWorker) closeIdleSessions(ctx context.Context) {
	if w.config.IdleTimeout <= 0 {
		return
	}

	sessions, err := w.store.ActiveSessionsWithLastActivity()
	if err != nil {
		w.logger.Error("failed to query active sessions for idle check", "error", err)
		return
	}

	cutoff := time.Now().UTC().Add(-w.config.IdleTimeout)
	for _, s := range sessions {
		if ctx.Err() != nil {
			return
		}
		if s.LastActivity.Before(cutoff) {
			// Stamp session_id on active messages so GetSessionTranscript
			// can find them. Active messages have session_id=NULL until
			// the normal archive flow runs; without this the summarizer
			// would see an empty transcript and mark the session as empty.
			if _, err := w.store.ClaimActiveMessages(s.ConversationID, s.SessionID); err != nil {
				w.logger.Warn("failed to claim messages for idle session, skipping close",
					"session", ShortID(s.SessionID),
					"conversation_id", s.ConversationID,
					"error", err,
				)
				// Don't close the session — unclaimed messages would be
				// orphaned (session_id=NULL) and the summarizer would see
				// an empty transcript. Retry next tick.
				continue
			}

			if err := w.store.EndSession(s.SessionID, "idle_timeout"); err != nil {
				w.logger.Warn("failed to close idle session",
					"session", ShortID(s.SessionID),
					"conversation_id", s.ConversationID,
					"error", err,
				)
				continue
			}
			w.logger.Info("closed idle session (backstop)",
				"session", ShortID(s.SessionID),
				"conversation_id", s.ConversationID,
				"idle_duration", time.Since(s.LastActivity).Round(time.Second),
			)
		}
	}
}

func (w *SummarizerWorker) scan(ctx context.Context) {
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

func (w *SummarizerWorker) summarizeSession(ctx context.Context, sess *Session) {
	ctx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()

	// Fetch transcript before routing a model — avoids wasting a router
	// call on sessions that have no content to summarize.
	messages, err := w.store.GetSessionTranscript(sess.ID)
	if err != nil {
		w.logger.Warn("failed to fetch transcript",
			"session", ShortID(sess.ID),
			"error", err,
		)
		return
	}
	if len(messages) == 0 {
		// Empty-transcript sessions (scheduler tasks, empty delegates)
		// must be marked as summarized so they don't re-enter the queue
		// on every scan cycle.
		w.markEmpty(sess.ID)
		return
	}

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
			"session", ShortID(sess.ID),
			"model", model,
			"error", err,
		)
		return
	}

	meta, title, tags := parseMetadataResponse(resp.Message.Content, toolUsage, w.logger)

	if err := w.store.SetSessionMetadata(sess.ID, meta, title, tags); err != nil {
		w.logger.Warn("failed to save session metadata",
			"session", ShortID(sess.ID),
			"error", err,
		)
		return
	}

	w.logger.Info("session metadata generated",
		"session", ShortID(sess.ID),
		"title", title,
		"model", model,
		"tags", len(tags),
	)

	// Notify the interaction callback so contact history can be updated.
	// Wrap in a deferred recover to prevent callback panics from crashing
	// the summarizer loop.
	if w.interactionCB != nil && sess.EndedAt != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					w.logger.Error("interaction callback panicked",
						"session", ShortID(sess.ID),
						"panic", r,
					)
				}
			}()
			w.interactionCB(sess.ConversationID, sess.ID, *sess.EndedAt, tags)
		}()
	}
}

// markEmpty marks a session with no transcript as summarized so it is
// excluded from future scans. A placeholder title and session type are
// stored so the session is distinguishable from real summaries.
func (w *SummarizerWorker) markEmpty(sessionID string) {
	meta := &SessionMetadata{
		OneLiner:    "Empty session (no transcript)",
		SessionType: "empty",
	}
	if err := w.store.SetSessionMetadata(sessionID, meta, "(empty session)", nil); err != nil {
		w.logger.Warn("failed to mark empty session",
			"session", ShortID(sessionID),
			"error", err,
		)
		return
	}
	w.logger.Info("marked empty session as summarized",
		"session", ShortID(sessionID),
	)
}

// buildTranscript creates a condensed transcript from archived messages,
// truncated at maxTranscriptBytes.
func buildTranscript(messages []Message) string {
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
func parseMetadataResponse(content string, toolUsage map[string]int, logger *slog.Logger) (*SessionMetadata, string, []string) {
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
		meta := &SessionMetadata{Paragraph: content}
		return meta, "", nil
	}

	meta := &SessionMetadata{
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
