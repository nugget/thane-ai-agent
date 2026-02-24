package media

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChunkTranscript(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		targetSize int
		wantCount  int
		wantFirst  string // substring in first chunk
	}{
		{
			name:       "empty input",
			transcript: "",
			targetSize: 100,
			wantCount:  0,
		},
		{
			name:       "whitespace only",
			transcript: "   \n\n  \n\n  ",
			targetSize: 100,
			wantCount:  0,
		},
		{
			name:       "single paragraph fits",
			transcript: "Hello world, this is a short transcript.",
			targetSize: 1000,
			wantCount:  1,
			wantFirst:  "Hello world",
		},
		{
			name:       "two paragraphs fit in one chunk",
			transcript: "First paragraph.\n\nSecond paragraph.",
			targetSize: 1000,
			wantCount:  1,
			wantFirst:  "First paragraph",
		},
		{
			name:       "two paragraphs split into two chunks",
			transcript: "First paragraph.\n\nSecond paragraph.",
			targetSize: 20,
			wantCount:  2,
			wantFirst:  "First paragraph.",
		},
		{
			name:       "oversized single paragraph stays intact",
			transcript: strings.Repeat("x", 200),
			targetSize: 50,
			wantCount:  1,
			wantFirst:  "xxxx",
		},
		{
			name:       "multiple paragraphs accumulate then flush",
			transcript: "AAA\n\nBBB\n\nCCC\n\nDDD\n\nEEE",
			targetSize: 10,
			wantCount:  3, // AAA+BBB=9, CCC+DDD=9, EEE=3
			wantFirst:  "AAA",
		},
		{
			name:       "empty paragraphs skipped",
			transcript: "Hello\n\n\n\n\n\nWorld",
			targetSize: 1000,
			wantCount:  1,
			wantFirst:  "Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunkTranscript(tt.transcript, tt.targetSize)
			if len(chunks) != tt.wantCount {
				t.Fatalf("got %d chunks, want %d; chunks: %v", len(chunks), tt.wantCount, chunks)
			}
			if tt.wantFirst != "" && len(chunks) > 0 {
				if !strings.Contains(chunks[0], tt.wantFirst) {
					t.Errorf("first chunk %q missing %q", chunks[0], tt.wantFirst)
				}
			}
		})
	}
}

func TestChunkTranscript_SentenceFallback(t *testing.T) {
	// Dense transcript with no paragraph breaks but clear sentence boundaries.
	// Each sentence is ~50 chars. With targetSize=100, paragraph splitting
	// produces 1 oversized chunk (~250 chars, > 2×100), triggering sentence
	// fallback which should produce ~3 chunks.
	transcript := "The quick brown fox jumped over the lazy dog. " +
		"A second sentence follows the first one here. " +
		"Third sentence has some more content to read. " +
		"Fourth sentence completes the dense transcript. " +
		"Final sentence wraps up the whole thing here."

	chunks := chunkTranscript(transcript, 100)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks from sentence fallback, got %d: %v", len(chunks), chunks)
	}
	for i, c := range chunks {
		if len(c) > 200 { // 2x target
			t.Errorf("chunk[%d] len=%d exceeds 2x target (200)", i, len(c))
		}
	}
	// Verify no content lost.
	joined := strings.Join(chunks, " ")
	if !strings.Contains(joined, "quick brown fox") {
		t.Error("sentence fallback lost content from beginning")
	}
	if !strings.Contains(joined, "wraps up") {
		t.Error("sentence fallback lost content from end")
	}
}

func TestChunkTranscript_WordFallback(t *testing.T) {
	// Transcript with no paragraph breaks and no sentence boundaries
	// (no period/question/exclamation followed by uppercase).
	// Word splitting should produce multiple chunks.
	words := make([]string, 100)
	for i := range words {
		words[i] = "word"
	}
	transcript := strings.Join(words, " ") // 100 words × 5 chars = ~499 chars

	chunks := chunkTranscript(transcript, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks from word fallback, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 100 { // 2x target
			t.Errorf("chunk[%d] len=%d exceeds 2x target (100)", i, len(c))
		}
	}
}

func TestChunkTranscript_DenseTranscript(t *testing.T) {
	// Integration: simulate a 50K char dense transcript with no paragraph
	// breaks. chunkTranscript must produce multiple chunks, none > 2×5000.
	sentence := "This is a typical transcript sentence from a video. "
	repeats := 50000 / len(sentence)
	transcript := strings.Repeat(sentence, repeats)

	chunks := chunkTranscript(transcript, 5000)
	if len(chunks) < 2 {
		t.Fatalf("50K dense transcript should produce multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 10000 {
			t.Errorf("chunk[%d] len=%d exceeds 2x default target (10000)", i, len(c))
		}
	}
	// Verify total content is preserved (within whitespace normalization).
	totalLen := 0
	for _, c := range chunks {
		totalLen += len(c)
	}
	// Account for spaces between chunks that become intra-chunk spaces.
	if totalLen < len(transcript)/2 {
		t.Errorf("chunks total %d chars, expected close to %d", totalLen, len(transcript))
	}
}

func TestSplitOnSentences(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		targetSize int
		wantMin    int // minimum expected chunks
		wantMax    int // maximum expected chunks
	}{
		{
			name:       "no sentence boundaries",
			transcript: "just some words without any ending punctuation",
			targetSize: 100,
			wantMin:    1,
			wantMax:    1,
		},
		{
			name:       "period boundary",
			transcript: "First sentence here. Second sentence here.",
			targetSize: 25,
			wantMin:    2,
			wantMax:    2,
		},
		{
			name:       "question mark boundary",
			transcript: "What is this? Another sentence follows.",
			targetSize: 20,
			wantMin:    2,
			wantMax:    2,
		},
		{
			name:       "exclamation boundary",
			transcript: "Wow that is great! Next sentence here.",
			targetSize: 25,
			wantMin:    2,
			wantMax:    2,
		},
		{
			name:       "multiple sentences accumulate",
			transcript: "One. Two. Three. Four. Five.",
			targetSize: 100,
			wantMin:    1,
			wantMax:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitOnSentences(tt.transcript, tt.targetSize)
			if len(chunks) < tt.wantMin || len(chunks) > tt.wantMax {
				t.Errorf("got %d chunks, want %d-%d; chunks: %v",
					len(chunks), tt.wantMin, tt.wantMax, chunks)
			}
		})
	}
}

func TestSplitOnWords(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		targetSize int
		wantCount  int
	}{
		{
			name:       "empty input",
			transcript: "   ",
			targetSize: 100,
			wantCount:  0,
		},
		{
			name:       "single word",
			transcript: "hello",
			targetSize: 100,
			wantCount:  1,
		},
		{
			name:       "words fit in one chunk",
			transcript: "hello world foo bar",
			targetSize: 100,
			wantCount:  1,
		},
		{
			name:       "words split across chunks",
			transcript: "aaa bbb ccc ddd eee",
			targetSize: 8,
			wantCount:  3, // "aaa bbb"=7, "ccc ddd"=7, "eee"=3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitOnWords(tt.transcript, tt.targetSize)
			if len(chunks) != tt.wantCount {
				t.Fatalf("got %d chunks, want %d; chunks: %v",
					len(chunks), tt.wantCount, chunks)
			}
			// Verify no chunk exceeds target (unless single word).
			for i, c := range chunks {
				if len(c) > tt.targetSize && !strings.Contains(c, " ") {
					continue // single oversized word is OK
				}
				if len(c) > tt.targetSize {
					t.Errorf("chunk[%d] len=%d exceeds target %d: %q",
						i, len(c), tt.targetSize, c)
				}
			}
		})
	}
}

func TestChunkTranscript_PreservesOrder(t *testing.T) {
	transcript := "Para1\n\nPara2\n\nPara3\n\nPara4\n\nPara5"
	chunks := chunkTranscript(transcript, 10)

	for i, chunk := range chunks {
		expected := fmt.Sprintf("Para%d", i+1)
		if !strings.Contains(chunk, expected) {
			t.Errorf("chunk[%d] = %q, want to contain %q", i, chunk, expected)
		}
	}
}

func TestSummarizeTranscript(t *testing.T) {
	var callCount atomic.Int32
	mockSummarize := func(_ context.Context, prompt string) (string, error) {
		callCount.Add(1)
		if strings.Contains(prompt, "Combine these section summaries") {
			return "final combined summary", nil
		}
		// Map phase: return a short summary for each chunk.
		return "chunk summary", nil
	}

	c := &Client{
		cfg:       Config{MaxTranscriptChars: 50000},
		logger:    slog.Default(),
		summarize: mockSummarize,
	}

	// Build a transcript with multiple paragraphs that will be chunked.
	paragraphs := make([]string, 10)
	for i := range paragraphs {
		paragraphs[i] = strings.Repeat(fmt.Sprintf("Paragraph %d content. ", i+1), 100)
	}
	transcript := strings.Join(paragraphs, "\n\n")

	result, err := c.summarizeTranscript(context.Background(), transcript, "", DetailSummary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "final combined summary" {
		t.Errorf("got %q, want %q", result, "final combined summary")
	}

	// At least 2 calls: one or more map + one reduce.
	count := callCount.Load()
	if count < 2 {
		t.Errorf("expected at least 2 LLM calls, got %d", count)
	}
}

func TestSummarizeTranscript_WithFocus(t *testing.T) {
	var receivedPrompts []string
	var mu = &struct{ strings []string }{}
	_ = mu

	mockSummarize := func(_ context.Context, prompt string) (string, error) {
		receivedPrompts = append(receivedPrompts, prompt)
		if strings.Contains(prompt, "Combine") {
			return "focused summary", nil
		}
		return "chunk summary", nil
	}

	c := &Client{
		cfg:       Config{MaxTranscriptChars: 50000},
		logger:    slog.Default(),
		summarize: mockSummarize,
	}

	// Single chunk so we get exactly 1 map + 1 reduce call, deterministic order.
	transcript := "Some transcript about GPU benchmarks and performance testing."
	result, err := c.summarizeTranscript(context.Background(), transcript, "GPU benchmarks", DetailSummary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "focused summary" {
		t.Errorf("got %q, want %q", result, "focused summary")
	}

	// Verify focus appears in both map and reduce prompts.
	for i, p := range receivedPrompts {
		if !strings.Contains(p, "GPU benchmarks") {
			t.Errorf("prompt %d missing focus string", i)
		}
	}
}

func TestSummarizeTranscript_ErrorPropagation(t *testing.T) {
	mockSummarize := func(_ context.Context, prompt string) (string, error) {
		if strings.Contains(prompt, "part 1 of") || strings.Contains(prompt, "part 2 of") {
			return "", fmt.Errorf("LLM unavailable")
		}
		return "ok", nil
	}

	c := &Client{
		cfg:       Config{MaxTranscriptChars: 50000},
		logger:    slog.Default(),
		summarize: mockSummarize,
	}

	transcript := "Para1\n\nPara2"
	_, err := c.summarizeTranscript(context.Background(), transcript, "", DetailSummary)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "map phase") {
		t.Errorf("error %q should mention map phase", err.Error())
	}
}

func TestSummarizeTranscript_NilSummarizer(t *testing.T) {
	c := &Client{
		cfg:    Config{MaxTranscriptChars: 50000},
		logger: slog.Default(),
		// summarize is nil
	}

	_, err := c.summarizeTranscript(context.Background(), "text", "", DetailSummary)
	if err == nil {
		t.Fatal("expected error for nil summarizer")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error %q should mention not configured", err.Error())
	}
}

func TestSummarizeTranscript_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	mockSummarize := func(ctx context.Context, _ string) (string, error) {
		return "", ctx.Err()
	}

	c := &Client{
		cfg:       Config{MaxTranscriptChars: 50000},
		logger:    slog.Default(),
		summarize: mockSummarize,
	}

	_, err := c.summarizeTranscript(ctx, "Some text", "", DetailSummary)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
