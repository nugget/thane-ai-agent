// Package ingest handles importing documents into the fact store.
package ingest

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/facts"
)

// MarkdownIngester parses markdown documents into facts.
type MarkdownIngester struct {
	store      *facts.Store
	embeddings facts.EmbeddingClient
	source     string
	category   facts.Category
}

// NewMarkdownIngester creates a markdown document ingester.
// Category determines how facts are categorized (e.g., CategoryArchitecture).
func NewMarkdownIngester(store *facts.Store, embeddings facts.EmbeddingClient, source string, category facts.Category) *MarkdownIngester {
	return &MarkdownIngester{
		store:      store,
		embeddings: embeddings,
		source:     source,
		category:   category,
	}
}

// Chunk represents a semantic unit from the document.
type Chunk struct {
	Key     string
	Content string
	Section string
}

// IngestFile reads and processes a markdown file into facts.
func (m *MarkdownIngester) IngestFile(ctx context.Context, path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	chunks := parseMarkdown(file)
	return m.ingestChunks(ctx, chunks)
}

// IngestString processes markdown content from a string.
func (m *MarkdownIngester) IngestString(ctx context.Context, content string) (int, error) {
	chunks := parseMarkdown(strings.NewReader(content))
	return m.ingestChunks(ctx, chunks)
}

func (m *MarkdownIngester) ingestChunks(ctx context.Context, chunks []Chunk) (int, error) {
	// Delete existing facts from this source (enables clean re-imports)
	_ = m.store.DeleteBySource(m.source)

	count := 0
	for _, chunk := range chunks {
		// Store the fact
		fact, err := m.store.Set(
			m.category,
			chunk.Key,
			chunk.Content,
			m.source,
			1.0,
			nil,
		)
		if err != nil {
			continue // Skip failures
		}

		// Generate and store embedding
		if m.embeddings != nil {
			embText := fmt.Sprintf("%s: %s - %s", m.category, chunk.Key, chunk.Content)
			if emb, err := m.embeddings.Generate(ctx, embText); err == nil {
				_ = m.store.SetEmbedding(fact.ID, emb)
			}
		}

		count++
	}

	return count, nil
}

// parseMarkdown extracts semantic chunks from markdown content.
func parseMarkdown(r interface{ Read([]byte) (int, error) }) []Chunk {
	var chunks []Chunk
	scanner := bufio.NewScanner(r)

	var currentH1, currentH2 string
	var currentContent strings.Builder
	var lastKey string

	flushChunk := func() {
		content := strings.TrimSpace(currentContent.String())
		if content != "" && lastKey != "" {
			chunks = append(chunks, Chunk{
				Key:     lastKey,
				Content: content,
				Section: currentH1,
			})
		}
		currentContent.Reset()
	}

	h1Pattern := regexp.MustCompile(`^#\s+(.+)$`)
	h2Pattern := regexp.MustCompile(`^##\s+(.+)$`)
	h3Pattern := regexp.MustCompile(`^###\s+(.+)$`)
	codeBlockPattern := regexp.MustCompile("^```")

	inCodeBlock := false

	for scanner.Scan() {
		line := scanner.Text()

		// Track code blocks
		if codeBlockPattern.MatchString(line) {
			inCodeBlock = !inCodeBlock
			currentContent.WriteString(line + "\n")
			continue
		}

		if inCodeBlock {
			currentContent.WriteString(line + "\n")
			continue
		}

		// Check for headers
		if m := h1Pattern.FindStringSubmatch(line); m != nil {
			flushChunk()
			currentH1 = m[1]
			currentH2 = ""
			lastKey = slugify(currentH1)
			continue
		}

		if m := h2Pattern.FindStringSubmatch(line); m != nil {
			flushChunk()
			currentH2 = m[1]
			if currentH1 != "" {
				lastKey = slugify(currentH1) + "/" + slugify(currentH2)
			} else {
				lastKey = slugify(currentH2)
			}
			continue
		}

		if m := h3Pattern.FindStringSubmatch(line); m != nil {
			flushChunk()
			h3 := m[1]
			if currentH2 != "" {
				lastKey = slugify(currentH1) + "/" + slugify(currentH2) + "/" + slugify(h3)
			} else if currentH1 != "" {
				lastKey = slugify(currentH1) + "/" + slugify(h3)
			} else {
				lastKey = slugify(h3)
			}
			continue
		}

		// Accumulate content
		if line != "" || currentContent.Len() > 0 {
			currentContent.WriteString(line + "\n")
		}
	}

	// Flush final chunk
	flushChunk()

	return chunks
}

// slugify converts a header to a key-friendly format.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
