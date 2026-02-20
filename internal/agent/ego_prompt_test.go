package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
