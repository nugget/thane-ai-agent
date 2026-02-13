package memory

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// ExtractionResult is the structured JSON response from an LLM fact
// extraction call. WorthPersisting acts as a top-level gate: when false,
// the Facts slice is ignored even if populated.
type ExtractionResult struct {
	Facts           []ExtractedFact `json:"facts"`
	WorthPersisting bool            `json:"worth_persisting"`
}

// ExtractedFact is a single fact parsed from the LLM extraction response.
// Category must be one of the valid fact categories (user, home, device,
// routine, preference, architecture). Confidence is 0–1.
type ExtractedFact struct {
	Category   string  `json:"category"`
	Key        string  `json:"key"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// ExtractFunc calls an LLM to extract facts from a single interaction.
// It receives the current user message, assistant response, and recent
// conversation history for context.
type ExtractFunc func(ctx context.Context, userMessage, assistantResponse string, recentHistory []Message) (*ExtractionResult, error)

// FactSetter persists extracted facts to long-term storage. Implementations
// may apply additional logic such as confidence reinforcement on upsert.
type FactSetter interface {
	SetFact(category, key, value, source string, confidence float64) error
}

// Extractor runs automatic fact extraction after each interaction.
// It is fully async and best-effort — failures are logged but never
// propagate to the caller or affect the user-facing response.
type Extractor struct {
	facts       FactSetter
	extract     ExtractFunc
	logger      *slog.Logger
	minMessages int
	timeout     time.Duration
}

// NewExtractor creates an Extractor that persists facts via the given
// FactSetter. The minMessages threshold controls the minimum conversation
// length before extraction is attempted.
func NewExtractor(facts FactSetter, logger *slog.Logger, minMessages int) *Extractor {
	return &Extractor{
		facts:       facts,
		logger:      logger,
		minMessages: minMessages,
		timeout:     30 * time.Second,
	}
}

// SetTimeout configures the LLM call timeout for extraction.
func (e *Extractor) SetTimeout(d time.Duration) {
	e.timeout = d
}

// Timeout returns the configured extraction timeout.
func (e *Extractor) Timeout() time.Duration {
	return e.timeout
}

// SetExtractFunc configures the LLM extraction function.
func (e *Extractor) SetExtractFunc(fn ExtractFunc) {
	e.extract = fn
}

// ShouldExtract reports whether the given interaction is worth analyzing
// for facts. It filters out simple device commands, short responses, and
// auxiliary requests to keep LLM extraction calls to roughly 30–50% of
// interactions.
func (e *Extractor) ShouldExtract(userMsg, assistantResp string, messageCount int, skipContext bool) bool {
	// Auxiliary OWU requests (title/tag gen) never contain facts.
	if skipContext {
		e.logger.Debug("extraction skipped: auxiliary request")
		return false
	}

	// Very short conversations have no context to extract from.
	if messageCount < e.minMessages {
		e.logger.Debug("extraction skipped: too few messages",
			"count", messageCount, "min", e.minMessages)
		return false
	}

	// Error responses or bare confirmations ("Done.", "OK") aren't useful.
	if len(assistantResp) < 20 {
		e.logger.Debug("extraction skipped: short response",
			"len", len(assistantResp))
		return false
	}

	// Simple device commands rarely produce extractable facts.
	if isSimpleCommand(strings.ToLower(userMsg)) {
		preview := userMsg
		if len(preview) > 50 {
			preview = preview[:50]
		}
		e.logger.Debug("extraction skipped: simple command",
			"msg", preview)
		return false
	}

	e.logger.Debug("extraction gate passed",
		"msg_len", len(userMsg), "resp_len", len(assistantResp),
		"messages", messageCount)
	return true
}

// isSimpleCommand detects short device control and status queries that
// are unlikely to contain facts worth persisting.
func isSimpleCommand(lower string) bool {
	// Very short messages are usually commands.
	if len(lower) < 5 {
		return true
	}

	commandPrefixes := []string{
		"turn on ", "turn off ",
		"switch on ", "switch off ",
		"set the ", "set my ",
		"what time", "what's the time",
		"lock the ", "unlock the ",
		"open the ", "close the ",
	}
	for _, prefix := range commandPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return false
}

// Extract calls the configured ExtractFunc and persists any discovered
// facts via the FactSetter. Incomplete facts (missing category, key, or
// value) are silently skipped. Errors from individual SetFact calls are
// logged but do not stop processing of remaining facts.
func (e *Extractor) Extract(ctx context.Context, userMsg, assistantResp string, recentHistory []Message) error {
	if e.extract == nil {
		return nil
	}

	result, err := e.extract(ctx, userMsg, assistantResp, recentHistory)
	if err != nil {
		e.logger.Warn("fact extraction LLM call failed", "error", err)
		return err
	}

	if result == nil || !result.WorthPersisting || len(result.Facts) == 0 {
		e.logger.Debug("extraction found no facts worth persisting")
		return nil
	}

	persisted := 0
	for _, fact := range result.Facts {
		if fact.Category == "" || fact.Key == "" || fact.Value == "" {
			e.logger.Debug("skipping incomplete extracted fact",
				"category", fact.Category, "key", fact.Key)
			continue
		}

		if err := e.facts.SetFact(fact.Category, fact.Key, fact.Value, "auto-extraction", fact.Confidence); err != nil {
			e.logger.Warn("failed to persist extracted fact",
				"category", fact.Category, "key", fact.Key, "error", err)
			continue
		}

		e.logger.Debug("persisted extracted fact",
			"category", fact.Category, "key", fact.Key,
			"value", fact.Value, "confidence", fact.Confidence,
			"source", "auto-extraction")
		persisted++
	}

	if persisted > 0 {
		e.logger.Info("extracted facts from conversation",
			"count", persisted, "total_extracted", len(result.Facts),
			"source", "auto-extraction")
	}

	return nil
}
