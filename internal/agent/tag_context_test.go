package agent

import (
	"context"
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

func TestBuildSystemPrompt_TagContextIncluded(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "arch.md")
	f2 := filepath.Join(dir, "style.md")
	os.WriteFile(f1, []byte("# Architecture\nUse hexagonal pattern."), 0644)
	os.WriteFile(f2, []byte("# Style Guide\nTabs not spaces."), 0644)

	l := newTagTestLoop()
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
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
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
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
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
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
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
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
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
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
	l.SetCapabilityTags(map[string]config.CapabilityTagConfig{
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
