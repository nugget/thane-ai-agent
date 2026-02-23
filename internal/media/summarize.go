package media

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/prompts"
)

// SummarizeFunc is a function that sends a prompt to an LLM and returns
// the response text. It decouples the media package from specific LLM and
// router implementations. The caller (typically cmd/thane/main.go) builds
// a closure that handles model selection and provider routing.
type SummarizeFunc func(ctx context.Context, prompt string) (string, error)

// DetailLevel controls how much processing is applied to a transcript
// before it is returned to the caller.
type DetailLevel string

const (
	// DetailFull returns the raw cleaned transcript without summarization.
	DetailFull DetailLevel = "full"

	// DetailSummary produces a map-reduce summary of ~2000-3000 characters.
	DetailSummary DetailLevel = "summary"

	// DetailBrief produces an aggressive summary of ~500 characters.
	DetailBrief DetailLevel = "brief"
)

const (
	// defaultChunkSize is the target character count per chunk during
	// paragraph-boundary splitting. 5K chars is roughly 1.5K tokens,
	// leaving headroom for prompt overhead in models with 4K+ contexts.
	defaultChunkSize = 5000

	// maxParallelChunks limits the number of concurrent LLM calls during
	// the map phase to avoid saturating a local Ollama instance.
	maxParallelChunks = 4
)

// chunkTranscript splits a transcript (with paragraphs separated by double
// newlines) into chunks of approximately targetSize characters. Splits occur
// only at paragraph boundaries — a single paragraph that exceeds targetSize
// becomes its own chunk without further sub-splitting.
func chunkTranscript(transcript string, targetSize int) []string {
	if strings.TrimSpace(transcript) == "" {
		return nil
	}

	paragraphs := strings.Split(transcript, "\n\n")

	var chunks []string
	var current strings.Builder

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// If adding this paragraph would exceed the target and we already
		// have content, flush the current chunk first.
		if current.Len() > 0 && current.Len()+len(p)+2 > targetSize {
			chunks = append(chunks, current.String())
			current.Reset()
		}

		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}

	// Flush any remaining content.
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// summarizeTranscript runs the map-reduce pipeline on the given transcript.
// Each chunk is summarized in parallel (capped at maxParallelChunks), then
// the chunk summaries are combined in a reduce step. The focus string, when
// non-empty, guides both phases to emphasize relevant content.
func (c *Client) summarizeTranscript(ctx context.Context, transcript, focus string, detail DetailLevel) (string, error) {
	if c.summarize == nil {
		return "", fmt.Errorf("summarizer not configured")
	}

	chunks := chunkTranscript(transcript, defaultChunkSize)
	if len(chunks) == 0 {
		return "", fmt.Errorf("no content to summarize")
	}

	c.logger.Info("starting transcript summarization",
		"chunks", len(chunks),
		"detail", string(detail),
		"has_focus", focus != "",
	)

	// Map phase: summarize each chunk in parallel.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	summaries := make([]string, len(chunks))
	sem := make(chan struct{}, maxParallelChunks)
	errs := make(chan error, len(chunks))
	var wg sync.WaitGroup

	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, text string) {
			defer wg.Done()

			// Acquire semaphore slot.
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}

			prompt := prompts.TranscriptChunkSummaryPrompt(text, focus, idx+1, len(chunks))
			result, err := c.summarize(ctx, prompt)
			if err != nil {
				errs <- fmt.Errorf("chunk %d: %w", idx+1, err)
				cancel()
				return
			}
			summaries[idx] = result
		}(i, chunk)
	}

	wg.Wait()
	close(errs)

	// Check for the first error.
	if err := <-errs; err != nil {
		return "", fmt.Errorf("map phase: %w", err)
	}

	// Reduce phase: combine chunk summaries into a final result.
	combined := strings.Join(summaries, "\n\n---\n\n")
	reducePrompt := prompts.TranscriptReducePrompt(combined, focus, string(detail))

	c.logger.Info("running reduce phase",
		"combined_length", len(combined),
		"detail", string(detail),
	)

	result, err := c.summarize(ctx, reducePrompt)
	if err != nil {
		return "", fmt.Errorf("reduce phase: %w", err)
	}

	return result, nil
}
