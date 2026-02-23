package prompts

import (
	"strings"
	"testing"
)

func TestTranscriptChunkSummaryPrompt(t *testing.T) {
	tests := []struct {
		name        string
		chunk       string
		focus       string
		chunkIndex  int
		totalChunks int
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "without focus",
			chunk:       "some transcript text",
			focus:       "",
			chunkIndex:  1,
			totalChunks: 3,
			wantContain: []string{
				"part 1 of 3",
				"some transcript text",
				"key points",
			},
			wantAbsent: []string{"Focus on:"},
		},
		{
			name:        "with focus",
			chunk:       "some transcript text",
			focus:       "benchmark methodology",
			chunkIndex:  2,
			totalChunks: 5,
			wantContain: []string{
				"part 2 of 5",
				"some transcript text",
				"Focus on: benchmark methodology",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranscriptChunkSummaryPrompt(tt.chunk, tt.focus, tt.chunkIndex, tt.totalChunks)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("prompt missing %q", want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("prompt should not contain %q", absent)
				}
			}
		})
	}
}

func TestTranscriptReducePrompt(t *testing.T) {
	tests := []struct {
		name        string
		summaries   string
		focus       string
		detail      string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:      "summary without focus",
			summaries: "chunk 1 summary\n---\nchunk 2 summary",
			focus:     "",
			detail:    "summary",
			wantContain: []string{
				"chunk 1 summary",
				"2000-3000 characters",
			},
			wantAbsent: []string{"Focus on:", "500 characters"},
		},
		{
			name:      "brief with focus",
			summaries: "chunk 1 summary",
			focus:     "travel logistics",
			detail:    "brief",
			wantContain: []string{
				"chunk 1 summary",
				"Focus on: travel logistics",
				"500 characters",
			},
			wantAbsent: []string{"2000-3000 characters"},
		},
		{
			name:      "summary with focus",
			summaries: "summary text",
			focus:     "GPU benchmarks",
			detail:    "summary",
			wantContain: []string{
				"Focus on: GPU benchmarks",
				"2000-3000 characters",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TranscriptReducePrompt(tt.summaries, tt.focus, tt.detail)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("prompt missing %q", want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("prompt should not contain %q", absent)
				}
			}
		})
	}
}
