package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func TestBuildSystemPromptSections_FullPromptUsesInvertedPyramidOrder(t *testing.T) {
	dir := t.TempDir()
	axiomsPath := filepath.Join(dir, "axioms.md")
	egoPath := filepath.Join(dir, "ego.md")
	injectPath := filepath.Join(dir, "mission.md")
	if err := os.WriteFile(axiomsPath, []byte("AXIOMS_MARKER"), 0o644); err != nil {
		t.Fatalf("write axioms.md: %v", err)
	}
	if err := os.WriteFile(egoPath, []byte("EGO_MARKER"), 0o644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}
	if err := os.WriteFile(injectPath, []byte("INJECT_MARKER"), 0o644); err != nil {
		t.Fatalf("write inject file: %v", err)
	}

	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "forge.md"),
		[]byte("---\ntags: [forge]\n---\nTAGGED_GUIDANCE_MARKER"), 0o644); err != nil {
		t.Fatalf("write kb article: %v", err)
	}

	l := newPromptOrderLoop(t, kbDir)
	l.persona = "PERSONA_MARKER"
	l.ensureCoreContextProvider().updateAxiomsFile(axiomsPath)
	l.SetEgoFile(egoPath)
	l.SetInjectFiles([]string{injectPath})
	l.RegisterTagContextProvider("forge", &mockTagProvider{
		content: "LIVE_STATE_MARKER",
		bucket:  agentctx.ContextBucketLiveState,
	})
	l.RegisterAlwaysContextProvider(&mockTagProvider{
		content: "CONTINUITY_MARKER",
		bucket:  agentctx.ContextBucketContinuity,
	})

	_, sections := l.buildSystemPromptWithProfileSections(
		testCtxForLoop(l),
		"hello",
		nil,
		llm.DefaultModelInteractionProfile(),
	)
	index := promptSectionIndex(t, sections)
	assertPromptSectionsStartAtContent(t, sections)

	assertPromptSectionOrder(t, index,
		"AXIOMS",
		"PERSONA",
		"EGO",
		"INJECTED CONTEXT",
		"RUNTIME CONTRACT",
		"TALENTS ALWAYS ON",
		"TALENTS TAGGED",
		"ACTIVE CAPABILITIES",
		"TAGGED GUIDANCE",
		"CONTINUITY CONTEXT",
		"LIVE STATE",
		"CURRENT CONDITIONS",
	)
	assertPromptSectionContains(t, sections, "AXIOMS", "AXIOMS_MARKER")
	assertPromptSectionContains(t, sections, "EGO", "EGO_MARKER")
	assertPromptSectionContains(t, sections, "INJECTED CONTEXT", "INJECT_MARKER")
	assertPromptSectionContains(t, sections, "TAGGED GUIDANCE", "TAGGED_GUIDANCE_MARKER")
	assertPromptSectionContains(t, sections, "CONTINUITY CONTEXT", "CONTINUITY_MARKER")
	assertPromptSectionContains(t, sections, "LIVE STATE", "LIVE_STATE_MARKER")
}

func TestBuildSystemPromptSections_TaskPromptUsesCompactOrder(t *testing.T) {
	dir := t.TempDir()
	axiomsPath := filepath.Join(dir, "axioms.md")
	egoPath := filepath.Join(dir, "ego.md")
	injectPath := filepath.Join(dir, "mission.md")
	if err := os.WriteFile(axiomsPath, []byte("AXIOMS_MARKER"), 0o644); err != nil {
		t.Fatalf("write axioms.md: %v", err)
	}
	if err := os.WriteFile(egoPath, []byte("EGO_MARKER"), 0o644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}
	if err := os.WriteFile(injectPath, []byte("INJECT_MARKER"), 0o644); err != nil {
		t.Fatalf("write inject file: %v", err)
	}

	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "forge.md"),
		[]byte("---\ntags: [forge]\n---\nTAGGED_GUIDANCE_MARKER"), 0o644); err != nil {
		t.Fatalf("write kb article: %v", err)
	}

	l := newPromptOrderLoop(t, kbDir)
	l.persona = "PERSONA_MARKER"
	l.ensureCoreContextProvider().updateAxiomsFile(axiomsPath)
	l.SetEgoFile(egoPath)
	l.SetInjectFiles([]string{injectPath})
	l.RegisterTagContextProvider("forge", &mockTagProvider{
		content: "LIVE_STATE_MARKER",
		bucket:  agentctx.ContextBucketLiveState,
	})
	l.RegisterAlwaysContextProvider(&mockTagProvider{
		content: "CONTINUITY_MARKER",
		bucket:  agentctx.ContextBucketContinuity,
	})

	ctx := agentctx.WithPromptMode(testCtxForLoop(l), agentctx.PromptModeTask)
	_, sections := l.buildSystemPromptWithProfileSections(
		ctx,
		"hello",
		nil,
		llm.DefaultModelInteractionProfile(),
	)
	index := promptSectionIndex(t, sections)
	assertPromptSectionsStartAtContent(t, sections)

	assertPromptSectionAbsent(t, index, "AXIOMS")
	assertPromptSectionAbsent(t, index, "EGO")
	assertPromptSectionAbsent(t, index, "INJECTED CONTEXT")
	assertPromptSectionAbsent(t, index, "TALENTS ALWAYS ON")
	assertPromptSectionAbsent(t, index, "CONTINUITY CONTEXT")
	assertPromptSectionOrder(t, index,
		"PERSONA",
		"RUNTIME CONTRACT",
		"TALENTS TAGGED",
		"ACTIVE CAPABILITIES",
		"TAGGED GUIDANCE",
		"LIVE STATE",
		"CURRENT CONDITIONS",
	)
	assertPromptSectionContains(t, sections, "TAGGED GUIDANCE", "TAGGED_GUIDANCE_MARKER")
	assertPromptSectionContains(t, sections, "LIVE STATE", "LIVE_STATE_MARKER")
}

func newPromptOrderLoop(t *testing.T, kbDir string) *Loop {
	t.Helper()
	l := newTagTestLoop()
	parsed := []talents.Talent{
		{Name: "core-guidance", Content: "ALWAYS_TALENT_MARKER"},
		{Name: "forge-guidance", Tags: []string{"forge"}, Content: "TAGGED_TALENT_MARKER"},
	}
	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Forge and code review tools.",
			Tools:        []string{"forge_pr_get"},
			AlwaysActive: true,
		},
	}
	l.SetCapabilityTags(capTags, parsed)
	l.UseCapabilitySurface([]toolcatalog.CapabilitySurface{
		{
			Tag:          "forge",
			Description:  "Forge and code review tools.",
			Tools:        []string{"forge_pr_get"},
			AlwaysActive: true,
		},
	})
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: capTags,
		KBDir:   kbDir,
		Logger:  l.logger,
	}))
	return l
}

func promptSectionIndex(t *testing.T, sections []llm.PromptSection) map[string]int {
	t.Helper()
	index := make(map[string]int, len(sections))
	for i, section := range sections {
		if _, exists := index[section.Name]; exists {
			t.Fatalf("duplicate prompt section %q in %#v", section.Name, sections)
		}
		index[section.Name] = i
	}
	return index
}

func assertPromptSectionsStartAtContent(t *testing.T, sections []llm.PromptSection) {
	t.Helper()
	for _, section := range sections {
		if strings.HasPrefix(section.Content, "\n") || strings.HasPrefix(section.Content, "\r") {
			t.Fatalf("prompt section %q starts with separator whitespace: %q", section.Name, section.Content)
		}
	}
}

func assertPromptSectionOrder(t *testing.T, index map[string]int, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := index[name]; !ok {
			t.Fatalf("missing prompt section %q in %#v", name, index)
		}
	}
	for i := 1; i < len(names); i++ {
		prev := names[i-1]
		next := names[i]
		if index[prev] >= index[next] {
			t.Fatalf("prompt section %q should appear before %q: %#v", prev, next, index)
		}
	}
}

func assertPromptSectionAbsent(t *testing.T, index map[string]int, name string) {
	t.Helper()
	if _, ok := index[name]; ok {
		t.Fatalf("prompt section %q should be absent: %#v", name, index)
	}
}

func assertPromptSectionContains(t *testing.T, sections []llm.PromptSection, name string, want string) {
	t.Helper()
	for _, section := range sections {
		if section.Name != name {
			continue
		}
		if strings.Contains(section.Content, want) {
			return
		}
		t.Fatalf("prompt section %q content = %q, want marker %q", name, section.Content, want)
	}
	t.Fatalf("missing prompt section %q", name)
}
