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
