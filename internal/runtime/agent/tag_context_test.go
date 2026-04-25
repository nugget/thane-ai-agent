package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
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

// testCtxForLoop creates a context containing a capabilityScope seeded
// from the loop's capTags (always-active tags are activated). This
// mirrors what Run() does before calling buildSystemPrompt.
func testCtxForLoop(l *Loop) context.Context {
	if l.capTags == nil {
		return context.Background()
	}
	return withCapabilityScope(context.Background(), newCapabilityScope(l.capTags, nil))
}

func TestBuildSystemPrompt_TagContextIncluded(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "arch.md"),
		[]byte("---\ntags: [forge]\n---\n# Architecture\nUse hexagonal pattern."), 0644)
	os.WriteFile(filepath.Join(kbDir, "style.md"),
		[]byte("---\ntags: [forge]\n---\n# Style Guide\nTabs not spaces."), 0644)

	l := newTagTestLoop()
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{
			"forge": {
				Description:  "Code generation",
				Tools:        []string{"forge_run"},
				AlwaysActive: true,
			},
		},
		KBDir:  kbDir,
		Logger: l.logger,
	}))
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			AlwaysActive: true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "hexagonal pattern") {
		t.Error("system prompt should contain first KB article content")
	}
	if !strings.Contains(prompt, "Tabs not spaces") {
		t.Error("system prompt should contain second KB article content")
	}
	if !strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should contain capability context section heading")
	}
}

func TestBuildSystemPrompt_TagContextInactiveExcluded(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "arch.md"),
		[]byte("---\ntags: [forge]\n---\n# Architecture\nSecret content."), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			AlwaysActive: false, // not always active, so inactive by default
		},
	}

	l := newTagTestLoop()
	l.SetCapabilityTags(capTags, nil)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: capTags,
		KBDir:   kbDir,
		Logger:  l.logger,
	}))

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if strings.Contains(prompt, "Secret content") {
		t.Error("system prompt should not contain context from inactive tags")
	}
	if strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should not contain capability context section when no active tags have context")
	}
}

func TestBuildSystemPrompt_TagContextDedup(t *testing.T) {
	kbDir := t.TempDir()
	// A KB article tagged with both forge and review should appear only once.
	os.WriteFile(filepath.Join(kbDir, "shared.md"),
		[]byte("---\ntags: [forge, review]\n---\nshared knowledge"), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description:  "Code generation",
			Tools:        []string{"forge_run"},
			AlwaysActive: true,
		},
		"review": {
			Description:  "Code review",
			Tools:        []string{"review_run"},
			AlwaysActive: true,
		},
	}

	l := newTagTestLoop()
	l.SetCapabilityTags(capTags, nil)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: capTags,
		KBDir:   kbDir,
		Logger:  l.logger,
	}))

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	// The shared file should appear exactly once.
	count := strings.Count(prompt, "shared knowledge")
	if count != 1 {
		t.Errorf("shared context file should appear exactly once, found %d times", count)
	}
}

func TestBuildSystemPrompt_TagContextEntryPointFirst(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "article.md"),
		[]byte("---\ntags: [development]\n---\nARTICLE_MARKER"), 0o644)
	os.WriteFile(filepath.Join(kbDir, "tree.md"),
		[]byte("---\nkind: entry_point\ntags: [development]\n---\nTREE_MARKER"), 0o644)

	capTags := map[string]config.CapabilityTagConfig{
		"development": {
			Description:  "Development entry point",
			AlwaysActive: true,
		},
	}

	l := newTagTestLoop()
	l.SetCapabilityTags(capTags, nil)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: capTags,
		KBDir:   kbDir,
		Logger:  l.logger,
	}))

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
	treeIdx := strings.Index(prompt, "TREE_MARKER")
	articleIdx := strings.Index(prompt, "ARTICLE_MARKER")
	if treeIdx < 0 || articleIdx < 0 {
		t.Fatalf("prompt missing expected markers:\n%s", prompt)
	}
	if treeIdx > articleIdx {
		t.Fatalf("entry-point guidance should precede doctrine article in prompt:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_TagContextNoCapTags(t *testing.T) {
	l := newMinimalLoop()
	// capTags not set — should not inject any tag context

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should not contain capability context section when capTags is nil")
	}
}

func TestBuildSystemPrompt_TagContextChannelPinnedBuiltinTagIncluded(t *testing.T) {
	kbDir := t.TempDir()
	err := os.WriteFile(
		filepath.Join(kbDir, "interactive.md"),
		[]byte("---\ntags: [interactive]\n---\nINTERACTIVE_CONTEXT_MARKER"),
		0644,
	)
	if err != nil {
		t.Fatalf("write kb article: %v", err)
	}

	l := newMinimalLoop()
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		KBDir:    kbDir,
		Logger:   l.logger,
		HAInject: l.HAInject(),
	}))

	scope := newCapabilityScope(nil, nil)
	scope.PinChannelTags([]string{"interactive"})
	ctx := withCapabilityScope(context.Background(), scope)

	prompt := l.buildSystemPrompt(ctx, "hello", nil)

	if !strings.Contains(prompt, "INTERACTIVE_CONTEXT_MARKER") {
		t.Fatal("channel-pinned builtin helper tag should inject matching KB article")
	}
	if !strings.Contains(prompt, "Capability Context") {
		t.Fatal("prompt should include capability context heading for channel-pinned helper tag")
	}
}

func TestBuildSystemPrompt_TagContextOrderAfterInjected(t *testing.T) {
	dir := t.TempDir()
	injected := filepath.Join(dir, "injected.md")
	os.WriteFile(injected, []byte("INJECTED_MARKER"), 0644)

	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "tag-context.md"),
		[]byte("---\ntags: [test]\n---\nTAG_CONTEXT_MARKER"), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test tag",
			Tools:        []string{"test_tool"},
			AlwaysActive: true,
		},
	}

	l := newTagTestLoop()
	l.SetInjectFiles([]string{injected})
	l.SetCapabilityTags(capTags, []talents.Talent{})
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: capTags,
		KBDir:   kbDir,
		Logger:  l.logger,
	}))

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

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
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "pool.md"),
		[]byte("---\ntags: [pool]\n---\n<!-- ha-inject: sensor.pool_temp -->\n# Pool Status\nRefer to live state above."), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"pool": {
			Description:  "Pool management",
			Tools:        []string{"pool_tool"},
			AlwaysActive: true,
		},
	}

	l := newTagTestLoop()
	// Set HAInject before assembler so it gets the fetcher.
	l.SetHAInject(&mockStateFetcher{states: map[string]string{
		"sensor.pool_temp": "84.2",
	}})
	l.SetCapabilityTags(capTags, nil)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags:  capTags,
		KBDir:    kbDir,
		HAInject: l.HAInject(),
		Logger:   l.logger,
	}))

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

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
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "doc.md"),
		[]byte("---\ntags: [test]\n---\n<!-- ha-inject: sensor.temp -->\n# Doc"), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test",
			Tools:        []string{"test_tool"},
			AlwaysActive: true,
		},
	}

	l := newTagTestLoop()
	l.SetCapabilityTags(capTags, nil)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: capTags,
		KBDir:   kbDir,
		Logger:  l.logger,
	}))
	// haInject not set — should pass through without resolving

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if strings.Contains(prompt, "## Current HA State") {
		t.Error("prompt should not contain state block when haInject is nil")
	}
	if !strings.Contains(prompt, "# Doc") {
		t.Error("original document content should be preserved")
	}
}

func TestBuildSystemPrompt_HAInjectFetchFailure(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "doc.md"),
		[]byte("---\ntags: [test]\n---\n<!-- ha-inject: sensor.missing -->\n# Doc"), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"test": {
			Description:  "Test",
			Tools:        []string{"test_tool"},
			AlwaysActive: true,
		},
	}

	l := newTagTestLoop()
	l.SetHAInject(&mockStateFetcher{states: map[string]string{}})
	l.SetCapabilityTags(capTags, nil)
	l.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags:  capTags,
		KBDir:    kbDir,
		HAInject: l.HAInject(),
		Logger:   l.logger,
	}))

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "⚠️ HA entity state unavailable") {
		t.Error("prompt should contain unavailability warning when all fetches fail")
	}
	if !strings.Contains(prompt, "# Doc") {
		t.Error("original document content should be preserved on failure")
	}
}

// --- TagContextAssembler unit tests ---

func TestSafeManagedRefPath_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if got, ok := safeManagedRefPath(root, link); ok {
		t.Fatalf("safeManagedRefPath() = (%q, true), want false for symlink escape", got)
	}
}

func TestSafeManagedRefPath_AllowsSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.md")
	if err := os.WriteFile(target, []byte("safe"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, ok := safeManagedRefPath(root, link)
	if !ok {
		t.Fatal("safeManagedRefPath() rejected symlink that resolves inside root")
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if got != want {
		t.Fatalf("safeManagedRefPath() = %q, want %q", got, want)
	}
}

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
	})

	providers := map[string]TagContextProvider{
		"forge": &mockTagProvider{content: `{"accounts":["github-primary"]}`},
	}
	result := a.Build(context.Background(), map[string]bool{"forge": true}, providers)

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
	})

	providers := map[string]TagContextProvider{
		"forge": &mockTagProvider{err: fmt.Errorf("connection failed")},
		"ha":    &mockTagProvider{content: "ha context ok"},
	}
	result := a.Build(context.Background(), map[string]bool{"forge": true, "ha": true}, providers)

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

	result := a.Build(context.Background(), map[string]bool{"forge": true}, nil)

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

func TestTagContextAssembler_KBAndLiveProvider(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "kb.md"),
		[]byte("---\ntags: [forge]\n---\nKB_CONTENT"), 0o644)

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{
			"forge": {},
		},
		KBDir: kbDir,
	})

	providers := map[string]TagContextProvider{
		"forge": &mockTagProvider{content: "LIVE_CONTENT"},
	}
	result := a.Build(context.Background(), map[string]bool{"forge": true}, providers)

	for _, want := range []string{"KB_CONTENT", "LIVE_CONTENT"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %s in assembled output", want)
		}
	}
}

func TestTagContextAssembler_NilAssembler(t *testing.T) {
	var a *TagContextAssembler
	result := a.Build(context.Background(), map[string]bool{"forge": true}, nil)
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

func TestTagContextAssembler_KBMenuHints(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "knowledge-tree.md"), []byte("---\nkind: entry_point\ntags: [knowledge]\nteaser: \"Activate when the next move is about internal docs or durable knowledge.\"\nnext_tags: [files, memory, web]\n---\nTREE"), 0o644)
	os.WriteFile(filepath.Join(kbDir, "knowledge-article.md"), []byte("---\ntags: [knowledge]\n---\nARTICLE"), 0o644)

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{},
		KBDir:   kbDir,
	})

	hints := a.KBMenuHints()
	hint, ok := hints["knowledge"]
	if !ok {
		t.Fatal("knowledge menu hint missing")
	}
	if hint.Teaser != "Activate when the next move is about internal docs or durable knowledge." {
		t.Fatalf("teaser = %q", hint.Teaser)
	}
	if got := strings.Join(hint.NextTags, ","); got != "files,memory,web" {
		t.Fatalf("next_tags = %q", got)
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

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "github-primary") {
		t.Error("system prompt should contain live provider content")
	}
	if !strings.Contains(prompt, "Capability Context") {
		t.Error("system prompt should contain capability context heading")
	}
}
