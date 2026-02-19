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

func TestBuildSystemPrompt_NoEgoFile(t *testing.T) {
	l := newMinimalLoop()
	// egoFile not set — default empty string

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "ego.md") {
		t.Error("system prompt should not contain ego section when egoFile is not set")
	}
}
