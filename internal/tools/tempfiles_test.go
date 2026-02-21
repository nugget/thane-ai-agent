package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/opstate"
)

func testTempFileStore(t *testing.T) (*TempFileStore, *opstate.Store) {
	t.Helper()
	state, err := opstate.NewStore(filepath.Join(t.TempDir(), "opstate_test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { state.Close() })

	baseDir := filepath.Join(t.TempDir(), ".tmp")
	return NewTempFileStore(baseDir, state, nil), state
}

func TestTempFileStore_Create(t *testing.T) {
	tfs, state := testTempFileStore(t)

	label, err := tfs.Create(context.Background(), "conv-1", "issue_body", "# Bug Report\n\nDetails here.")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if label != "issue_body" {
		t.Errorf("label = %q, want %q", label, "issue_body")
	}

	// Verify file exists on disk.
	path, err := state.Get(tempfileNamespace("conv-1"), "issue_body")
	if err != nil {
		t.Fatalf("Get mapping: %v", err)
	}
	if path == "" {
		t.Fatal("label→path mapping not stored in opstate")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "# Bug Report\n\nDetails here." {
		t.Errorf("file content = %q, want %q", string(data), "# Bug Report\n\nDetails here.")
	}
}

func TestTempFileStore_Create_InvalidLabel(t *testing.T) {
	tfs, _ := testTempFileStore(t)

	tests := []struct {
		name  string
		label string
	}{
		{"empty", ""},
		{"starts_with_hyphen", "-foo"},
		{"starts_with_underscore", "_foo"},
		{"special_chars", "foo@bar"},
		{"spaces", "foo bar"},
		{"too_long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tfs.Create(context.Background(), "conv-1", tt.label, "content")
			if err == nil {
				t.Errorf("expected error for label %q, got nil", tt.label)
			}
		})
	}
}

func TestTempFileStore_Create_ValidLabels(t *testing.T) {
	tfs, _ := testTempFileStore(t)

	tests := []struct {
		name  string
		label string
	}{
		{"simple", "issue_body"},
		{"with_hyphens", "review-comments"},
		{"numeric_start", "1st_draft"},
		{"single_char", "x"},
		{"max_length", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, err := tfs.Create(context.Background(), "conv-1", tt.label, "content")
			if err != nil {
				t.Errorf("unexpected error for label %q: %v", tt.label, err)
			}
			if label != tt.label {
				t.Errorf("label = %q, want %q", label, tt.label)
			}
		})
	}
}

func TestTempFileStore_Create_Overwrite(t *testing.T) {
	tfs, state := testTempFileStore(t)

	_, err := tfs.Create(context.Background(), "conv-1", "draft", "version 1")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	oldPath, _ := state.Get(tempfileNamespace("conv-1"), "draft")

	_, err = tfs.Create(context.Background(), "conv-1", "draft", "version 2")
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	newPath, _ := state.Get(tempfileNamespace("conv-1"), "draft")

	// Old file should be removed.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old file was not removed on overwrite")
	}

	// New file should contain updated content.
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "version 2" {
		t.Errorf("content = %q, want %q", string(data), "version 2")
	}
}

func TestTempFileStore_ExpandLabels(t *testing.T) {
	tfs, state := testTempFileStore(t)

	// Store mappings directly in opstate.
	ns := tempfileNamespace("conv-1")
	_ = state.Set(ns, "issue_body", "/workspace/.tmp/conv-1_issue_body_abcd.md")
	_ = state.Set(ns, "review", "/workspace/.tmp/conv-1_review_ef01.md")

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			"single_label",
			"Read temp:issue_body and summarize it",
			"Read /workspace/.tmp/conv-1_issue_body_abcd.md and summarize it",
		},
		{
			"two_labels",
			"Compare temp:issue_body with temp:review",
			"Compare /workspace/.tmp/conv-1_issue_body_abcd.md with /workspace/.tmp/conv-1_review_ef01.md",
		},
		{
			"no_labels",
			"Just a regular task description",
			"Just a regular task description",
		},
		{
			"unknown_label",
			"Read temp:nonexistent and process it",
			"Read temp:nonexistent and process it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tfs.ExpandLabels("conv-1", tt.text)
			if got != tt.want {
				t.Errorf("ExpandLabels(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestTempFileStore_ExpandLabels_OverlappingLabels(t *testing.T) {
	tfs, state := testTempFileStore(t)

	// Store labels where one is a prefix of the other.
	ns := tempfileNamespace("conv-1")
	_ = state.Set(ns, "a", "/path/to/a.md")
	_ = state.Set(ns, "ab", "/path/to/ab.md")

	// "temp:ab" must expand to the longer label's path, not "temp:a"+"b".
	got := tfs.ExpandLabels("conv-1", "Read temp:ab and also temp:a")
	want := "Read /path/to/ab.md and also /path/to/a.md"
	if got != want {
		t.Errorf("ExpandLabels with overlapping labels:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestTempFileStore_ExpandLabels_NoLabels(t *testing.T) {
	tfs, _ := testTempFileStore(t)

	text := "No labels here at all"
	got := tfs.ExpandLabels("conv-1", text)
	if got != text {
		t.Errorf("ExpandLabels = %q, want unchanged %q", got, text)
	}
}

func TestTempFileStore_Cleanup(t *testing.T) {
	tfs, state := testTempFileStore(t)

	// Create two temp files.
	_, _ = tfs.Create(context.Background(), "conv-1", "file_a", "aaa")
	_, _ = tfs.Create(context.Background(), "conv-1", "file_b", "bbb")

	// Verify files exist.
	ns := tempfileNamespace("conv-1")
	mappings, _ := state.List(ns)
	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(mappings))
	}

	pathA := mappings["file_a"]
	pathB := mappings["file_b"]

	if err := tfs.Cleanup("conv-1"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Files should be deleted.
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Error("file_a was not deleted")
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Error("file_b was not deleted")
	}

	// Opstate entries should be deleted.
	remaining, _ := state.List(ns)
	if len(remaining) != 0 {
		t.Errorf("expected 0 opstate entries after cleanup, got %d", len(remaining))
	}
}

func TestTempFileStore_Cleanup_Empty(t *testing.T) {
	tfs, _ := testTempFileStore(t)

	// Should not error when no temp files exist.
	if err := tfs.Cleanup("nonexistent-conv"); err != nil {
		t.Errorf("Cleanup on empty conversation: %v", err)
	}
}

func TestTempFileStore_ConversationIsolation(t *testing.T) {
	tfs, state := testTempFileStore(t)

	_, _ = tfs.Create(context.Background(), "conv-1", "shared_name", "conv1 data")
	_, _ = tfs.Create(context.Background(), "conv-2", "shared_name", "conv2 data")

	// Each conversation should have its own mapping.
	path1, _ := state.Get(tempfileNamespace("conv-1"), "shared_name")
	path2, _ := state.Get(tempfileNamespace("conv-2"), "shared_name")
	if path1 == path2 {
		t.Error("different conversations should have different file paths")
	}

	// Cleaning up conv-1 should not affect conv-2.
	_ = tfs.Cleanup("conv-1")
	path2After, _ := state.Get(tempfileNamespace("conv-2"), "shared_name")
	if path2After != path2 {
		t.Error("cleanup of conv-1 affected conv-2 mapping")
	}
}

func TestTempFileStore_Resolve(t *testing.T) {
	tfs, _ := testTempFileStore(t)

	_, _ = tfs.Create(context.Background(), "conv-1", "my_file", "content here")

	path := tfs.Resolve("conv-1", "my_file")
	if path == "" {
		t.Fatal("Resolve returned empty for existing label")
	}

	missing := tfs.Resolve("conv-1", "nonexistent")
	if missing != "" {
		t.Errorf("Resolve returned %q for nonexistent label", missing)
	}
}

func TestSanitizeForFilesystem(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple-id", "simple-id"},
		{"conv/with:colons", "conv_with_colons"},
		{"has spaces", "has_spaces"},
		{"UPPER-lower_123", "UPPER-lower_123"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeForFilesystem(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeForFilesystem(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeForFilesystem_Truncation(t *testing.T) {
	long := ""
	for range 100 {
		long += "a"
	}
	got := sanitizeForFilesystem(long)
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
}
