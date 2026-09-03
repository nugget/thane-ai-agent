package awareness

import (
	"strings"
	"testing"
	"time"
)

func TestFormatContextUsage_ConversationModelPin(t *testing.T) {
	tests := []struct {
		name     string
		info     ContextUsageInfo
		contains []string
		excludes []string
	}{
		{
			name: "honored pin shows the deployment and its age as a delta",
			info: ContextUsageInfo{
				Model:         "claude-opus-4-8",
				ModelPinned:   true,
				ModelPinAge:   2 * time.Minute,
				TokenCount:    1000,
				ContextWindow: 200000,
			},
			contains: []string{"claude-opus-4-8 (pinned -120s)"},
			excludes: []string{"routed", "skipped"},
		},
		{
			name: "honored pin outranks the routed marker",
			info: ContextUsageInfo{
				Model:         "claude-opus-4-8",
				Routed:        true,
				ModelPinned:   true,
				ModelPinAge:   3*time.Hour + 15*time.Minute,
				TokenCount:    1000,
				ContextWindow: 200000,
			},
			contains: []string{"claude-opus-4-8 (pinned -3h15m)"},
			excludes: []string{"(routed)"},
		},
		{
			name: "skipped pin names the pin, the reason, and the routed replacement",
			info: ContextUsageInfo{
				Model:              "claude-sonnet-4-20250514",
				Routed:             true,
				ModelPinSkipped:    "qwen3:8b",
				ModelPinSkipReason: "it does not support image inputs",
				TokenCount:         1000,
				ContextWindow:      200000,
			},
			contains: []string{"claude-sonnet-4-20250514 (routed; pinned qwen3:8b skipped this turn: it does not support image inputs)"},
			excludes: []string{"(routed)"},
		},
		{
			name: "skipped pin without a reason still reads cleanly",
			info: ContextUsageInfo{
				Model:           "claude-sonnet-4-20250514",
				Routed:          true,
				ModelPinSkipped: "qwen3:8b",
				TokenCount:      1000,
				ContextWindow:   200000,
			},
			contains: []string{"(routed; pinned qwen3:8b skipped this turn)"},
		},
		{
			name: "no pin leaves the routed marker alone",
			info: ContextUsageInfo{
				Model:         "qwen3:8b",
				Routed:        true,
				TokenCount:    1000,
				ContextWindow: 32768,
			},
			contains: []string{"qwen3:8b (routed)"},
			excludes: []string{"pinned"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatContextUsage(tc.info)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatContextUsage() = %q, want it to contain %q", got, want)
				}
			}
			for _, bad := range tc.excludes {
				if strings.Contains(got, bad) {
					t.Errorf("FormatContextUsage() = %q, must not contain %q", got, bad)
				}
			}
		})
	}
}

func TestFormatPinAge(t *testing.T) {
	tests := []struct {
		age  time.Duration
		want string
	}{
		{age: 0, want: "-0s"},
		{age: -5 * time.Second, want: "-0s"},
		{age: 45 * time.Second, want: "-45s"},
		{age: 90 * time.Minute, want: "-1h30m"},
	}
	for _, tc := range tests {
		if got := formatPinAge(tc.age); got != tc.want {
			t.Errorf("formatPinAge(%v) = %q, want %q", tc.age, got, tc.want)
		}
	}
}
