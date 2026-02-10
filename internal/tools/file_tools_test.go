package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTools_ResolvePath(t *testing.T) {
	// Create temp workspace
	workspace, err := os.MkdirTemp("", "thane-file-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)

	ft := NewFileTools(workspace)

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
			_, err := ft.resolvePath(tt.path)
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

	ft := NewFileTools(workspace)
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

	ft := NewFileTools(workspace)
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

	ft := NewFileTools(workspace)
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
	ft := NewFileTools("")

	if ft.Enabled() {
		t.Error("FileTools should be disabled with empty path")
	}

	ctx := context.Background()
	_, err := ft.Read(ctx, "test.txt", 0, 0)
	if err == nil {
		t.Error("Read should fail when disabled")
	}
}
