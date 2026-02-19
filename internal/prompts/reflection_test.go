package prompts

import (
	"strings"
	"testing"
)

func TestPeriodicReflectionPrompt_EmptyContent(t *testing.T) {
	got := PeriodicReflectionPrompt("")

	if !strings.Contains(got, "does not exist yet") {
		t.Error("expected placeholder text for missing ego.md")
	}
	if !strings.Contains(got, "periodic reflection") {
		t.Error("expected 'periodic reflection' in prompt")
	}
}

func TestPeriodicReflectionPrompt_WithContent(t *testing.T) {
	content := "# My Reflections\n\nI've been learning about the home."
	got := PeriodicReflectionPrompt(content)

	if !strings.Contains(got, content) {
		t.Error("expected ego.md content to appear in prompt")
	}
	if strings.Contains(got, "does not exist yet") {
		t.Error("placeholder should not appear when content is provided")
	}
}

func TestPeriodicReflectionPrompt_ContainsKeyPhrases(t *testing.T) {
	got := PeriodicReflectionPrompt("test content")

	phrases := []string{
		"periodic reflection",
		"ego.md",
		"Current ego.md",
		"curiosity",
	}
	for _, phrase := range phrases {
		if !strings.Contains(got, phrase) {
			t.Errorf("prompt missing expected phrase %q", phrase)
		}
	}
}
