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
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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
// from the loop's capTags (core tags are activated). This
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
				Description: "Code generation",
				Tools:       []string{"forge_run"},
				Core:        true,
			},
		},
		KBDir:  kbDir,
		Logger: l.logger,
	}))
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Code generation",
			Tools:       []string{"forge_run"},
			Core:        true,
		},
	}, nil)

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "hexagonal pattern") {
		t.Error("system prompt should contain first KB article content")
	}
	if !strings.Contains(prompt, "Tabs not spaces") {
		t.Error("system prompt should contain second KB article content")
	}
	if !strings.Contains(prompt, "Tagged Guidance") {
		t.Error("system prompt should contain tagged guidance section heading")
	}
}

func TestBuildSystemPrompt_TagContextInactiveExcluded(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "arch.md"),
		[]byte("---\ntags: [forge]\n---\n# Architecture\nSecret content."), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Code generation",
			Tools:       []string{"forge_run"},
			Core:        false, // not always active, so inactive by default
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
	if strings.Contains(prompt, "Tagged Guidance") {
		t.Error("system prompt should not contain tagged guidance section when no active tags have context")
	}
}

func TestBuildSystemPrompt_TagContextDedup(t *testing.T) {
	kbDir := t.TempDir()
	// A KB article tagged with both forge and review should appear only once.
	os.WriteFile(filepath.Join(kbDir, "shared.md"),
		[]byte("---\ntags: [forge, review]\n---\nshared knowledge"), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Code generation",
			Tools:       []string{"forge_run"},
			Core:        true,
		},
		"review": {
			Description: "Code review",
			Tools:       []string{"review_run"},
			Core:        true,
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

func TestBuildSystemPrompt_TagContextTrailheadFirst(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "article.md"),
		[]byte("---\ntags: [development]\n---\nARTICLE_MARKER"), 0o644)
	os.WriteFile(filepath.Join(kbDir, "tree.md"),
		[]byte("---\nkind: trailhead\ntags: [development]\n---\nTREE_MARKER"), 0o644)

	capTags := map[string]config.CapabilityTagConfig{
		"development": {
			Description: "Development trailhead",
			Core:        true,
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
		t.Fatalf("trailhead guidance should precede doctrine article in prompt:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_TagContextNoCapTags(t *testing.T) {
	l := newMinimalLoop()
	// capTags not set — should not inject any tag context

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if strings.Contains(prompt, "Tagged Guidance") {
		t.Error("system prompt should not contain tagged guidance section when capTags is nil")
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
	if !strings.Contains(prompt, "Tagged Guidance") {
		t.Fatal("prompt should include tagged guidance heading for channel-pinned helper tag")
	}
}

func TestBuildSystemPrompt_CoreContextProviderOrder(t *testing.T) {
	dir := t.TempDir()
	injected := filepath.Join(dir, "injected.md")
	os.WriteFile(injected, []byte("INJECTED_MARKER"), 0644)

	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "tag-context.md"),
		[]byte("---\ntags: [test]\n---\nTAG_CONTEXT_MARKER"), 0644)

	capTags := map[string]config.CapabilityTagConfig{
		"test": {
			Description: "Test tag",
			Tools:       []string{"test_tool"},
			Core:        true,
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

	// Core context is first-class stable context, before generated
	// tagged guidance and live/runtime sections.
	if injectedIdx > tagCtxIdx {
		t.Error("core context should appear before tagged context")
	}
	if injectedIdx > conditionsIdx {
		t.Error("core context should appear before current conditions")
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
			Description: "Pool management",
			Tools:       []string{"pool_tool"},
			Core:        true,
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
			Description: "Test",
			Tools:       []string{"test_tool"},
			Core:        true,
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
			Description: "Test",
			Tools:       []string{"test_tool"},
			Core:        true,
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
	bucket  agentctx.ContextBucket
}

func (m *mockTagProvider) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	return m.content, m.err
}

func (m *mockTagProvider) TagContextBucket() agentctx.ContextBucket {
	return m.bucket
}

type rejectingContextVerifier struct{}

func (rejectingContextVerifier) VerifyRef(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("blocked by signature policy")
}

func (rejectingContextVerifier) VerifyPath(_ context.Context, _ string, _ string) error {
	return fmt.Errorf("blocked by signature policy")
}

func TestTagContextAssembler_LiveProvider(t *testing.T) {
	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{
			"forge": {},
		},
	})

	a.RegisterTaggedProvider("forge", &mockTagProvider{content: `{"accounts":["github-primary"]}`})
	result := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})

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

	a.RegisterTaggedProvider("forge", &mockTagProvider{err: fmt.Errorf("connection failed")})
	a.RegisterTaggedProvider("ha", &mockTagProvider{content: "ha context ok"})
	result := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true, "ha": true}})

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

	result := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})

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

func TestTagContextAssembler_SkipsKBArticleRejectedByVerifier(t *testing.T) {
	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "forge-guide.md"),
		[]byte("---\ntags: [forge]\n---\nSIGNED_ONLY_CONTEXT"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags:  map[string]config.CapabilityTagConfig{"forge": {}},
		KBDir:    kbDir,
		Verifier: rejectingContextVerifier{},
		Logger:   slog.Default(),
	})

	result := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})
	if strings.Contains(result, "SIGNED_ONLY_CONTEXT") {
		t.Fatalf("rejected KB article leaked into tag context:\n%s", result)
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

	a.RegisterTaggedProvider("forge", &mockTagProvider{content: "LIVE_CONTENT"})
	result := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})

	for _, want := range []string{"KB_CONTENT", "LIVE_CONTENT"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %s in assembled output", want)
		}
	}
}

func TestTagContextAssembler_BuildSectionsBuckets(t *testing.T) {
	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "kb.md"),
		[]byte("---\ntags: [forge]\n---\nKB_GUIDANCE"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{"forge": {}},
		KBDir:   kbDir,
	})
	a.RegisterTaggedProvider("forge", &mockTagProvider{
		content: "LIVE_PROVIDER",
		bucket:  agentctx.ContextBucketLiveState,
	})
	a.RegisterAlwaysProvider(&mockTagProvider{
		content: "RELATED_PROVIDER",
		bucket:  agentctx.ContextBucketRelated,
	})

	sections := a.BuildSections(context.Background(), agentctx.ContextRequest{
		ActiveTags:    map[string]bool{"forge": true},
		IncludeAlways: true,
	})
	if len(sections) != 3 {
		t.Fatalf("BuildSections returned %d sections, want 3: %#v", len(sections), sections)
	}
	wants := []struct {
		bucket  agentctx.ContextBucket
		title   string
		content string
	}{
		{agentctx.ContextBucketTaggedGuidance, "Tagged Guidance", "KB_GUIDANCE"},
		{agentctx.ContextBucketRelated, "Related Context", "RELATED_PROVIDER"},
		{agentctx.ContextBucketLiveState, "Live State", "LIVE_PROVIDER"},
	}
	for i, want := range wants {
		if sections[i].Bucket != want.bucket {
			t.Fatalf("section %d bucket = %q, want %q", i, sections[i].Bucket, want.bucket)
		}
		if sections[i].Title != want.title {
			t.Fatalf("section %d title = %q, want %q", i, sections[i].Title, want.title)
		}
		if !strings.Contains(sections[i].Content, want.content) {
			t.Fatalf("section %d content = %q, want marker %q", i, sections[i].Content, want.content)
		}
	}

	withoutAlways := a.BuildSections(context.Background(), agentctx.ContextRequest{
		ActiveTags:    map[string]bool{"forge": true},
		IncludeAlways: false,
	})
	if len(withoutAlways) != 2 {
		t.Fatalf("BuildSections without always returned %d sections, want 2: %#v", len(withoutAlways), withoutAlways)
	}
	for _, section := range withoutAlways {
		if strings.Contains(section.Content, "RELATED_PROVIDER") {
			t.Fatalf("IncludeAlways=false should suppress always-provider content: %#v", withoutAlways)
		}
		if section.Bucket == agentctx.ContextBucketRelated {
			t.Fatalf("IncludeAlways=false should suppress always-provider bucket: %#v", withoutAlways)
		}
	}
}

func TestTagContextAssembler_TaggedProviderOrderIsStable(t *testing.T) {
	a := NewTagContextAssembler(TagContextAssemblerConfig{})
	a.RegisterTaggedProvider("zulu", &mockTagProvider{content: "ZULU_PROVIDER"})
	a.RegisterTaggedProvider("alpha", &mockTagProvider{content: "ALPHA_PROVIDER"})

	sections := a.BuildSections(context.Background(), agentctx.ContextRequest{
		ActiveTags: map[string]bool{
			"zulu":  true,
			"alpha": true,
		},
	})
	if len(sections) != 1 {
		t.Fatalf("BuildSections returned %d sections, want 1: %#v", len(sections), sections)
	}
	alphaIdx := strings.Index(sections[0].Content, "ALPHA_PROVIDER")
	zuluIdx := strings.Index(sections[0].Content, "ZULU_PROVIDER")
	if alphaIdx < 0 || zuluIdx < 0 {
		t.Fatalf("missing provider markers in section content: %q", sections[0].Content)
	}
	if alphaIdx > zuluIdx {
		t.Fatalf("tagged provider output should be sorted by tag: %q", sections[0].Content)
	}
}

func TestTagContextAssembler_BucketCapDoesNotAbortOtherBuckets(t *testing.T) {
	kbDir := t.TempDir()
	oversizedGuidance := strings.Repeat("K", maxTagContextBytes+1024)
	if err := os.WriteFile(filepath.Join(kbDir, "large.md"),
		[]byte("---\ntags: [forge]\n---\n"+oversizedGuidance), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{"forge": {}},
		KBDir:   kbDir,
	})
	a.RegisterTaggedProvider("forge", &mockTagProvider{
		content: "LIVE_AFTER_GUIDANCE_CAP",
		bucket:  agentctx.ContextBucketLiveState,
	})
	a.RegisterAlwaysProvider(&mockTagProvider{
		content: "CONTINUITY_AFTER_GUIDANCE_CAP",
		bucket:  agentctx.ContextBucketContinuity,
	})

	sections := a.BuildSections(context.Background(), agentctx.ContextRequest{
		ActiveTags:    map[string]bool{"forge": true},
		IncludeAlways: true,
	})
	if len(sections) != 3 {
		t.Fatalf("BuildSections returned %d sections, want 3: %#v", len(sections), sections)
	}
	assertSectionContains(t, sections, agentctx.ContextBucketTaggedGuidance, "Tagged Guidance truncated: exceeded 64 KB bucket limit")
	assertSectionContains(t, sections, agentctx.ContextBucketLiveState, "LIVE_AFTER_GUIDANCE_CAP")
	assertSectionContains(t, sections, agentctx.ContextBucketContinuity, "CONTINUITY_AFTER_GUIDANCE_CAP")
}

func TestAppendContextContentRespectsLimitWithCustomMarker(t *testing.T) {
	t.Run("marker fits", func(t *testing.T) {
		var buf strings.Builder
		truncated := appendContextContent(&buf, []byte("abcdefghijk"), 10, "[cut]")
		if !truncated {
			t.Fatal("appendContextContent should report truncation")
		}
		if got := buf.Len(); got > 10 {
			t.Fatalf("buffer length = %d, want <= 10", got)
		}
		if !strings.Contains(buf.String(), "[cut]") {
			t.Fatalf("buffer missing marker: %q", buf.String())
		}
	})

	t.Run("marker does not fit", func(t *testing.T) {
		var buf strings.Builder
		buf.WriteString(strings.Repeat("x", 10))
		truncated := appendContextContent(&buf, []byte("abc"), 12, "[marker-too-long]")
		if !truncated {
			t.Fatal("appendContextContent should report truncation")
		}
		if got := buf.Len(); got > 12 {
			t.Fatalf("buffer length = %d, want <= 12", got)
		}
	})
}

func TestTagContextAssembler_NilAssembler(t *testing.T) {
	var a *TagContextAssembler
	result := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})
	if result != "" {
		t.Errorf("nil assembler should return empty, got %q", result)
	}
}

func assertSectionContains(t *testing.T, sections []agentctx.ContextSection, bucket agentctx.ContextBucket, want string) {
	t.Helper()
	for _, section := range sections {
		if section.Bucket != bucket {
			continue
		}
		if strings.Contains(section.Content, want) {
			return
		}
		t.Fatalf("section %q content = %q, want marker %q", bucket, section.Content, want)
	}
	t.Fatalf("missing section for bucket %q in %#v", bucket, sections)
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
	os.WriteFile(filepath.Join(kbDir, "knowledge-tree.md"), []byte("---\nkind: trailhead\ntags: [knowledge]\nteaser: \"Activate when the next move is about internal docs or durable knowledge.\"\nnext_tags: [files, memory, web]\n---\nTREE"), 0o644)
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
			Description: "Code generation",
			Tools:       []string{"forge_run"},
			Core:        true,
		},
	}, nil)
	l.RegisterTagContextProvider("forge", &mockTagProvider{
		content: `{"accounts":["github-primary"]}`,
	})

	prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)

	if !strings.Contains(prompt, "github-primary") {
		t.Error("system prompt should contain live provider content")
	}
	if !strings.Contains(prompt, "Tagged Guidance") {
		t.Error("system prompt should contain tagged guidance heading")
	}
}

// TestRegisterTagContextProvider_NormalizesTag covers the case where a
// provider is staged before the assembler is wired. The staged tag must
// be trimmed at staging time so it collides correctly with later
// assembler-direct registrations and so the drain order can't make
// "last write wins" non-deterministic for whitespace-equivalent tags.
func TestRegisterTagContextProvider_NormalizesTag(t *testing.T) {
	t.Run("whitespace tag still resolves at build time", func(t *testing.T) {
		l := newTagTestLoop()
		l.RegisterTagContextProvider("  forge  ", &mockTagProvider{
			content: `{"accounts":["github-primary"]}`,
		})
		setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
			"forge": {
				Description: "Code generation",
				Tools:       []string{"forge_run"},
				Core:        true,
			},
		}, nil)

		prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
		if !strings.Contains(prompt, "github-primary") {
			t.Error("provider registered with whitespace-padded tag should fire under the trimmed tag")
		}
	})

	t.Run("staged registration with equivalent tags resolves to one", func(t *testing.T) {
		l := newTagTestLoop()
		// Stage two providers under whitespace-equivalent tags. After
		// trimming both should land on the same key, so only one
		// survives — but importantly, neither leaks past the drain as
		// a stray un-trimmed key.
		l.RegisterTagContextProvider("forge", &mockTagProvider{content: "first"})
		l.RegisterTagContextProvider("forge ", &mockTagProvider{content: "second"})
		setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
			"forge": {Description: "x", Tools: []string{"t"}, Core: true},
		}, nil)

		prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
		// Exactly one provider's content must appear; both would
		// indicate the staging map kept whitespace-distinct keys.
		hasFirst := strings.Contains(prompt, "first")
		hasSecond := strings.Contains(prompt, "second")
		if hasFirst && hasSecond {
			t.Error("both providers fired; staging keys should have collided after trim")
		}
		if !hasFirst && !hasSecond {
			t.Error("neither provider fired; staged registration should reach the assembler")
		}
	})

	t.Run("empty-after-trim tag is dropped", func(t *testing.T) {
		l := newTagTestLoop()
		// Should not panic and should not create a stray entry.
		l.RegisterTagContextProvider("   ", &mockTagProvider{content: "leak"})
		setTagsWithAssembler(l, map[string]config.CapabilityTagConfig{
			"forge": {Description: "x", Tools: []string{"t"}, Core: true},
		}, nil)

		prompt := l.buildSystemPrompt(testCtxForLoop(l), "hello", nil)
		if strings.Contains(prompt, "leak") {
			t.Error("empty-after-trim registration should be dropped, not staged")
		}
	})
}

func TestArticleMatchesTags_OrSemantics(t *testing.T) {
	a := kbArticle{Tags: []string{"forge", "ha"}}

	if !articleMatchesTags(a, map[string]bool{"forge": true}) {
		t.Error("expected match when forge is active")
	}
	if !articleMatchesTags(a, map[string]bool{"ha": true}) {
		t.Error("expected match when ha is active")
	}
	if !articleMatchesTags(a, map[string]bool{"forge": true, "ha": true}) {
		t.Error("expected match when both are active")
	}
	if articleMatchesTags(a, map[string]bool{"unrelated": true}) {
		t.Error("expected no match when only an unlisted tag is active")
	}
}

func TestArticleMatchesTags_AndSemantics(t *testing.T) {
	// tags_all only: every tag must be active.
	a := kbArticle{TagsAll: []string{"owner", "message_channel"}}

	if articleMatchesTags(a, map[string]bool{"owner": true}) {
		t.Error("expected no match when only owner is active")
	}
	if articleMatchesTags(a, map[string]bool{"message_channel": true}) {
		t.Error("expected no match when only message_channel is active")
	}
	if !articleMatchesTags(a, map[string]bool{"owner": true, "message_channel": true}) {
		t.Error("expected match when both required tags are active")
	}
	if !articleMatchesTags(a, map[string]bool{"owner": true, "message_channel": true, "extra": true}) {
		t.Error("expected match with extra tags active too")
	}
}

func TestArticleMatchesTags_OrAndCombined(t *testing.T) {
	// (any of Tags) AND (all of TagsAll). Useful for "fires for several
	// trailhead tags, but only when paired with a runtime gate."
	a := kbArticle{
		Tags:    []string{"forge", "ha"},
		TagsAll: []string{"owner"},
	}

	cases := []struct {
		name   string
		active map[string]bool
		want   bool
	}{
		{"or-only", map[string]bool{"forge": true}, false},
		{"and-only", map[string]bool{"owner": true}, false},
		{"both-via-forge", map[string]bool{"forge": true, "owner": true}, true},
		{"both-via-ha", map[string]bool{"ha": true, "owner": true}, true},
		{"neither", map[string]bool{"unrelated": true}, false},
		{"all-three", map[string]bool{"forge": true, "ha": true, "owner": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := articleMatchesTags(a, tc.active); got != tc.want {
				t.Errorf("active=%v: got %v, want %v", tc.active, got, tc.want)
			}
		})
	}
}

func TestTagContextAssembler_TagsAllArticleInjects(t *testing.T) {
	kbDir := t.TempDir()
	os.WriteFile(filepath.Join(kbDir, "owner-signal-bundle.md"),
		[]byte("---\ntags_all: [owner, message_channel]\n---\n# Owner-Signal Bundle"), 0o644)

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{"owner": {}, "message_channel": {}},
		KBDir:   kbDir,
	})

	// Either tag alone: silent.
	if got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"owner": true}}); strings.Contains(got, "Owner-Signal Bundle") {
		t.Errorf("article injected with only owner active:\n%s", got)
	}
	if got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"message_channel": true}}); strings.Contains(got, "Owner-Signal Bundle") {
		t.Errorf("article injected with only message_channel active:\n%s", got)
	}

	// Intersection: injects.
	got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"owner": true, "message_channel": true}})
	if !strings.Contains(got, "Owner-Signal Bundle") {
		t.Errorf("article missing when both required tags active:\n%s", got)
	}
}

func TestTagContextAssembler_LiveFrontmatterPickup(t *testing.T) {
	// Frontmatter edits, additions, and deletions all propagate on the
	// next Build call — no process restart required. Each Build
	// re-scans the KB directory.
	kbDir := t.TempDir()

	a := NewTagContextAssembler(TagContextAssemblerConfig{
		CapTags: map[string]config.CapabilityTagConfig{"forge": {}, "ha": {}},
		KBDir:   kbDir,
	})

	// First Build: empty dir, nothing injects.
	if got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}}); got != "" {
		t.Errorf("expected empty output before any articles exist, got: %s", got)
	}

	// Add an article tagged forge — next Build should pick it up
	// without reconstruction.
	articlePath := filepath.Join(kbDir, "forge.md")
	if err := os.WriteFile(articlePath,
		[]byte("---\ntags: [forge]\n---\nFORGE_V1"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})
	if !strings.Contains(got, "FORGE_V1") {
		t.Errorf("new article not picked up:\n%s", got)
	}

	// Edit the article body — next Build should see the new content.
	if err := os.WriteFile(articlePath,
		[]byte("---\ntags: [forge]\n---\nFORGE_V2"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}})
	if !strings.Contains(got, "FORGE_V2") {
		t.Errorf("body edit not picked up:\n%s", got)
	}
	if strings.Contains(got, "FORGE_V1") {
		t.Errorf("stale content from first Build leaked through:\n%s", got)
	}

	// Change the frontmatter to retag the article — must propagate
	// without restart. Activate `ha` instead and confirm.
	if err := os.WriteFile(articlePath,
		[]byte("---\ntags: [ha]\n---\nFORGE_V2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"forge": true}}); strings.Contains(got, "FORGE_V2") {
		t.Errorf("retagged article still firing for forge:\n%s", got)
	}
	got = a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"ha": true}})
	if !strings.Contains(got, "FORGE_V2") {
		t.Errorf("retagged article not firing for ha:\n%s", got)
	}

	// Delete the article — next Build should be silent.
	if err := os.Remove(articlePath); err != nil {
		t.Fatal(err)
	}
	if got := a.Build(context.Background(), agentctx.ContextRequest{ActiveTags: map[string]bool{"ha": true}}); got != "" {
		t.Errorf("deleted article still appearing:\n%s", got)
	}
}

func TestKBArticleTags_CountsTagsAll(t *testing.T) {
	// Regression for PR #763 review: KBArticleTags previously counted
	// only article.Tags, so tags_all-only articles were invisible to
	// the capability surface. Both lists must contribute, with within-
	// article dedup so tags appearing in both lists count once.
	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "or.md"),
		[]byte("---\ntags: [forge, ha]\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "and-only.md"),
		[]byte("---\ntags_all: [owner, signal_channel]\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "mixed.md"),
		[]byte("---\ntags: [forge]\ntags_all: [forge, owner]\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewTagContextAssembler(TagContextAssemblerConfig{KBDir: kbDir})
	counts := a.KBArticleTags()

	expect := map[string]int{
		"forge":          2, // or.md + mixed.md (mixed has forge in both lists, dedup → 1)
		"ha":             1, // or.md
		"owner":          2, // and-only.md + mixed.md
		"signal_channel": 1, // and-only.md
	}
	for tag, want := range expect {
		if counts[tag] != want {
			t.Errorf("counts[%q] = %d, want %d (full counts: %v)", tag, counts[tag], want, counts)
		}
	}
	if len(counts) != len(expect) {
		t.Errorf("unexpected extra tags in counts: %v", counts)
	}
}
