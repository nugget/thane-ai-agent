package attachments

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
)

const (
	// defaultVisionTimeout bounds individual vision analysis calls.
	defaultVisionTimeout = 30 * time.Second

	// maxVisionImageSize is the maximum file size (20 MiB) that the
	// analyzer will read into memory for base64 encoding. Larger
	// images are skipped with a warning to prevent OOM on very large
	// attachments.
	maxVisionImageSize = 20 << 20 // 20 MiB
)

// Analyzer performs vision analysis on image attachments using an LLM.
// Results are cached in the attachment metadata index so identical
// content is only analyzed once (leveraging content-addressed dedup).
type Analyzer struct {
	store   *Store
	client  llm.Client
	model   string
	prompt  string
	timeout time.Duration
	logger  *slog.Logger
}

// AnalyzerConfig holds the dependencies for an [Analyzer].
type AnalyzerConfig struct {
	Client  llm.Client    // multi-client routes to the correct provider
	Model   string        // vision model name (must be in models.available)
	Prompt  string        // custom analysis prompt; empty uses default
	Timeout time.Duration // per-analysis timeout; 0 uses defaultVisionTimeout
	Logger  *slog.Logger
}

// NewAnalyzer creates an Analyzer that uses the given store and LLM
// client for vision analysis.
func NewAnalyzer(store *Store, cfg AnalyzerConfig) *Analyzer {
	prompt := cfg.Prompt
	if prompt == "" {
		prompt = prompts.DefaultVisionPrompt
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultVisionTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Analyzer{
		store:   store,
		client:  cfg.Client,
		model:   cfg.Model,
		prompt:  prompt,
		timeout: timeout,
		logger:  logger,
	}
}

// Analyze performs vision analysis on an image attachment. It returns
// the description text, or an empty string if the record is not an
// image. Results are cached in the attachment store: subsequent calls
// for the same record (or any record with the same content hash)
// return the cached description without calling the LLM.
func (a *Analyzer) Analyze(ctx context.Context, rec *Record) (string, error) {
	// Only analyze images.
	if !strings.HasPrefix(rec.ContentType, "image/") {
		return "", nil
	}

	// Cache hit: this record was already analyzed.
	if !rec.AnalyzedAt.IsZero() {
		return rec.Description, nil
	}

	// Hash-based reuse: another record with the same content was
	// already analyzed (dedup hit from a different sender/channel).
	if desc, model, ok := a.store.VisionByHash(ctx, rec.Hash); ok {
		if err := a.store.UpdateVision(ctx, rec.ID, desc, model); err != nil {
			a.logger.Warn("vision: failed to copy cached analysis",
				"id", rec.ID,
				"error", err,
			)
		}
		return desc, nil
	}

	// Guard against very large images that would cause high memory
	// pressure from base64 encoding (~1.33× original size in memory).
	if rec.Size > maxVisionImageSize {
		a.logger.Warn("vision: image too large for analysis",
			"id", rec.ID,
			"size", rec.Size,
			"max", maxVisionImageSize,
		)
		return "", nil
	}

	// Read and base64-encode the image file.
	absPath := a.store.AbsPath(rec)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("vision: read image %s: %w", absPath, err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	// Call the vision model with a timeout.
	analyzeCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	msgs := []llm.Message{{
		Role:    "user",
		Content: a.prompt,
		Images: []llm.ImageContent{{
			Data:      b64,
			MediaType: rec.ContentType,
		}},
	}}

	resp, err := a.client.Chat(analyzeCtx, a.model, msgs, nil)
	if err != nil {
		return "", fmt.Errorf("vision: analyze %s: %w", rec.ID, err)
	}

	description := strings.TrimSpace(resp.Message.Content)
	if description == "" {
		return "", nil
	}

	// Cache the result.
	if err := a.store.UpdateVision(ctx, rec.ID, description, a.model); err != nil {
		a.logger.Warn("vision: failed to cache analysis",
			"id", rec.ID,
			"error", err,
		)
	}

	a.logger.Info("vision analysis complete",
		"id", rec.ID,
		"model", a.model,
		"description_len", len(description),
	)

	return description, nil
}

// Reanalyze forces a fresh vision analysis regardless of any cached
// result. Use this to upgrade descriptions when better models become
// available. The model parameter overrides the analyzer's configured
// model; pass an empty string to use the default.
func (a *Analyzer) Reanalyze(ctx context.Context, rec *Record, model string) (string, error) {
	if !strings.HasPrefix(rec.ContentType, "image/") {
		return "", nil
	}

	if model == "" {
		model = a.model
	}

	if rec.Size > maxVisionImageSize {
		return "", fmt.Errorf("vision: image %s too large (%d bytes, max %d)", rec.ID, rec.Size, maxVisionImageSize)
	}

	absPath := a.store.AbsPath(rec)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("vision: read image %s: %w", absPath, err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	analyzeCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	msgs := []llm.Message{{
		Role:    "user",
		Content: a.prompt,
		Images: []llm.ImageContent{{
			Data:      b64,
			MediaType: rec.ContentType,
		}},
	}}

	resp, err := a.client.Chat(analyzeCtx, model, msgs, nil)
	if err != nil {
		return "", fmt.Errorf("vision: reanalyze %s: %w", rec.ID, err)
	}

	description := strings.TrimSpace(resp.Message.Content)
	if description == "" {
		return "", nil
	}

	if err := a.store.UpdateVision(ctx, rec.ID, description, model); err != nil {
		return "", fmt.Errorf("vision: cache reanalysis %s: %w", rec.ID, err)
	}

	a.logger.Info("vision reanalysis complete",
		"id", rec.ID,
		"model", model,
		"description_len", len(description),
	)

	return description, nil
}
