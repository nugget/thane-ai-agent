package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newMinimalLoop() *Loop {
	return &Loop{
		logger: slog.Default(),
	}
}

func TestBuildSystemPrompt_EgoFileIncluded(t *testing.T) {
	dir := t.TempDir()
	egoPath := filepath.Join(dir, "ego.md")
	content := "# Self-Reflection\n\nI notice the lights change at sunset."
	if err := os.WriteFile(egoPath, []byte(content), 0644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}

	l := newMinimalLoop()
	l.SetEgoFile(egoPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, content) {
		t.Error("system prompt should contain ego.md content")
	}
	if !strings.Contains(prompt, "Self-Reflection (ego.md)") {
		t.Error("system prompt should contain ego section heading")
	}
}

func TestBuildSystemPrompt_EgoFileMissing(t *testing.T) {
	l := newMinimalLoop()
	l.SetEgoFile(filepath.Join(t.TempDir(), "nonexistent.md"))

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "ego.md") {
		t.Error("system prompt should not contain ego section when file is missing")
	}
}

func TestBuildSystemPrompt_EgoFileEmpty(t *testing.T) {
	dir := t.TempDir()
	egoPath := filepath.Join(dir, "ego.md")
	if err := os.WriteFile(egoPath, []byte(""), 0644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}

	l := newMinimalLoop()
	l.SetEgoFile(egoPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "ego.md") {
		t.Error("system prompt should not contain ego section when file is empty")
	}
}

func TestBuildSystemPrompt_EgoFileTruncated(t *testing.T) {
	dir := t.TempDir()
	egoPath := filepath.Join(dir, "ego.md")

	// Create content larger than maxEgoBytes (16 KB).
	big := strings.Repeat("x", maxEgoBytes+1000)
	if err := os.WriteFile(egoPath, []byte(big), 0644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}

	l := newMinimalLoop()
	l.SetEgoFile(egoPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "truncated") {
		t.Error("system prompt should contain truncation marker for oversized ego.md")
	}
	if strings.Contains(prompt, big) {
		t.Error("system prompt should not contain full oversized content")
	}
}

func TestBuildSystemPrompt_NoEgoFile(t *testing.T) {
	l := newMinimalLoop()
	// egoFile not set — default empty string

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "ego.md") {
		t.Error("system prompt should not contain ego section when egoFile is not set")
	}
}

func TestBuildSystemPrompt_RuntimeContractIncluded(t *testing.T) {
	l := newMinimalLoop()

	prompt := l.buildSystemPrompt(context.Background(), "summarize kb:article.md", nil)

	if !strings.Contains(prompt, "## Runtime Contract") {
		t.Fatal("system prompt should contain runtime contract section")
	}
	if !strings.Contains(prompt, "Use only exact tool names") {
		t.Fatal("runtime contract should teach exact tool naming")
	}
	if !strings.Contains(prompt, "Keep the straight path clean") {
		t.Fatal("runtime contract should keep direct answers prominent")
	}
	if !strings.Contains(prompt, "`thane_now`") {
		t.Fatal("runtime contract should mention delegation when top-level tools are gated")
	}
}

// --- inject_files tests ---

func TestBuildSystemPrompt_InjectFilesIncluded(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "MEMORY.md")
	f2 := filepath.Join(dir, "USER.md")
	os.WriteFile(f1, []byte("# Shared Memory\nkey fact"), 0644)
	os.WriteFile(f2, []byte("# User Notes\npreference"), 0644)

	l := newMinimalLoop()
	l.SetInjectFiles([]string{f1, f2})

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "key fact") {
		t.Error("system prompt should contain first inject file content")
	}
	if !strings.Contains(prompt, "preference") {
		t.Error("system prompt should contain second inject file content")
	}
	if !strings.Contains(prompt, "Injected Context") {
		t.Error("system prompt should contain injected context section heading")
	}
	if !strings.Contains(prompt, "---") {
		t.Error("system prompt should contain separator between inject files")
	}
}

type rejectingInjectFileVerifier struct{}

func (rejectingInjectFileVerifier) VerifyPath(context.Context, string, string) error {
	return errors.New("untrusted inject file")
}

func TestBuildSystemPrompt_InjectFilesVerifierBlocks(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "MEMORY.md")
	if err := os.WriteFile(f, []byte("blocked context"), 0644); err != nil {
		t.Fatalf("write inject file: %v", err)
	}

	l := newMinimalLoop()
	l.SetInjectFiles([]string{f})
	l.UseInjectFileVerifier(rejectingInjectFileVerifier{}.VerifyPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "blocked context") {
		t.Fatal("system prompt should not contain content rejected by the inject-file verifier")
	}
	if strings.Contains(prompt, "Injected Context") {
		t.Fatal("system prompt should not include injected context section when all files are rejected")
	}
}

func TestBuildSystemPrompt_InjectFilesRereadOnChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "MEMORY.md")
	os.WriteFile(f, []byte("version-1"), 0644)

	l := newMinimalLoop()
	l.SetInjectFiles([]string{f})

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

func TestBuildSystemPrompt_InjectFilesMissing(t *testing.T) {
	l := newMinimalLoop()
	l.SetInjectFiles([]string{filepath.Join(t.TempDir(), "nonexistent.md")})

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "Injected Context") {
		t.Error("system prompt should not contain injected context section when all files are missing")
	}
}

func TestBuildSystemPrompt_InjectFilesEmpty(t *testing.T) {
	l := newMinimalLoop()
	// injectFiles not set — default nil

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "Injected Context") {
		t.Error("system prompt should not contain injected context section when no files configured")
	}
}

func TestBuildSystemPrompt_TaskModeOmitsIdentityContinuityContext(t *testing.T) {
	dir := t.TempDir()
	egoPath := filepath.Join(dir, "ego.md")
	injectPath := filepath.Join(dir, "metacognitive.md")
	if err := os.WriteFile(egoPath, []byte("EGO_MARKER"), 0o644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}
	if err := os.WriteFile(injectPath, []byte("INJECT_MARKER"), 0o644); err != nil {
		t.Fatalf("write inject file: %v", err)
	}

	l := newMinimalLoop()
	l.persona = "PERSONA_MARKER"
	l.SetEgoFile(egoPath)
	l.SetInjectFiles([]string{injectPath})

	ctx := agentctx.WithPromptMode(context.Background(), agentctx.PromptModeTask)
	history := []memory.Message{{Role: "user", Content: "HISTORY_MARKER"}}
	prompt := l.buildSystemPrompt(ctx, "hello", history)

	for _, marker := range []string{
		"PERSONA_MARKER",
		"EGO_MARKER",
		"INJECT_MARKER",
		"HISTORY_MARKER",
		"Self-Reflection (ego.md)",
		"Injected Context",
		"Conversation History",
	} {
		if strings.Contains(prompt, marker) {
			t.Fatalf("task prompt contains %q:\n%s", marker, prompt)
		}
	}
	for _, marker := range []string{
		"bounded task worker",
		"## Runtime Contract",
		"Current Conditions",
	} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("task prompt missing %q:\n%s", marker, prompt)
		}
	}
}
