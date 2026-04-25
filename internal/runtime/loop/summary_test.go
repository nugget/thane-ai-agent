package loop

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

func TestIterationSummary_NilOnPlainContext(t *testing.T) {
	t.Parallel()
	if got := IterationSummary(context.Background()); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestIterationSummary_ReturnsInjectedMap(t *testing.T) {
	t.Parallel()
	m := map[string]any{"foo": 42}
	ctx := context.WithValue(context.Background(), iterSummaryKey{}, m)

	got := IterationSummary(ctx)
	if got == nil {
		t.Fatal("expected non-nil map")
	}
	if got["foo"] != 42 {
		t.Fatalf("expected foo=42, got %v", got["foo"])
	}
}

func TestReportAgentRun_PopulatesSummary(t *testing.T) {
	t.Parallel()
	m := map[string]any{}
	ctx := context.WithValue(context.Background(), iterSummaryKey{}, m)

	got := ReportAgentRun(ctx, AgentRunSummary{
		RequestID:      "req-123",
		Model:          "claude-3-opus",
		InputTokens:    500,
		OutputTokens:   200,
		ActiveTags:     []string{"forge", "ha"},
		EffectiveTools: []string{"get_state", "forge_issue_list"},
		LoadedCapabilities: []toolcatalog.LoadedCapabilityEntry{
			{Tag: "forge", Description: "Forge tools", ToolCount: 8},
		},
	})
	if got == nil {
		t.Fatal("expected non-nil map")
	}
	if got["request_id"] != "req-123" {
		t.Errorf("request_id = %v, want req-123", got["request_id"])
	}
	if got["model"] != "claude-3-opus" {
		t.Errorf("model = %v, want claude-3-opus", got["model"])
	}
	if got["input_tokens"] != 500 {
		t.Errorf("input_tokens = %v, want 500", got["input_tokens"])
	}
	if got["output_tokens"] != 200 {
		t.Errorf("output_tokens = %v, want 200", got["output_tokens"])
	}
	if active, ok := got["active_tags"].([]string); !ok || len(active) != 2 || active[0] != "forge" || active[1] != "ha" {
		t.Errorf("active_tags = %#v, want [forge ha]", got["active_tags"])
	}
	if tools, ok := got["effective_tools"].([]string); !ok || len(tools) != 2 || tools[0] != "get_state" || tools[1] != "forge_issue_list" {
		t.Errorf("effective_tools = %#v, want [get_state forge_issue_list]", got["effective_tools"])
	}
	if caps, ok := got["loaded_capabilities"].([]toolcatalog.LoadedCapabilityEntry); !ok || len(caps) != 1 || caps[0].Tag != "forge" {
		t.Errorf("loaded_capabilities = %#v, want forge entry", got["loaded_capabilities"])
	}
}

func TestReportAgentRun_NilOnPlainContext(t *testing.T) {
	t.Parallel()
	got := ReportAgentRun(context.Background(), AgentRunSummary{RequestID: "req-456"})
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestReportAgentRun_ChainableCustomFields(t *testing.T) {
	t.Parallel()
	m := map[string]any{}
	ctx := context.WithValue(context.Background(), iterSummaryKey{}, m)

	summary := ReportAgentRun(ctx, AgentRunSummary{RequestID: "req-789"})
	summary["sender"] = "+15551234567"
	summary["message_len"] = 42

	if m["sender"] != "+15551234567" {
		t.Errorf("custom field sender not set")
	}
	if m["message_len"] != 42 {
		t.Errorf("custom field message_len not set")
	}
}
