package agent

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/talents"
)

func TestBuildSystemPrompt_TaggedTalentsLoadForActiveTags(t *testing.T) {
	l := newTagTestLoop()
	parsed := []talents.Talent{
		{Name: "knowledge", Tags: []string{"knowledge"}, Content: "KNOWLEDGE_TREE_MARKER"},
		{Name: "files", Tags: []string{"files"}, Content: "FILES_DOCTRINE_MARKER"},
		{Name: "untagged", Tags: nil, Content: "UNTAGGED_MARKER"},
	}
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"knowledge": {Description: "Knowledge", AlwaysActive: true},
		"files":     {Description: "Files", AlwaysActive: false},
	}, parsed)

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "KNOWLEDGE_TREE_MARKER") {
		t.Fatalf("prompt missing knowledge talent: %s", prompt)
	}
	if strings.Contains(prompt, "FILES_DOCTRINE_MARKER") {
		t.Fatalf("prompt should not include inactive files talent: %s", prompt)
	}
	if !strings.Contains(prompt, "UNTAGGED_MARKER") {
		t.Fatalf("prompt missing untagged talent: %s", prompt)
	}
}

func TestBuildSystemPrompt_CommunicationSlicesFollowActiveTags(t *testing.T) {
	l := newTagTestLoop()
	parsed := []talents.Talent{
		{Name: "communication", Tags: nil, Content: "CORE_COMMUNICATION_MARKER"},
		{Name: "interactive-communication", Tags: []string{"interactive"}, Content: "INTERACTIVE_COMMUNICATION_MARKER"},
		{Name: "development-communication", Tags: []string{"development", "forge"}, Content: "DEVELOPMENT_COMMUNICATION_MARKER"},
	}
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"interactive": {Description: "Interactive", AlwaysActive: true},
		"development": {Description: "Development", AlwaysActive: false},
		"forge":       {Description: "Forge", AlwaysActive: false},
	}, parsed)

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "CORE_COMMUNICATION_MARKER") {
		t.Fatalf("prompt missing core communication slice: %s", prompt)
	}
	if !strings.Contains(prompt, "INTERACTIVE_COMMUNICATION_MARKER") {
		t.Fatalf("prompt missing interactive communication slice: %s", prompt)
	}
	if strings.Contains(prompt, "DEVELOPMENT_COMMUNICATION_MARKER") {
		t.Fatalf("prompt should not include development communication slice: %s", prompt)
	}
}
