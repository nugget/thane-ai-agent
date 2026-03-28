package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/talents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// newTagTestLoop creates a Loop with logger and tools registry initialized,
// suitable for tests that call SetCapabilityTags.
func newTagTestLoop() *Loop {
	l := &Loop{
		logger: slog.Default(),
		tools:  tools.NewEmptyRegistry(),
	}
	return l
}

// setTagsWithAssembler is a test helper that sets capability tags on the
// loop and creates a matching TagContextAssembler. This mirrors the
// production wiring in main.go.
func setTagsWithAssembler(l *Loop, capTags map[string]config.CapabilityTagConfig, parsedTalents []talents.Talent) {
	l.SetCapabilityTags(capTags, parsedTalents)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags:  capTags,
		HAInject: l.HAInject(),
		Logger:   l.logger,
	}))
}

func TestBuildSystemPrompt_TagContextIncluded(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "arch.md")
	f2 := filepath.Join(dir, "style.md")
	os.WriteFile(f1, []byte("# Architecture\nUse hexagonal pattern."), 0644)
	os.WriteFile(f2, []byte("# Style Guide\nTabs not spaces."), 0644)

	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			Context:      []string{f1, f2},
			AlwaysActive: true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "hexagonal pattern") {
		t.Error("system prompt should contain first context file content")
	}
	if !strings.Contains(prompt, "Tabs not spaces") {
		t.Error("system prompt should contain second context file content")
	}
	if !strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should contain capability context section heading")
	}
}

func TestBuildSystemPrompt_TagContextInactiveExcluded(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "arch.md")
	os.WriteFile(f, []byte("# Architecture\nSecret content."), 0644)

	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			Context:      []string{f},
			AlwaysActive: false, // not always active, so inactive by default
		},
	}, nil)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "Secret content") {
		t.Error("system prompt should not contain context from inactive tags")
	}
	if strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should not contain capability context section when no active tags have context")
	}
}

func TestBuildSystemPrompt_TagContextDedup(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.md")
	os.WriteFile(shared, []byte("shared knowledge"), 0644)

	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			Context:      []string{shared},
			AlwaysActive: true,
		},
		"review": {
			Description:  "Code review",
			Tools:        []string{"review_run"},
			Context:      []string{shared}, // same file as forge
			AlwaysActive: true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	// The shared file should appear exactly once.
	count := strings.Count(prompt, "shared knowledge")
	if count != 1 {
		t.Errorf("shared context file should appear exactly once, found %d times", count)
	}
}

func TestBuildSystemPrompt_TagContextMissingFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.md")
	missing := filepath.Join(dir, "nonexistent.md")
	os.WriteFile(good, []byte("good content"), 0644)

	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test tag",
			Tools:        []string{"test_tool"},
			Context:      []string{missing, good},
			AlwaysActive: true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	// Good file should still be included despite missing file.
	if !strings.Contains(prompt, "good content") {
		t.Error("system prompt should still include readable context files when some are missing")
	}
}

func TestBuildSystemPrompt_TagContextRereadPerTurn(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "evolving.md")
	os.WriteFile(f, []byte("version-1"), 0644)

	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test tag",
			Tools:        []string{"test_tool"},
			Context:      []string{f},
			AlwaysActive: true,
		},
	}, nil)

	prompt1 := l.buildSystemPrompt(context.Background(), "hello", nil)
	if !strings.Contains(prompt1, "version-1") {
		t.Fatal("first prompt should contain version-1")
	}

	// Modify the file between turns.
	os.WriteFile(f, []byte("version-2"), 0644)

	prompt2 := l.buildSystemPrompt(context.Background(), "hello", nil)
	if strings.Contains(prompt2, "version-1") {
		t.Error("second prompt should not contain stale version-1")
	}
	if !strings.Contains(prompt2, "version-2") {
		t.Error("second prompt should contain updated version-2")
	}
}

func TestBuildSystemPrompt_TagContextNoCapTags(t *testing.T) {
	l := newMinimalLoop()
	// capTags not set — should not inject any tag context

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should not contain capability context section when capTags is nil")
	}
}

func TestBuildSystemPrompt_TagContextOrderAfterInjected(t *testing.T) {
	dir := t.TempDir()
	injected := filepath.Join(dir, "injected.md")
	tagCtx := filepath.Join(dir, "tag-context.md")
	os.WriteFile(injected, []byte("INJECTED_MARKER"), 0644)
	os.WriteFile(tagCtx, []byte("TAG_CONTEXT_MARKER"), 0644)

	l := newTagTestLoop()
	l.SetInjectFiles([]string{injected})
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test tag",
			Tools:        []string{"test_tool"},
			Context:      []string{tagCtx},
			AlwaysActive: true,
		},
	}, []talents.Talent{})

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	injectedIdx := strings.Index(prompt, "INJECTED_MARKER")
	tagCtxIdx := strings.Index(prompt, "TAG_CONTEXT_MARKER")
	conditionsIdx := strings.Index(prompt, "Current Conditions")

	if injectedIdx < 0 {
		t.Fatal("prompt should contain injected context marker")
	}
	if tagCtxIdx < 0 {
		t.Fatal("prompt should contain tag context marker")
	}
	if conditionsIdx < 0 {
		t.Fatal("prompt should contain current conditions")
	}

	// Tag context should appear between injected context and conditions.
	if tagCtxIdx < injectedIdx {
		t.Error("tag context should appear after injected context")
	}
	if tagCtxIdx > conditionsIdx {
		t.Error("tag context should appear before current conditions")
	}
}

// mockStateFetcher implements homeassistant.StateFetcher for agent-level tests.
type mockStateFetcher struct {
	states map[string]string
}

func (m *mockStateFetcher) FetchState(_ context.Context, entityID string) (string, error) {
	v, ok := m.states[entityID]
	if !ok {
		return "", fmt.Errorf("entity %q not found", entityID)
	}
	return v, nil
}

func TestBuildSystemPrompt_HAInjectResolved(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "pool.md")
	os.WriteFile(f, []byte("<!-- ha-inject: sensor.pool_temp -->\n# Pool Status\nRefer to live state above."), 0644)

	l := newTagTestLoop()
	// Set HAInject before assembler so it gets the fetcher.
	l.SetHAInject(&mockStateFetcher{states: map[string]string{
		"sensor.pool_temp": "84.2",
	}})
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"pool": {
			Description:  "Pool management",
			Tools:        []string{"pool_tool"},
			Context:      []string{f},
			AlwaysActive: true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "## Current HA State (live)") {
		t.Error("prompt should contain live state header")
	}
	if !strings.Contains(prompt, "- sensor.pool_temp: 84.2") {
		t.Error("prompt should contain resolved entity state")
	}
	if !strings.Contains(prompt, "# Pool Status") {
		t.Error("prompt should preserve the original document content")
	}
}

func TestBuildSystemPrompt_HAInjectNilFetcher(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "doc.md")
	os.WriteFile(f, []byte("<!-- ha-inject: sensor.temp -->\n# Doc"), 0644)

	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test",
			Tools:        []string{"test_tool"},
			Context:      []string{f},
			AlwaysActive: true,
		},
	}, nil)
	// haInject not set — should pass through without resolving

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "## Current HA State") {
		t.Error("prompt should not contain state block when haInject is nil")
	}
	if !strings.Contains(prompt, "# Doc") {
		t.Error("original document content should be preserved")
	}
}

func TestBuildSystemPrompt_HAInjectFetchFailure(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "doc.md")
	os.WriteFile(f, []byte("<!-- ha-inject: sensor.missing -->\n# Doc"), 0644)

	l := newTagTestLoop()
	l.SetHAInject(&mockStateFetcher{states: map[string]string{}})
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test",
			Tools:        []string{"test_tool"},
			Context:      []string{f},
			AlwaysActive: true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "⚠️ HA entity state unavailable") {
		t.Error("prompt should contain unavailability warning when all fetches fail")
	}
	if !strings.Contains(prompt, "# Doc") {
		t.Error("original document content should be preserved on failure")
	}
}

// --- TagContextAssembler unit tests ---

// mockTagProvider is a test double for TagContextProvider.
type mockTagProvider struct {
	content string
	err     error
}

func (m *mockTagProvider) TagContext(_ context.Context) (string, error) {
	return m.content, m.err
}

func TestTagContextAssembler_LiveProvider(t *testing.T) {
	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{
			"forge": {},
		},
		Providers: map[string]TagContextProvider{
			"forge": &mockTagProvider{content: `{"accounts":["github-primary"]}`},
		},
	})

	result := a.Build(context.Background(), map[string]bool{"forge": true})

	if !strings.Contains(result, "github-primary") {
		t.Error("expected live provider content")
	}
}

func TestTagContextAssembler_ProviderError(t *testing.T) {
	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{
			"forge": {},
			"ha":    {},
		},
		Providers: map[string]TagContextProvider{
			"forge": &mockTagProvider{err: fmt.Errorf("connection failed")},
			"ha":    &mockTagProvider{content: "ha context ok"},
		},
	})

	result := a.Build(context.Background(), map[string]bool{"forge": true, "ha": true})

	if strings.Contains(result, "connection failed") {
		t.Error("provider error should not appear in output")
	}
	if !strings.Contains(result, "ha context ok") {
		t.Error("other provider should still produce output")
	}
}

func TestTagContextAssembler_TaggedKBArticles(t *testing.T) {
	kbDir := t.TempDir()

	os.WriteFile(filepath.Join(kbDir, "forge-guide.md"),
		[]byte("---\ntags: [forge]\n---\n# Forge Conventions\nAlways use PRs."), 0o644)
	os.WriteFile(filepath.Join(kbDir, "general.md"),
		[]byte("# General\nUntagged."), 0o644)
	os.WriteFile(filepath.Join(kbDir, "ha-guide.md"),
		[]byte("---\ntags: [ha]\n---\n# HA Guide"), 0o644)

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{"forge": {}},
		KBDir:   kbDir,
	})

	result := a.Build(context.Background(), map[string]bool{"forge": true})

	if !strings.Contains(result, "Forge Conventions") {
		t.Error("expected tagged KB article content")
	}
	if strings.Contains(result, "Untagged") {
		t.Error("untagged KB article should not be auto-loaded")
	}
	if strings.Contains(result, "HA Guide") {
		t.Error("KB article with different tag should not load")
	}
	// Frontmatter should be stripped.
	if strings.Contains(result, "tags:") {
		t.Error("frontmatter should be stripped from KB articles")
	}
}

func TestTagContextAssembler_AllThreeSources(t *testing.T) {
	dir := t.TempDir()
	staticFile := filepath.Join(dir, "static.md")
	os.WriteFile(staticFile, []byte("STATIC_CONTENT"), 0o644)

	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "kb.md"),
		[]byte("---\ntags: [forge]\n---\nKB_CONTENT"), 0o644)

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{
			"forge": {Context: []string{staticFile}},
		},
		KBDir: kbDir,
		Providers: map[string]TagContextProvider{
			"forge": &mockTagProvider{content: "LIVE_CONTENT"},
		},
	})

	result := a.Build(context.Background(), map[string]bool{"forge": true})

	for _, want := range []string{"STATIC_CONTENT", "KB_CONTENT", "LIVE_CONTENT"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %s in assembled output", want)
		}
	}
}

func TestTagContextAssembler_NilAssembler(t *testing.T) {
	var a *TagContextAssembler
	result := a.Build(context.Background(), map[string]bool{"forge": true})
	if result != "" {
		t.Errorf("nil assembler should return empty, got %q", result)
	}
}

func TestTagContextAssembler_KBArticleTags(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "a.md"), []byte("---\ntags: [forge]\n---\nA"), 0o644)
	os.WriteFile(filepath.Join(kbDir, "b.md"), []byte("---\ntags: [forge, ha]\n---\nB"), 0o644)
	os.WriteFile(filepath.Join(kbDir, "c.md"), []byte("---\ntags: [ha]\n---\nC"), 0o644)
	os.WriteFile(filepath.Join(kbDir, "d.md"), []byte("no frontmatter"), 0o644)

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{},
		KBDir:   kbDir,
	})

	counts := a.KBArticleTags()
	if counts["forge"] != 2 {
		t.Errorf("forge KB count = %d, want 2", counts["forge"])
	}
	if counts["ha"] != 2 {
		t.Errorf("ha KB count = %d, want 2", counts["ha"])
	}
}

func TestBuildSystemPrompt_TagContextViaProvider(t *testing.T) {
	l := newTagTestLoop()
	setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			AlwaysActive: true,
		},
	}, nil)
	l.RegisterTagContextProvider("forge", &mockTagProvider{
		content: `{"accounts":["github-primary"]}`,
	})

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "github-primary") {
		t.Error("system prompt should contain live provider content")
	}
	if !strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should contain capability context heading")
	}
}
