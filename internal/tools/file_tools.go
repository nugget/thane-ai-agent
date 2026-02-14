// Package tools provides file operation tools for the agent.
package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FileTools provides file read/write/edit capabilities within a workspace.
type FileTools struct {
	workspacePath string
	readOnlyDirs  []string // Additional read-only directories
}

// NewFileTools creates a new FileTools instance.
// If workspacePath is empty, file tools will be disabled.
func NewFileTools(workspacePath string, readOnlyDirs []string) *FileTools {
	return &FileTools{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

// Enabled reports whether file tools are available.
func (ft *FileTools) Enabled() bool {
	return ft.workspacePath != ""
}

// WorkspacePath returns the configured workspace path.
func (ft *FileTools) WorkspacePath() string {
	return ft.workspacePath
}

// resolvePath converts a relative path to an absolute path within allowed directories.
// Returns the resolved path and whether it's read-only.
func (ft *FileTools) resolvePath(path string) (string, bool, error) {
	if ft.workspacePath == "" {
		return "", false, fmt.Errorf("workspace not configured")
	}

	// Expand ~ to home directory
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[1:])
		}
	}

	// Clean and resolve the path
	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath = filepath.Clean(filepath.Join(ft.workspacePath, path))
	}

	// Resolve symlinks to get the real path
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// File might not exist yet (for writes) — check parent
		parentReal, perr := filepath.EvalSymlinks(filepath.Dir(absPath))
		if perr != nil {
			realPath = absPath // Fall through to directory checks
		} else {
			realPath = filepath.Join(parentReal, filepath.Base(absPath))
		}
	}

	// Check workspace (read-write)
	workspaceAbs, err := filepath.Abs(ft.workspacePath)
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	if strings.HasPrefix(absPath, workspaceAbs) || strings.HasPrefix(realPath, workspaceAbs) {
		return realPath, false, nil
	}

	// Check read-only directories
	for _, dir := range ft.readOnlyDirs {
		dirAbs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, dirAbs) || strings.HasPrefix(realPath, dirAbs) {
			return realPath, true, nil
		}
	}

	return "", false, fmt.Errorf("path escapes allowed directories: %s", path)
}

// Read reads the contents of a file.
func (ft *FileTools) Read(ctx context.Context, path string, offset, limit int) (string, error) {
	absPath, _, err := ft.resolvePath(path)
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
	absPath, readOnly, err := ft.resolvePath(path)
	if err != nil {
		return err
	}
	if readOnly {
		return fmt.Errorf("path is read-only: %s", path)
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
	absPath, readOnly, err := ft.resolvePath(path)
	if err != nil {
		return err
	}
	if readOnly {
		return fmt.Errorf("path is read-only: %s", path)
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
	absPath, _, err := ft.resolvePath(path)
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

// Search finds files matching a glob pattern within a directory tree.
// Results are returned as workspace-relative paths, one per line.
func (ft *FileTools) Search(ctx context.Context, dir, pattern string, maxDepth int) (string, error) {
	absDir, _, err := ft.resolvePath(dir)
	if err != nil {
		return "", err
	}

	if _, err := filepath.Match(pattern, "test"); err != nil {
		return "", fmt.Errorf("invalid glob pattern: %w", err)
	}

	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxDepth > 20 {
		maxDepth = 20
	}

	workspaceAbs, err := filepath.Abs(ft.workspacePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace: %w", err)
	}

	const maxResults = 500
	var matches []string

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip inaccessible entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Enforce depth limit relative to the search root
		rel, _ := filepath.Rel(absDir, path)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() && depth >= maxDepth {
			return fs.SkipDir
		}

		matched, _ := filepath.Match(pattern, d.Name())
		if matched {
			displayPath := path
			if r, err := filepath.Rel(workspaceAbs, path); err == nil {
				displayPath = r
			}
			matches = append(matches, displayPath)
			if len(matches) >= maxResults {
				return fmt.Errorf("result limit reached")
			}
		}
		return nil
	})

	// Swallow the sentinel error from result limiting
	if err != nil && err.Error() != "result limit reached" && err != context.Canceled {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(matches) == 0 {
		return "No files matching pattern: " + pattern, nil
	}

	result := strings.Join(matches, "\n")
	if len(matches) >= maxResults {
		result += fmt.Sprintf("\n\n[... truncated at %d results ...]", maxResults)
	}
	return result, nil
}

// Grep searches file contents for a regular expression pattern.
// Results are formatted as path:line_number:matching_line.
func (ft *FileTools) Grep(ctx context.Context, dir, pattern string, maxDepth int, caseInsensitive bool) (string, error) {
	absDir, _, err := ft.resolvePath(dir)
	if err != nil {
		return "", err
	}

	regexPattern := pattern
	if caseInsensitive {
		regexPattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxDepth > 20 {
		maxDepth = 20
	}

	workspaceAbs, err := filepath.Abs(ft.workspacePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace: %w", err)
	}

	const (
		maxMatches  = 100
		maxFileSize = 1 << 20 // 1MB
	)

	var results []string
	matchCount := 0

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, _ := filepath.Rel(absDir, path)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() && depth >= maxDepth {
			return fs.SkipDir
		}

		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}

		// Skip large files
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Skip binary files (check first 512 bytes for null bytes)
		probe := data
		if len(probe) > 512 {
			probe = probe[:512]
		}
		if bytes.ContainsRune(probe, 0) {
			return nil
		}

		displayPath := path
		if r, err := filepath.Rel(workspaceAbs, path); err == nil {
			displayPath = r
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				// Truncate very long matching lines
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				results = append(results, fmt.Sprintf("%s:%d:%s", displayPath, lineNum, line))
				matchCount++
				if matchCount >= maxMatches {
					return fmt.Errorf("match limit reached")
				}
			}
		}

		return nil
	})

	if err != nil && err.Error() != "match limit reached" && err != context.Canceled {
		return "", fmt.Errorf("grep failed: %w", err)
	}

	if len(results) == 0 {
		return "No matches for pattern: " + pattern, nil
	}

	result := strings.Join(results, "\n")
	if matchCount >= maxMatches {
		result += fmt.Sprintf("\n\n[... truncated at %d matches ...]", maxMatches)
	}
	return result, nil
}

// Stat returns detailed information about one or more files or directories.
// Paths should be comma-separated. Each path is resolved through the workspace sandbox.
func (ft *FileTools) Stat(ctx context.Context, paths string) (string, error) {
	if ft.workspacePath == "" {
		return "", fmt.Errorf("workspace not configured")
	}

	pathList := strings.Split(paths, ",")

	var results []string
	for _, p := range pathList {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		absPath, _, err := ft.resolvePath(p)
		if err != nil {
			results = append(results, fmt.Sprintf("%s: %s", p, err))
			continue
		}

		info, err := os.Lstat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				results = append(results, fmt.Sprintf("%s: not found", p))
			} else {
				results = append(results, fmt.Sprintf("%s: %s", p, err))
			}
			continue
		}

		kind := "file"
		if info.IsDir() {
			kind = "directory"
		} else if info.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
		}

		results = append(results, fmt.Sprintf(
			"%s: type=%s size=%s permissions=%s modified=%s",
			p, kind, humanSize(info.Size()), info.Mode().Perm(), info.ModTime().Format(time.RFC3339),
		))
	}

	return strings.Join(results, "\n"), nil
}

// Tree renders a directory tree with indentation.
// The output includes a summary of total directories and files.
func (ft *FileTools) Tree(ctx context.Context, dir string, maxDepth int) (string, error) {
	absDir, _, err := ft.resolvePath(dir)
	if err != nil {
		return "", err
	}

	if maxDepth <= 0 {
		maxDepth = 3
	}
	if maxDepth > 10 {
		maxDepth = 10
	}

	var buf strings.Builder
	dirCount := 0
	fileCount := 0

	// Write root directory name
	displayRoot := dir
	if dir == "" || dir == "." {
		displayRoot = filepath.Base(absDir)
	}
	buf.WriteString(displayRoot + "/\n")

	err = ft.renderTree(&buf, absDir, "", maxDepth, 0, &dirCount, &fileCount, ctx)
	if err != nil && err != context.Canceled {
		return "", fmt.Errorf("tree failed: %w", err)
	}

	buf.WriteString(fmt.Sprintf("\n%d directories, %d files", dirCount, fileCount))

	result := buf.String()
	const maxBytes = 50 * 1024
	if len(result) > maxBytes {
		result = result[:maxBytes] + "\n\n[... truncated ...]"
	}

	return result, nil
}

// renderTree recursively renders directory entries with tree-style indentation.
func (ft *FileTools) renderTree(buf *strings.Builder, dir, prefix string, maxDepth, currentDepth int, dirCount, fileCount *int, ctx context.Context) error {
	if currentDepth >= maxDepth {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // skip unreadable directories
	}

	for i, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		isLast := i == len(entries)-1
		connector := "├── "
		childPrefix := "│   "
		if isLast {
			connector = "└── "
			childPrefix = "    "
		}

		name := entry.Name()
		if entry.IsDir() {
			name += "/"
			*dirCount++
			buf.WriteString(prefix + connector + name + "\n")
			err := ft.renderTree(buf, filepath.Join(dir, entry.Name()), prefix+childPrefix, maxDepth, currentDepth+1, dirCount, fileCount, ctx)
			if err != nil {
				return err
			}
		} else {
			*fileCount++
			buf.WriteString(prefix + connector + name + "\n")
		}
	}
	return nil
}

// humanSize formats a byte count into a human-readable string.
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
