// Package tools provides file operation tools for the agent.
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileTools provides file read/write/edit capabilities within a workspace.
type FileTools struct {
	workspacePath string
}

// NewFileTools creates a new FileTools instance.
// If workspacePath is empty, file tools will be disabled.
func NewFileTools(workspacePath string) *FileTools {
	return &FileTools{workspacePath: workspacePath}
}

// Enabled returns true if file tools are available.
func (ft *FileTools) Enabled() bool {
	return ft.workspacePath != ""
}

// WorkspacePath returns the configured workspace path.
func (ft *FileTools) WorkspacePath() string {
	return ft.workspacePath
}

// resolvePath converts a relative path to an absolute path within the workspace.
// Returns an error if the path would escape the workspace.
func (ft *FileTools) resolvePath(path string) (string, error) {
	if ft.workspacePath == "" {
		return "", fmt.Errorf("workspace not configured")
	}

	// Clean and resolve the path
	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath = filepath.Clean(filepath.Join(ft.workspacePath, path))
	}

	// Ensure the path is within the workspace
	workspaceAbs, err := filepath.Abs(ft.workspacePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace: %w", err)
	}

	// Check that absPath starts with workspace path
	if !strings.HasPrefix(absPath, workspaceAbs) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}

	return absPath, nil
}

// Read reads the contents of a file.
func (ft *FileTools) Read(ctx context.Context, path string, offset, limit int) (string, error) {
	absPath, err := ft.resolvePath(path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", path)
		}
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	content := string(data)

	// Apply offset and limit if specified (line-based)
	if offset > 0 || limit > 0 {
		lines := strings.Split(content, "\n")

		// Convert 1-indexed offset to 0-indexed
		startLine := 0
		if offset > 0 {
			startLine = offset - 1
		}
		if startLine >= len(lines) {
			return "", fmt.Errorf("offset %d exceeds file length (%d lines)", offset, len(lines))
		}

		endLine := len(lines)
		if limit > 0 && startLine+limit < endLine {
			endLine = startLine + limit
		}

		content = strings.Join(lines[startLine:endLine], "\n")

		// Add line info if truncated
		if startLine > 0 || endLine < len(lines) {
			content = fmt.Sprintf("[Lines %d-%d of %d]\n%s", startLine+1, endLine, len(lines), content)
		}
	}

	// Truncate very large content
	const maxBytes = 50 * 1024 // 50KB
	if len(content) > maxBytes {
		content = content[:maxBytes] + "\n\n[... truncated, use offset/limit for more ...]"
	}

	return content, nil
}

// Write writes content to a file, creating directories as needed.
func (ft *FileTools) Write(ctx context.Context, path, content string) error {
	absPath, err := ft.resolvePath(path)
	if err != nil {
		return err
	}

	// Create parent directories
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write the file
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// Edit performs a surgical text replacement in a file.
func (ft *FileTools) Edit(ctx context.Context, path, oldText, newText string) error {
	absPath, err := ft.resolvePath(path)
	if err != nil {
		return err
	}

	// Read current content
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", path)
		}
		return fmt.Errorf("failed to read file: %w", err)
	}

	content := string(data)

	// Find and replace
	if !strings.Contains(content, oldText) {
		// Provide helpful error with context
		if len(oldText) > 100 {
			return fmt.Errorf("old text not found in file (first 100 chars: %q...)", oldText[:100])
		}
		return fmt.Errorf("old text not found in file: %q", oldText)
	}

	// Count occurrences
	count := strings.Count(content, oldText)
	if count > 1 {
		return fmt.Errorf("old text appears %d times in file; must be unique for safe editing", count)
	}

	// Perform replacement
	newContent := strings.Replace(content, oldText, newText, 1)

	// Write back
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// List lists files in a directory.
func (ft *FileTools) List(ctx context.Context, path string) ([]string, error) {
	absPath, err := ft.resolvePath(path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory not found: %s", path)
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var result []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		result = append(result, name)
	}

	return result, nil
}
