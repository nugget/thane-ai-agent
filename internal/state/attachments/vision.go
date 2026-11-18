package attachments

import (
	"context"
	"encoding/base64"
	"errors"
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
	// images are refused with [ErrImageTooLarge] to prevent OOM on
	// very large attachments.
	maxVisionImageSize = 20 << 20 // 20 MiB
)

// Every analysis of an image either returns a description or an error;
// none returns an empty description with a nil error, so a caller can
// always say why an image has no description. Match these classes with
// errors.Is. [VisionFailureReason] turns any analysis error into words a
// model can read.
var (
	// ErrImageTooLarge marks an image over the size vision analysis
	// accepts. The vision model was not called.
	ErrImageTooLarge = errors.New("image too large for vision analysis")

	// ErrNoDescription marks a vision model that answered with no
	// description.
	ErrNoDescription = errors.New("vision model returned no description")

	// ErrVisionCall marks a failed call to the vision model. The
	// provider's own error stays in the chain for
	// [llm.ClassifyCallFailure] and for logs.
	ErrVisionCall = errors.New("vision model call failed")
)

// VisionFailureReason states, for a model to read, why an analysis that
// returned err produced no description. It is built from err's class,
// never from its text, which can carry the stored file's path and the
// provider's raw response.
func VisionFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrImageTooLarge):
		return "the image is over the 20 MiB limit for vision analysis"
	case errors.Is(err, ErrNoDescription):
		return "the vision model returned an empty description"
	case errors.Is(err, ErrVisionCall):
		return "the vision model call failed (" + llm.ClassifyCallFailure(err).Phrase() + ")"
	}
	return "the stored image could not be read for analysis"
}

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
// image. For an image it returns a non-empty description or an error
// classed by [ErrImageTooLarge], [ErrNoDescription] or [ErrVisionCall].
// Results are cached in the attachment store: subsequent calls for the
// same record (or any record with the same content hash) return the
// cached description without calling the LLM.
func (a *Analyzer) Analyze(ctx context.Context, rec *Record) (string, error) {
	// Only analyze images.
	if !strings.HasPrefix(rec.ContentType, "image/") {
		return "", nil
	}

	// Cache hit: this record was already analyzed.
	if !rec.AnalyzedAt.IsZero() && rec.Description != "" {
		return rec.Description, nil
	}

	// Hash-based reuse: another record with the same content was
	// already analyzed (dedup hit from a different sender/channel).
	if desc, model, ok := a.store.VisionByHash(ctx, rec.Hash); ok && desc != "" {
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
		return "", fmt.Errorf("vision: image %s is %d bytes, over the %d-byte limit: %w", rec.ID, rec.Size, maxVisionImageSize, ErrImageTooLarge)
	}

	// Read and base64-encode the image file.
	absPath := a.store.AbsPath(rec)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("vision: read image %s: %w", absPath, err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	// Call the vision model with a timeout.
	analyzeCtx, cancel := context.WithTimeout(withAnalysisAttribution(ctx, rec), a.timeout)
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
		return "", llm.WithErrorClass(ErrVisionCall, fmt.Errorf("vision: analyze %s: %w", rec.ID, err))
	}

	description := strings.TrimSpace(resp.Message.Content)
	if description == "" {
		return "", fmt.Errorf("vision: analyze %s: %w", rec.ID, ErrNoDescription)
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
		return "", fmt.Errorf("vision: image %s is %d bytes, over the %d-byte limit: %w", rec.ID, rec.Size, maxVisionImageSize, ErrImageTooLarge)
	}

	absPath := a.store.AbsPath(rec)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("vision: read image %s: %w", absPath, err)
	}
	b64 := base64.StdEncoding.EncodeToString(data)

	analyzeCtx, cancel := context.WithTimeout(withAnalysisAttribution(ctx, rec), a.timeout)
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
		return "", llm.WithErrorClass(ErrVisionCall, fmt.Errorf("vision: reanalyze %s: %w", rec.ID, err))
	}

	description := strings.TrimSpace(resp.Message.Content)
	if description == "" {
		return "", fmt.Errorf("vision: reanalyze %s: %w", rec.ID, ErrNoDescription)
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

// On-demand reanalysis belongs to its caller; pre-turn ingestion may only know
// the attachment's conversation. Do not substitute the source conversation for
// an already-attributed caller analyzing an older attachment.
func withAnalysisAttribution(ctx context.Context, rec *Record) context.Context {
	attribution := llm.AttributionFromContext(ctx)
	if attribution.ConversationID == "" {
		attribution.ConversationID = rec.ConversationID
		ctx = llm.WithAttribution(ctx, attribution)
	}
	return ctx
}
