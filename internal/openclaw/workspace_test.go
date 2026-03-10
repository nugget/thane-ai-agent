package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWorkspaceFiles_MainSession(t *testing.T) {
	dir := t.TempDir()

	// Create workspace files.
	writeFile(t, dir, "AGENTS.md", "# Agents rules")
	writeFile(t, dir, "SOUL.md", "# Soul persona")
	writeFile(t, dir, "TOOLS.md", "# Tool notes")
	writeFile(t, dir, "IDENTITY.md", "# Identity")
	writeFile(t, dir, "USER.md", "# User context")
	writeFile(t, dir, "HEARTBEAT.md", "# Heartbeat tasks")
	writeFile(t, dir, "MEMORY.md", "# Long-term memory")

	files := LoadWorkspaceFiles(dir, false, 20000)

	// Expect all main-session files in order.
	want := []string{"AGENTS.md", "SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md", "MEMORY.md"}
	got := names(files)

	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("files[%d] = %q, want %q", i, got[i], name)
		}
	}

	// Verify content is loaded.
	if files[0].Content != "# Agents rules" {
		t.Errorf("AGENTS.md content = %q, want %q", files[0].Content, "# Agents rules")
	}
}

func TestLoadWorkspaceFiles_SubagentFiltering(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "AGENTS.md", "# Agents")
	writeFile(t, dir, "SOUL.md", "# Soul")
	writeFile(t, dir, "TOOLS.md", "# Tools")
	writeFile(t, dir, "USER.md", "# User")
	writeFile(t, dir, "MEMORY.md", "# Memory")

	files := LoadWorkspaceFiles(dir, true, 20000)

	// Subagent only gets AGENTS.md and TOOLS.md.
	got := names(files)
	want := []string{"AGENTS.md", "TOOLS.md"}

	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("files[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestLoadWorkspaceFiles_MissingFiles(t *testing.T) {
	dir := t.TempDir()

	// Only AGENTS.md exists — others should be marked missing.
	writeFile(t, dir, "AGENTS.md", "# Agents")

	files := LoadWorkspaceFiles(dir, false, 20000)

	var missingNames []string
	for _, f := range files {
		if f.Missing {
			missingNames = append(missingNames, f.Name)
		}
	}

	// SOUL.md, TOOLS.md, IDENTITY.md, USER.md, HEARTBEAT.md should be missing.
	// BOOTSTRAP.md is optional (mustExist=false) so it should be skipped entirely.
	// MEMORY.md only loads if it exists, so it should also be absent.
	wantMissing := []string{"SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md"}
	if len(missingNames) != len(wantMissing) {
		t.Fatalf("missing files = %v, want %v", missingNames, wantMissing)
	}
}

func TestLoadWorkspaceFiles_EmptyFileSkipped(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "AGENTS.md", "# Agents")
	writeFile(t, dir, "SOUL.md", "   \n\t  ") // whitespace-only

	files := LoadWorkspaceFiles(dir, false, 20000)

	// SOUL.md should be treated as missing (empty content).
	for _, f := range files {
		if f.Name == "SOUL.md" && !f.Missing {
			t.Error("SOUL.md with whitespace-only content should be treated as missing")
		}
	}
}

func TestLoadWorkspaceFiles_MemoryMdCaseFallback(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "AGENTS.md", "# Agents")
	writeFile(t, dir, "memory.md", "# lowercase memory")

	files := LoadWorkspaceFiles(dir, false, 20000)

	// On case-insensitive filesystems (macOS), MEMORY.md check may match
	// the lowercase file. Accept either casing — the key behavior is that
	// the memory content is loaded exactly once.
	var found bool
	for _, f := range files {
		if f.Name == "memory.md" || f.Name == "MEMORY.md" {
			if f.Content != "# lowercase memory" {
				t.Errorf("memory file content = %q", f.Content)
			}
			if found {
				t.Error("memory file loaded more than once")
			}
			found = true
		}
	}
	if !found {
		t.Error("memory file should be loaded when only memory.md exists")
	}
}

func TestTruncateFile_Short(t *testing.T) {
	content := "short content"
	got := TruncateFile(content, 100)
	if got != content {
		t.Errorf("TruncateFile should not modify short content")
	}
}

func TestTruncateFile_Long(t *testing.T) {
	// Create content that's exactly 1000 chars.
	content := strings.Repeat("x", 1000)
	maxChars := 200

	got := TruncateFile(content, maxChars)

	if len(got) > maxChars {
		t.Errorf("truncated length %d exceeds maxChars %d", len(got), maxChars)
	}
	if !strings.Contains(got, "[... content truncated ...]") {
		t.Error("truncated content should contain truncation marker")
	}
	// Should start with x's and end with x's.
	if !strings.HasPrefix(got, "xxx") {
		t.Error("truncated content should start with head portion")
	}
	if !strings.HasSuffix(got, "xxx") {
		t.Error("truncated content should end with tail portion")
	}
}

func TestTruncateFile_PreservesRatio(t *testing.T) {
	content := strings.Repeat("H", 500) + strings.Repeat("T", 500)
	maxChars := 300

	got := TruncateFile(content, maxChars)
	marker := "[... content truncated ...]"
	idx := strings.Index(got, marker)
	if idx < 0 {
		t.Fatal("missing truncation marker")
	}

	head := got[:idx]
	tail := got[idx+len(marker):]

	// Head should be ~70% and tail ~20% of the budget.
	// With marker overhead, verify head > tail (rough ratio check).
	if len(head) < len(tail) {
		t.Errorf("head (%d) should be larger than tail (%d)", len(head), len(tail))
	}
}

// --- helpers ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(files []WorkspaceFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Name
	}
	return out
}
