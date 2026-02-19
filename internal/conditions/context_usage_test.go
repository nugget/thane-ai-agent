package conditions

import (
	"strings"
	"testing"
	"time"
)

func TestFormatContextUsage(t *testing.T) {
	// Fixed reference time for deterministic session duration.
	sessionStart := time.Now().Add(-47 * time.Minute)

	tests := []struct {
		name     string
		info     ContextUsageInfo
		contains []string // substrings that must appear
		excludes []string // substrings that must NOT appear
	}{
		{
			name: "full info routed",
			info: ContextUsageInfo{
				Model:           "claude-opus-4-20250514",
				Routed:          true,
				TokenCount:      31200,
				ContextWindow:   200000,
				MessageCount:    34,
				SessionStart:    sessionStart,
				CompactionCount: 0,
			},
			contains: []string{
				"**Context:**",
				"claude-opus-4-20250514 (routed)",
				"31,200/200,000 tokens",
				"15.6%",
				"34 msgs",
				"session 47m",
				"no compaction",
			},
		},
		{
			name: "not routed",
			info: ContextUsageInfo{
				Model:         "qwen2.5:72b",
				Routed:        false,
				TokenCount:    8000,
				ContextWindow: 32768,
				MessageCount:  12,
			},
			contains: []string{"qwen2.5:72b"},
			excludes: []string{"(routed)"},
		},
		{
			name: "zero context window omits tokens",
			info: ContextUsageInfo{
				Model:        "test-model",
				TokenCount:   500,
				MessageCount: 5,
			},
			contains: []string{"test-model", "5 msgs", "no compaction"},
			excludes: []string{"tokens"},
		},
		{
			name: "zero session start omits session",
			info: ContextUsageInfo{
				Model:         "test-model",
				ContextWindow: 100000,
				MessageCount:  10,
			},
			contains: []string{"test-model"},
			excludes: []string{"session"},
		},
		{
			name: "single compaction",
			info: ContextUsageInfo{
				Model:           "test-model",
				ContextWindow:   100000,
				CompactionCount: 1,
			},
			contains: []string{"1 compaction"},
			excludes: []string{"no compaction", "compactions"},
		},
		{
			name: "multiple compactions",
			info: ContextUsageInfo{
				Model:           "test-model",
				ContextWindow:   100000,
				CompactionCount: 3,
			},
			contains: []string{"3 compactions"},
			excludes: []string{"no compaction"},
		},
		{
			name: "large token numbers",
			info: ContextUsageInfo{
				Model:         "test-model",
				TokenCount:    1234567,
				ContextWindow: 2000000,
			},
			contains: []string{"1,234,567/2,000,000 tokens"},
		},
		{
			name: "empty model",
			info: ContextUsageInfo{
				ContextWindow: 100000,
				TokenCount:    5000,
				MessageCount:  8,
			},
			contains: []string{"5,000/100,000 tokens", "8 msgs", "no compaction"},
			excludes: []string{"(routed)"},
		},
		{
			name: "zero messages omits msg count",
			info: ContextUsageInfo{
				Model:         "test-model",
				ContextWindow: 100000,
			},
			contains: []string{"test-model"},
			excludes: []string{"msgs"},
		},
		{
			name: "high utilization percentage",
			info: ContextUsageInfo{
				Model:         "test-model",
				TokenCount:    95000,
				ContextWindow: 100000,
			},
			contains: []string{"95.0%"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatContextUsage(tt.info)

			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected output to contain %q\ngot: %s", want, got)
				}
			}
			for _, reject := range tt.excludes {
				if strings.Contains(got, reject) {
					t.Errorf("expected output to NOT contain %q\ngot: %s", reject, got)
				}
			}
		})
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{31200, "31,200"},
		{200000, "200,000"},
		{1234567, "1,234,567"},
		{1000000000, "1,000,000,000"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatNumber(tt.input); got != tt.want {
				t.Errorf("formatNumber(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
