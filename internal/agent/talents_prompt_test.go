package agent

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
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

func TestBuildSystemPrompt_EntryPointTalentsPrecedeTaggedDoctrine(t *testing.T) {
	l := newTagTestLoop()
	parsed := []talents.Talent{
		{Name: "readme", Tags: nil, Content: "CORE_MARKER"},
		{Name: "interactive-entry-point", Tags: []string{"interactive"}, Kind: "entry_point", Content: "INTERACTIVE_ENTRY_MARKER"},
		{Name: "interactive-communication", Tags: []string{"interactive"}, Content: "INTERACTIVE_COMM_MARKER"},
		{Name: "interactive-doctrine", Tags: []string{"interactive"}, Content: "INTERACTIVE_DOCTRINE_MARKER"},
	}
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"interactive": {Description: "Interactive", AlwaysActive: true},
	}, parsed)

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
	coreIdx := strings.Index(prompt, "CORE_MARKER")
	entryIdx := strings.Index(prompt, "INTERACTIVE_ENTRY_MARKER")
	commIdx := strings.Index(prompt, "INTERACTIVE_COMM_MARKER")
	doctrineIdx := strings.Index(prompt, "INTERACTIVE_DOCTRINE_MARKER")
	if coreIdx < 0 || entryIdx < 0 || commIdx < 0 || doctrineIdx < 0 {
		t.Fatalf("prompt missing expected markers:\n%s", prompt)
	}
	if coreIdx >= entryIdx || entryIdx >= commIdx || entryIdx >= doctrineIdx {
		t.Fatalf("unexpected ordering:\n%s", prompt)
	}
}

func TestBuildSystemPromptWithProfileSections_SplitsCacheableBehaviorPrefix(t *testing.T) {
	l := newTagTestLoop()
	l.persona = "PERSONA_MARKER"
	parsed := []talents.Talent{
		{Name: "readme", Tags: nil, Content: "CORE_MARKER"},
		{Name: "interactive-entry-point", Tags: []string{"interactive"}, Kind: "entry_point", Content: "INTERACTIVE_ENTRY_MARKER"},
		{Name: "interactive-doctrine", Tags: []string{"interactive"}, Content: "INTERACTIVE_DOCTRINE_MARKER"},
	}
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"interactive": {Description: "Interactive", AlwaysActive: true},
	}, parsed)

	prompt, sections := l.buildSystemPromptWithProfileSections(
		testCtxForLoop(l),
		"hello",
		nil,
		llm.DefaultModelInteractionProfile(),
	)

	if !strings.Contains(prompt, "CORE_MARKER") || !strings.Contains(prompt, "INTERACTIVE_ENTRY_MARKER") {
		t.Fatalf("prompt missing expected markers:\n%s", prompt)
	}

	indexByName := make(map[string]int, len(sections))
	sectionByName := make(map[string]llm.PromptSection, len(sections))
	for i, section := range sections {
		indexByName[section.Name] = i
		sectionByName[section.Name] = section
	}

	if got := sectionByName["PERSONA"].CacheTTL; got != "1h" {
		t.Fatalf("PERSONA CacheTTL = %q, want 1h", got)
	}
	if got := sectionByName["TALENTS ALWAYS ON"].CacheTTL; got != "1h" {
		t.Fatalf("TALENTS ALWAYS ON CacheTTL = %q, want 1h", got)
	}
	if got := sectionByName["TALENTS TAGGED"].CacheTTL; got != "5m" {
		t.Fatalf("TALENTS TAGGED CacheTTL = %q, want 5m", got)
	}
	if got := sectionByName["CURRENT CONDITIONS"].CacheTTL; got != "" {
		t.Fatalf("CURRENT CONDITIONS CacheTTL = %q, want empty", got)
	}
	if indexByName["TALENTS ALWAYS ON"] >= indexByName["TALENTS TAGGED"] ||
		indexByName["TALENTS TAGGED"] >= indexByName["CURRENT CONDITIONS"] {
		t.Fatalf("unexpected cacheable-prefix ordering: %#v", indexByName)
	}
}
