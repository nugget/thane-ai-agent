package media

import (
	"context"
	"fmt"
	"regexp"
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

// sentenceBoundaryRe matches sentence-ending punctuation followed by
// whitespace and an uppercase letter. The split point is after the
// punctuation and whitespace, before the uppercase letter.
var sentenceBoundaryRe = regexp.MustCompile(`[.!?]\s+[A-Z]`)

// chunkTranscript splits a transcript into chunks of approximately
// targetSize characters using a tiered strategy:
//
//  1. Paragraph boundaries (double newlines) — preserves natural topic breaks.
//  2. Sentence boundaries — fallback when paragraphs are absent or oversized.
//  3. Word boundaries — last resort when sentences are also oversized.
//
// A chunk is considered oversized when it exceeds 2× targetSize. When any
// chunk from a tier is oversized, the entire transcript is re-split using
// the next tier down.
func chunkTranscript(transcript string, targetSize int) []string {
	if strings.TrimSpace(transcript) == "" {
		return nil
	}

	// Tier 1: paragraph-boundary splitting.
	chunks := splitOnParagraphs(transcript, targetSize)
	if !hasOversizedChunk(chunks, targetSize*2) {
		return chunks
	}

	// Tier 2: sentence-boundary splitting.
	chunks = splitOnSentences(transcript, targetSize)
	if !hasOversizedChunk(chunks, targetSize*2) {
		return chunks
	}

	// Tier 3: word-boundary hard splitting.
	return splitOnWords(transcript, targetSize)
}

// splitOnParagraphs splits transcript at double-newline boundaries,
// accumulating paragraphs into chunks of approximately targetSize
// characters. A single paragraph exceeding targetSize becomes its own
// chunk without further sub-splitting.
func splitOnParagraphs(transcript string, targetSize int) []string {
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

// splitOnSentences splits transcript at sentence boundaries detected by
// sentenceBoundaryRe. Sentences are accumulated into chunks of
// approximately targetSize characters. When no sentence boundaries are
// found, returns the entire transcript as a single chunk.
func splitOnSentences(transcript string, targetSize int) []string {
	// Find all sentence boundary positions. Each match spans the
	// punctuation, whitespace, and first uppercase letter. The split
	// point is just before the uppercase letter (end of match - 1).
	locs := sentenceBoundaryRe.FindAllStringIndex(transcript, -1)
	if len(locs) == 0 {
		return []string{strings.TrimSpace(transcript)}
	}

	// Build a list of sentences by splitting at boundary positions.
	var sentences []string
	prev := 0
	for _, loc := range locs {
		// Split just before the uppercase letter that starts the next sentence.
		boundary := loc[1] - 1
		s := strings.TrimSpace(transcript[prev:boundary])
		if s != "" {
			sentences = append(sentences, s)
		}
		prev = boundary
	}
	// Trailing text after the last boundary.
	if tail := strings.TrimSpace(transcript[prev:]); tail != "" {
		sentences = append(sentences, tail)
	}

	// Accumulate sentences into chunks.
	var chunks []string
	var current strings.Builder

	for _, s := range sentences {
		if current.Len() > 0 && current.Len()+len(s)+1 > targetSize {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(s)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// splitOnWords splits transcript at word boundaries, guaranteeing that
// no chunk exceeds targetSize characters (unless a single word does).
func splitOnWords(transcript string, targetSize int) []string {
	words := strings.Fields(transcript)
	if len(words) == 0 {
		return nil
	}

	var chunks []string
	var current strings.Builder

	for _, w := range words {
		if current.Len() > 0 && current.Len()+len(w)+1 > targetSize {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(w)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

// hasOversizedChunk reports whether any chunk exceeds the given limit.
func hasOversizedChunk(chunks []string, limit int) bool {
	for _, c := range chunks {
		if len(c) > limit {
			return true
		}
	}
	return false
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
