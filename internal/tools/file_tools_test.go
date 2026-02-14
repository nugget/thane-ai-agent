package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileTools_ResolvePath(t *testing.T) {
	// Create temp workspace
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative path", "test.txt", false},
		{"nested path", "dir/subdir/file.txt", false},
		{"dot prefix", "./test.txt", false},
		{"parent escape attempt", "../outside.txt", true},
		{"absolute escape attempt", "/etc/passwd", true},
		{"sneaky escape", "dir/../../outside.txt", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ft.resolvePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolvePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestFileTools_ReadWriteEdit(t *testing.T) {
	// Create temp workspace
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	// Test write
	content := "Hello, World!\nLine 2\nLine 3"
	err = ft.Write(ctx, "test.txt", content)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(workspace, "test.txt")); err != nil {
		t.Fatalf("File not created: %v", err)
	}

	// Test read
	readContent, err := ft.Read(ctx, "test.txt", 0, 0)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if readContent != content {
		t.Errorf("Read content mismatch: got %q, want %q", readContent, content)
	}

	// Test read with offset/limit
	readContent, err = ft.Read(ctx, "test.txt", 2, 1)
	if err != nil {
		t.Fatalf("Read with offset failed: %v", err)
	}
	if readContent != "[Lines 2-2 of 3]\nLine 2" {
		t.Errorf("Read with offset mismatch: got %q", readContent)
	}

	// Test edit
	err = ft.Edit(ctx, "test.txt", "Line 2", "Modified Line 2")
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}

	// Verify edit
	readContent, err = ft.Read(ctx, "test.txt", 0, 0)
	if err != nil {
		t.Fatalf("Read after edit failed: %v", err)
	}
	expected := "Hello, World!\nModified Line 2\nLine 3"
	if readContent != expected {
		t.Errorf("Edit content mismatch: got %q, want %q", readContent, expected)
	}

	// Test edit with non-existent text
	err = ft.Edit(ctx, "test.txt", "NOT FOUND", "replacement")
	if err == nil {
		t.Error("Edit should fail for non-existent text")
	}
}

func TestFileTools_List(t *testing.T) {
	// Create temp workspace with structure
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	// Create test files and directories
	os.WriteFile(filepath.Join(workspace, "file1.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(workspace, "file2.md"), []byte("test"), 0644)
	os.MkdirAll(filepath.Join(workspace, "subdir"), 0755)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	entries, err := ft.List(ctx, ".")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// Should have 3 entries: file1.txt, file2.md, subdir/
	if len(entries) != 3 {
		t.Errorf("Expected 3 entries, got %d: %v", len(entries), entries)
	}

	// Check that directory has trailing slash
	hasSubdir := false
	for _, e := range entries {
		if e == "subdir/" {
			hasSubdir = true
		}
	}
	if !hasSubdir {
		t.Errorf("Expected 'subdir/' in entries: %v", entries)
	}
}

func TestFileTools_CreateNestedDirectories(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	// Write to deeply nested path
	err = ft.Write(ctx, "a/b/c/deep.txt", "deep content")
	if err != nil {
		t.Fatalf("Write to nested path failed: %v", err)
	}

	// Verify
	content, err := ft.Read(ctx, "a/b/c/deep.txt", 0, 0)
	if err != nil {
		t.Fatalf("Read nested file failed: %v", err)
	}
	if content != "deep content" {
		t.Errorf("Content mismatch: got %q", content)
	}
}

func TestFileTools_Disabled(t *testing.T) {
	ft := NewFileTools("", nil)

	if ft.Enabled() {
		t.Error("FileTools should be disabled with empty path")
	}

	ctx := context.Background()
	_, err := ft.Read(ctx, "test.txt", 0, 0)
	if err == nil {
		t.Error("Read should fail when disabled")
	}

	err = ft.Write(ctx, "test.txt", "content")
	if err == nil {
		t.Error("Write should fail when disabled")
	}

	err = ft.Edit(ctx, "test.txt", "old", "new")
	if err == nil {
		t.Error("Edit should fail when disabled")
	}

	_, err = ft.List(ctx, ".")
	if err == nil {
		t.Error("List should fail when disabled")
	}
}

func TestFileTools_ReadNonExistent(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	_, err = ft.Read(ctx, "does-not-exist.txt", 0, 0)
	if err == nil {
		t.Error("Read should fail for non-existent file")
	}
}

func TestFileTools_EditDuplicateText(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	// Write file with duplicate text
	content := "duplicate\nsome text\nduplicate\n"
	err = ft.Write(ctx, "dup.txt", content)
	if err != nil {
		t.Fatal(err)
	}

	// Edit should fail because "duplicate" appears twice
	err = ft.Edit(ctx, "dup.txt", "duplicate", "unique")
	if err == nil {
		t.Error("Edit should fail when old text appears multiple times")
	}
}

func TestFileTools_EditNonExistent(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	err = ft.Edit(ctx, "does-not-exist.txt", "old", "new")
	if err == nil {
		t.Error("Edit should fail for non-existent file")
	}
}

func TestFileTools_ListNonExistent(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	_, err = ft.List(ctx, "does-not-exist")
	if err == nil {
		t.Error("List should fail for non-existent directory")
	}
}

func TestFileTools_ReadOffsetBeyondFile(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	// Write 3-line file
	err = ft.Write(ctx, "short.txt", "line1\nline2\nline3")
	if err != nil {
		t.Fatal(err)
	}

	// Try to read from line 100
	_, err = ft.Read(ctx, "short.txt", 100, 0)
	if err == nil {
		t.Error("Read should fail when offset exceeds file length")
	}
}

func TestFileTools_OverwriteExisting(t *testing.T) {
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace, nil)
	ctx := context.Background()

	// Write initial content
	err = ft.Write(ctx, "overwrite.txt", "initial content")
	if err != nil {
		t.Fatal(err)
	}

	// Overwrite with new content
	err = ft.Write(ctx, "overwrite.txt", "new content")
	if err != nil {
		t.Fatal(err)
	}

	// Verify new content
	content, err := ft.Read(ctx, "overwrite.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if content != "new content" {
		t.Errorf("Expected 'new content', got %q", content)
	}
}

// setupSearchWorkspace creates a temp workspace with a file tree for search/grep/tree tests.
// Layout:
//
//	workspace/
//	  config.yaml
//	  readme.md
//	  src/
//	    main.go        (contains "func main()")
//	    util.go        (contains "TODO: refactor")
//	    data/
//	      data.json
//	      binary.dat   (contains null bytes)
func setupSearchWorkspace(t *testing.T) (string, *FileTools) {
	t.Helper()
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}

	dirs := []string{
		filepath.Join(workspace, "src"),
		filepath.Join(workspace, "src", "data"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	files := map[string][]byte{
		"config.yaml":         []byte("server:\n  port: 8080\n"),
		"readme.md":           []byte("# Project\nThis is a readme.\n"),
		"src/main.go":         []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"),
		"src/util.go":         []byte("package main\n\n// TODO: refactor this\nfunc helper() {}\n"),
		"src/data/data.json":  []byte(`{"key": "value"}`),
		"src/data/binary.dat": append([]byte("header"), 0, 0, 0),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	return workspace, NewFileTools(workspace, nil)
}

func TestFileTools_Search(t *testing.T) {
	workspace, ft := setupSearchWorkspace(t)
	defer os.RemoveAll(workspace)
	ctx := context.Background()

	tests := []struct {
		name      string
		dir       string
		pattern   string
		maxDepth  int
		wantCount int
		wantMatch string // substring expected in output
		wantErr   bool
	}{
		{
			name:      "find yaml files",
			dir:       ".",
			pattern:   "*.yaml",
			wantCount: 1,
			wantMatch: "config.yaml",
		},
		{
			name:      "find go files",
			dir:       "src",
			pattern:   "*.go",
			wantCount: 2,
			wantMatch: "main.go",
		},
		{
			name:      "find all json",
			dir:       ".",
			pattern:   "*.json",
			wantCount: 1,
			wantMatch: "data.json",
		},
		{
			name:      "no matches",
			dir:       ".",
			pattern:   "*.rs",
			wantCount: 0,
			wantMatch: "No files matching",
		},
		{
			name:      "depth limited",
			dir:       ".",
			pattern:   "*.json",
			maxDepth:  1,
			wantCount: 0,
			wantMatch: "No files matching",
		},
		{
			name:    "invalid pattern",
			dir:     ".",
			pattern: "[invalid",
			wantErr: true,
		},
		{
			name:    "path escape",
			dir:     "../outside",
			pattern: "*",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ft.Search(ctx, tt.dir, tt.pattern, tt.maxDepth)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Search() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !strings.Contains(result, tt.wantMatch) {
				t.Errorf("Search() result missing %q:\n%s", tt.wantMatch, result)
			}
			if tt.wantCount > 0 {
				lines := strings.Split(strings.TrimSpace(result), "\n")
				if len(lines) < tt.wantCount {
					t.Errorf("Search() got %d results, want at least %d", len(lines), tt.wantCount)
				}
			}
		})
	}
}

func TestFileTools_Grep(t *testing.T) {
	workspace, ft := setupSearchWorkspace(t)
	defer os.RemoveAll(workspace)
	ctx := context.Background()

	tests := []struct {
		name            string
		dir             string
		pattern         string
		maxDepth        int
		caseInsensitive bool
		wantMatch       string
		wantNoMatch     string
		wantErr         bool
	}{
		{
			name:      "find TODO comments",
			dir:       ".",
			pattern:   "TODO",
			wantMatch: "util.go:3:// TODO: refactor this",
		},
		{
			name:      "find func declarations",
			dir:       "src",
			pattern:   "^func ",
			wantMatch: "main.go",
		},
		{
			name:            "case insensitive",
			dir:             ".",
			pattern:         "project",
			caseInsensitive: true,
			wantMatch:       "readme.md",
		},
		{
			name:        "case sensitive no match",
			dir:         ".",
			pattern:     "project",
			wantNoMatch: "readme.md",
			wantMatch:   "No matches",
		},
		{
			name:        "skips binary files",
			dir:         ".",
			pattern:     "header",
			wantNoMatch: "binary.dat",
		},
		{
			name:      "no matches at all",
			dir:       ".",
			pattern:   "ZZZZNOTFOUND",
			wantMatch: "No matches",
		},
		{
			name:    "invalid regex",
			dir:     ".",
			pattern: "[invalid",
			wantErr: true,
		},
		{
			name:    "path escape",
			dir:     "../outside",
			pattern: "test",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ft.Grep(ctx, tt.dir, tt.pattern, tt.maxDepth, tt.caseInsensitive)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Grep() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantMatch != "" && !strings.Contains(result, tt.wantMatch) {
				t.Errorf("Grep() result missing %q:\n%s", tt.wantMatch, result)
			}
			if tt.wantNoMatch != "" && strings.Contains(result, tt.wantNoMatch) {
				t.Errorf("Grep() result should not contain %q:\n%s", tt.wantNoMatch, result)
			}
		})
	}
}

func TestFileTools_Stat(t *testing.T) {
	workspace, ft := setupSearchWorkspace(t)
	defer os.RemoveAll(workspace)
	ctx := context.Background()

	tests := []struct {
		name      string
		paths     string
		wantMatch string
		wantErr   bool
	}{
		{
			name:      "single file",
			paths:     "config.yaml",
			wantMatch: "type=file",
		},
		{
			name:      "directory",
			paths:     "src",
			wantMatch: "type=directory",
		},
		{
			name:      "multiple paths",
			paths:     "config.yaml, src, readme.md",
			wantMatch: "type=directory",
		},
		{
			name:      "non-existent",
			paths:     "does-not-exist.txt",
			wantMatch: "not found",
		},
		{
			name:    "path escape",
			paths:   "../outside",
			wantErr: false, // Stat reports errors inline, not as error return
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ft.Stat(ctx, tt.paths)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Stat() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !strings.Contains(result, tt.wantMatch) {
				t.Errorf("Stat() result missing %q:\n%s", tt.wantMatch, result)
			}
		})
	}

	// Verify batch returns one line per path
	t.Run("batch line count", func(t *testing.T) {
		result, err := ft.Stat(ctx, "config.yaml, src, readme.md")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(result), "\n")
		if len(lines) != 3 {
			t.Errorf("Stat() batch returned %d lines, want 3:\n%s", len(lines), result)
		}
	})
}

func TestFileTools_Tree(t *testing.T) {
	workspace, ft := setupSearchWorkspace(t)
	defer os.RemoveAll(workspace)
	ctx := context.Background()

	tests := []struct {
		name      string
		dir       string
		maxDepth  int
		wantMatch string
		wantErr   bool
	}{
		{
			name:      "default depth",
			dir:       ".",
			wantMatch: "directories",
		},
		{
			name:      "includes files",
			dir:       ".",
			wantMatch: "config.yaml",
		},
		{
			name:      "includes tree connectors",
			dir:       ".",
			wantMatch: "├──",
		},
		{
			name:      "subdirectory",
			dir:       "src",
			wantMatch: "main.go",
		},
		{
			name:     "depth 1 excludes nested",
			dir:      ".",
			maxDepth: 1,
		},
		{
			name:    "path escape",
			dir:     "../outside",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ft.Tree(ctx, tt.dir, tt.maxDepth)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Tree() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantMatch != "" && !strings.Contains(result, tt.wantMatch) {
				t.Errorf("Tree() result missing %q:\n%s", tt.wantMatch, result)
			}
		})
	}

	// Verify depth limiting excludes deeply nested files
	t.Run("depth limiting", func(t *testing.T) {
		result, err := ft.Tree(ctx, ".", 1)
		if err != nil {
			t.Fatal(err)
		}
		// At depth 1, we should see src/ but not main.go inside it
		if !strings.Contains(result, "src/") {
			t.Errorf("Tree(depth=1) should contain src/:\n%s", result)
		}
		if strings.Contains(result, "main.go") {
			t.Errorf("Tree(depth=1) should not contain main.go:\n%s", result)
		}
	})

	// Verify summary line format
	t.Run("summary counts", func(t *testing.T) {
		result, err := ft.Tree(ctx, ".", 10)
		if err != nil {
			t.Fatal(err)
		}
		// Should have "X directories, Y files" at the end
		if !strings.Contains(result, "2 directories") {
			t.Errorf("Tree() summary should show 2 directories:\n%s", result)
		}
		if !strings.Contains(result, "6 files") {
			t.Errorf("Tree() summary should show 6 files:\n%s", result)
		}
	})
}

func TestFileTools_SearchDisabled(t *testing.T) {
	ft := NewFileTools("", nil)
	ctx := context.Background()

	_, err := ft.Search(ctx, ".", "*.go", 0)
	if err == nil {
		t.Error("Search should fail when disabled")
	}

	_, err = ft.Grep(ctx, ".", "test", 0, false)
	if err == nil {
		t.Error("Grep should fail when disabled")
	}

	_, err = ft.Stat(ctx, "test.txt")
	if err == nil {
		t.Error("Stat should fail when disabled")
	}

	_, err = ft.Tree(ctx, ".", 0)
	if err == nil {
		t.Error("Tree should fail when disabled")
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := humanSize(tt.bytes)
			if got != tt.want {
				t.Errorf("humanSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
