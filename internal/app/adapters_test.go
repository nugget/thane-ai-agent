package app

import (
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

func TestCompileLoopAgentRequest(t *testing.T) {
	req := looppkg.Request{
		Model:          "spark/gpt-oss:20b",
		ConversationID: "conv-123",
		Messages: []looppkg.Message{
			{Role: "system", Content: "stay focused"},
			{Role: "user", Content: "summarize this"},
		},
		SkipContext:     true,
		AllowedTools:    []string{"alpha", "beta"},
		ExcludeTools:    []string{"gamma"},
		SkipTagFilter:   true,
		Hints:           map[string]string{"mission": "automation"},
		SeedTags:        []string{"monitoring"},
		MaxIterations:   7,
		MaxOutputTokens: 321,
		ToolTimeout:     2 * time.Second,
		UsageRole:       "delegate",
		UsageTaskName:   "spec-probe",
		SystemPrompt:    "custom prompt",
	}

	got := compileLoopAgentRequest(req)
	if got.Model != req.Model {
		t.Fatalf("Model = %q, want %q", got.Model, req.Model)
	}
	if got.ConversationID != req.ConversationID {
		t.Fatalf("ConversationID = %q, want %q", got.ConversationID, req.ConversationID)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Content != "summarize this" {
		t.Fatalf("Messages = %#v", got.Messages)
	}
	if !got.SkipContext || !got.SkipTagFilter {
		t.Fatalf("Skip flags = %#v", got)
	}
	if got.MaxIterations != 7 || got.MaxOutputTokens != 321 {
		t.Fatalf("Iteration/output limits = %#v", got)
	}
	if got.ToolTimeout != 2*time.Second {
		t.Fatalf("ToolTimeout = %v", got.ToolTimeout)
	}
	if got.UsageRole != "delegate" || got.UsageTaskName != "spec-probe" {
		t.Fatalf("Usage fields = role %q task %q", got.UsageRole, got.UsageTaskName)
	}
	if got.SystemPrompt != "custom prompt" {
		t.Fatalf("SystemPrompt = %q", got.SystemPrompt)
	}

	got.AllowedTools[0] = "changed"
	got.ExcludeTools[0] = "changed"
	got.Hints["mission"] = "changed"
	got.SeedTags[0] = "changed"

	if req.AllowedTools[0] != "alpha" {
		t.Fatalf("AllowedTools mutated = %#v", req.AllowedTools)
	}
	if req.ExcludeTools[0] != "gamma" {
		t.Fatalf("ExcludeTools mutated = %#v", req.ExcludeTools)
	}
	if req.Hints["mission"] != "automation" {
		t.Fatalf("Hints mutated = %#v", req.Hints)
	}
	if req.SeedTags[0] != "monitoring" {
		t.Fatalf("SeedTags mutated = %#v", req.SeedTags)
	}
}
