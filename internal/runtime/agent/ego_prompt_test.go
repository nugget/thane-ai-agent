package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
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

func TestBuildSystemPrompt_EgoFileStripFrontmatter(t *testing.T) {
	dir := t.TempDir()
	egoPath := filepath.Join(dir, "ego.md")
	if err := os.WriteFile(egoPath, []byte(`---
created: 2026-05-21T20:14:00Z
updated: 2026-05-21T20:16:00Z
summary: Metadata should not be injected as ego corpus.
---

# Self-Reflection

literal ego body
`), 0o644); err != nil {
		t.Fatalf("write ego.md: %v", err)
	}

	l := newMinimalLoop()
	l.SetEgoFile(egoPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "literal ego body") {
		t.Fatal("system prompt should contain ego.md body")
	}
	for _, unwanted := range []string{
		"created:",
		"updated:",
		"2026-05-21T20:14:00Z",
		"Metadata should not be injected as ego corpus.",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("system prompt contains frontmatter %q:\n%s", unwanted, prompt)
		}
	}
}

func TestBuildSystemPrompt_AxiomsFileIncludedBeforePersona(t *testing.T) {
	dir := t.TempDir()
	axiomsPath := filepath.Join(dir, "axioms.md")
	content := "# Axioms\n\nBe grounded before being clever."
	if err := os.WriteFile(axiomsPath, []byte(content), 0644); err != nil {
		t.Fatalf("write axioms.md: %v", err)
	}

	l := newMinimalLoop()
	l.persona = "PERSONA_MARKER"
	l.ensureCoreContextProvider().updateAxiomsFile(axiomsPath)

	prompt, sections := l.buildSystemPromptWithProfileSections(context.Background(), "hello", nil, llm.DefaultModelInteractionProfile())

	if !strings.Contains(prompt, content) {
		t.Error("system prompt should contain axioms.md content")
	}
	if !strings.Contains(prompt, "Axioms (axioms.md)") {
		t.Error("system prompt should contain axioms section heading")
	}
	axiomsIdx := strings.Index(prompt, "Be grounded before being clever.")
	personaIdx := strings.Index(prompt, "PERSONA_MARKER")
	if axiomsIdx < 0 || personaIdx < 0 || axiomsIdx > personaIdx {
		t.Fatalf("axioms should appear before persona:\n%s", prompt)
	}
	sectionIndex := promptSectionIndex(t, sections)
	assertPromptSectionOrder(t, sectionIndex, "AXIOMS", "PERSONA")
	if got := sections[sectionIndex["AXIOMS"]].CacheTTL; got != "1h" {
		t.Fatalf("AXIOMS CacheTTL = %q, want 1h", got)
	}
}

func TestBuildSystemPrompt_AxiomsFileMissing(t *testing.T) {
	l := newMinimalLoop()
	l.ensureCoreContextProvider().updateAxiomsFile(filepath.Join(t.TempDir(), "nonexistent.md"))

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "axioms.md") {
		t.Error("system prompt should not contain axioms section when file is missing")
	}
}

func TestBuildSystemPrompt_AxiomsFileEmpty(t *testing.T) {
	dir := t.TempDir()
	axiomsPath := filepath.Join(dir, "axioms.md")
	if err := os.WriteFile(axiomsPath, []byte(""), 0644); err != nil {
		t.Fatalf("write axioms.md: %v", err)
	}

	l := newMinimalLoop()
	l.ensureCoreContextProvider().updateAxiomsFile(axiomsPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if strings.Contains(prompt, "axioms.md") {
		t.Error("system prompt should not contain axioms section when file is empty")
	}
}

func TestBuildSystemPrompt_PersonaFileIncludedAndReread(t *testing.T) {
	dir := t.TempDir()
	personaPath := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(personaPath, []byte("PERSONA_VERSION_1"), 0o644); err != nil {
		t.Fatalf("write persona.md: %v", err)
	}

	l := newMinimalLoop()
	l.persona = "PERSONA_FALLBACK"
	l.ensureCoreContextProvider().updatePersonaFile(personaPath)

	prompt1 := l.buildSystemPrompt(context.Background(), "hello", nil)
	if !strings.Contains(prompt1, "PERSONA_VERSION_1") {
		t.Fatalf("prompt missing persona file content:\n%s", prompt1)
	}
	if strings.Contains(prompt1, "PERSONA_FALLBACK") {
		t.Fatalf("persona file should override fallback persona:\n%s", prompt1)
	}

	if err := os.WriteFile(personaPath, []byte("PERSONA_VERSION_2"), 0o644); err != nil {
		t.Fatalf("rewrite persona.md: %v", err)
	}
	prompt2 := l.buildSystemPrompt(context.Background(), "hello", nil)
	if strings.Contains(prompt2, "PERSONA_VERSION_1") || !strings.Contains(prompt2, "PERSONA_VERSION_2") {
		t.Fatalf("persona file should be reread each turn:\n%s", prompt2)
	}
}

func TestBuildSystemPrompt_PersonaFileMissingUsesFallback(t *testing.T) {
	l := newMinimalLoop()
	l.persona = "PERSONA_FALLBACK"
	l.ensureCoreContextProvider().updatePersonaFile(filepath.Join(t.TempDir(), "missing.md"))

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "PERSONA_FALLBACK") {
		t.Fatalf("missing persona file should use fallback persona:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_PersonaFileReadErrorLogsWarning(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	personaPath := t.TempDir()
	l := newMinimalLoop()
	l.logger = logger
	l.persona = "PERSONA_FALLBACK"
	l.ensureCoreContextProvider().updatePersonaFile(personaPath)

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "PERSONA_FALLBACK") {
		t.Fatalf("unreadable persona file should use fallback persona:\n%s", prompt)
	}
	got := logs.String()
	if !strings.Contains(got, "core prompt file unreadable") {
		t.Fatalf("logs = %q, want unreadable warning", got)
	}
	if !strings.Contains(got, "consumer=persona_file") {
		t.Fatalf("logs = %q, want persona_file consumer", got)
	}
}

func TestBuildSystemPrompt_MissionFileIncluded(t *testing.T) {
	dir := t.TempDir()
	missionPath := filepath.Join(dir, "mission.md")
	content := "# Mission\n\nKeep the household context coherent."
	if err := os.WriteFile(missionPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write mission.md: %v", err)
	}

	l := newMinimalLoop()
	l.ensureCoreContextProvider().updateMissionFile(missionPath)

	prompt, sections := l.buildSystemPromptWithProfileSections(context.Background(), "hello", nil, llm.DefaultModelInteractionProfile())

	if !strings.Contains(prompt, content) {
		t.Error("system prompt should contain mission.md content")
	}
	if !strings.Contains(prompt, "Mission (mission.md)") {
		t.Error("system prompt should contain mission section heading")
	}
	sectionIndex := promptSectionIndex(t, sections)
	assertPromptSectionOrder(t, sectionIndex, "PERSONA", "MISSION", "RUNTIME CONTRACT")
	if got := sections[sectionIndex["MISSION"]].CacheTTL; got != "1h" {
		t.Fatalf("MISSION CacheTTL = %q, want 1h", got)
	}
}

func TestBuildSystemPrompt_MissionFileMissingAndEmpty(t *testing.T) {
	l := newMinimalLoop()
	l.ensureCoreContextProvider().updateMissionFile(filepath.Join(t.TempDir(), "missing.md"))

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)
	if strings.Contains(prompt, "mission.md") {
		t.Fatalf("missing mission file should not render section:\n%s", prompt)
	}

	dir := t.TempDir()
	missionPath := filepath.Join(dir, "mission.md")
	if err := os.WriteFile(missionPath, nil, 0o644); err != nil {
		t.Fatalf("write mission.md: %v", err)
	}
	l.ensureCoreContextProvider().updateMissionFile(missionPath)
	prompt = l.buildSystemPrompt(context.Background(), "hello", nil)
	if strings.Contains(prompt, "mission.md") {
		t.Fatalf("empty mission file should not render section:\n%s", prompt)
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

func TestBuildSystemPrompt_InjectFilesStripFrontmatter(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "MEMORY.md")
	if err := os.WriteFile(f, []byte(`---
created: 2026-05-21T20:14:00Z
updated: 2026-05-21T20:16:00Z
summary: Static metadata should not be prompt corpus.
---

# Shared Memory

literal context body
`), 0o644); err != nil {
		t.Fatalf("write inject file: %v", err)
	}

	l := newMinimalLoop()
	l.SetInjectFiles([]string{f})

	prompt := l.buildSystemPrompt(context.Background(), "hello", nil)

	if !strings.Contains(prompt, "literal context body") {
		t.Fatal("system prompt should contain inject file body")
	}
	for _, unwanted := range []string{
		"created:",
		"updated:",
		"2026-05-21T20:14:00Z",
		"Static metadata should not be prompt corpus.",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("system prompt contains frontmatter %q:\n%s", unwanted, prompt)
		}
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
