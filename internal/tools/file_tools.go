package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// errResultLimit is a sentinel returned from WalkDir callbacks to stop
// traversal when the result cap is reached.
var errResultLimit = errors.New("result limit reached")

// errVisitedLimit is a sentinel returned when the traversal visits more
// entries than maxVisited, indicating an unexpectedly large directory tree.
var errVisitedLimit = errors.New("visited limit reached")

// searchTimeout bounds how long Search and Grep may spend walking the
// file tree. Matches the default shell_exec timeout.
const searchTimeout = 30 * time.Second

// defaultMaxVisited caps the total number of directory entries visited
// (not just matches) to bail out of unexpectedly large trees early.
const defaultMaxVisited = 50_000

// skipDirs contains directory names that are skipped during file tree
// traversal. These are known to be large and rarely contain files the
// agent should search.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"vendor":       true,
	"__pycache__":  true,
	".syncthing":   true,
	".stversions":  true,
	".Trash":       true,
	".cache":       true,
}

// PathVerifier reports whether a path may be exposed through model-facing file
// tools. Implementations must treat paths outside any configured root as a
// no-op (nil error). The concrete implementation is
// [documents.Store.VerifyPath]; this interface keeps file_tools free of an
// upward dependency on the documents package.
//
// Verifier is consulted in [FileTools.Read], [FileTools.Write], and
// [FileTools.Edit] so a managed root with `verify_signatures: required`
// blocks the model from bypassing the doc store via raw filesystem
// access. Write/Edit use VerifyMutationPath rather than VerifyPath so
// raw filesystem mutations cannot dirty signed or read-only document
// roots after a pre-write trust check passes.
//
// Every file surface is gated. Indexed managed documents are a logical model
// surface, so file tools must not expose their private Markdown codec, sibling
// files, or physical section layout. Direct access returns an actionable
// doc-tool redirect; workspace-wide walks skip managed subtrees.
type PathVerifier interface {
	VerifyPath(ctx context.Context, path string, consumer string) error
	VerifyMutationPath(ctx context.Context, path string, consumer string) error
}

// FileTools provides file read/write/edit capabilities within a workspace.
type FileTools struct {
	workspacePath string
	readOnlyDirs  []string        // Additional read-only directories
	resolver      *paths.Resolver // Shared prefix resolver (kb:, scratchpad:, etc.)
	verifier      PathVerifier    // Optional doc-root signature verifier
	maxVisited    int             // Traversal entry cap; 0 uses defaultMaxVisited
}

// NewFileTools creates a new FileTools instance.
// If workspacePath is empty, file tools will be disabled.
func NewFileTools(workspacePath string, readOnlyDirs []string) *FileTools {
	return &FileTools{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

// visitedLimit returns the effective max-visited cap, falling back to
// defaultMaxVisited when no override is set.
func (ft *FileTools) visitedLimit() int {
	if ft.maxVisited > 0 {
		return ft.maxVisited
	}
	return defaultMaxVisited
}

// Enabled reports whether file tools are available.
func (ft *FileTools) Enabled() bool {
	return ft.workspacePath != ""
}

// WorkspacePath returns the configured workspace path.
func (ft *FileTools) WorkspacePath() string {
	return ft.workspacePath
}

// SetResolver configures the shared path prefix resolver for
// directory-based prefixes (kb:, scratchpad:, etc.). When set, prefixed
// paths are expanded to their configured directories before sandbox
// checks.
func (ft *FileTools) SetResolver(r *paths.Resolver) {
	ft.resolver = r
}

// SetPathVerifier installs the doc-root signature verifier consulted
// by Read/Write/Edit. A nil verifier disables verification. Paths
// outside any managed doc root are passthrough — the verifier itself
// is responsible for that classification.
func (ft *FileTools) SetPathVerifier(v PathVerifier) {
	ft.verifier = v
}

// verifyPath delegates to the installed verifier when set. Returns
// nil when no verifier is configured so workspaces without doc roots
// keep the original behavior.
func (ft *FileTools) verifyPath(ctx context.Context, absPath, consumer string) error {
	if ft.verifier == nil {
		return nil
	}
	return ft.verifier.VerifyPath(ctx, absPath, consumer)
}

// verifyMutationPath delegates raw mutation checks to the installed
// verifier when set. Read verification asks whether the current content
// is trusted; mutation verification asks whether this tool is allowed
// to change the path outside the managed document writer.
func (ft *FileTools) verifyMutationPath(ctx context.Context, absPath, consumer string) error {
	if ft.verifier == nil {
		return nil
	}
	return ft.verifier.VerifyMutationPath(ctx, absPath, consumer)
}

// resolvePath converts a relative path to an absolute path within allowed
// directories. A repo_root binding changes the default base from the workspace
// to that repository root and refuses every other root. Returns the resolved
// path and whether it is read-only.
func (ft *FileTools) resolvePath(ctx context.Context, path string) (string, bool, error) {
	if ft.workspacePath == "" {
		return "", false, fmt.Errorf("workspace not configured")
	}

	boundName := looppkg.BindingFromContext(ctx, looppkg.BindingRepositoryRoot)
	var boundRoot paths.Root
	if boundName != "" {
		var ok bool
		boundRoot, ok = ft.resolver.Root(boundName)
		if !ok || boundRoot.Kind != paths.RootKindRepository {
			return "", false, fmt.Errorf("this loop is bound to repository root %q, but that root is not registered at this site", boundName)
		}
	}

	// Resolve registered prefixes (kb:, scratchpad:, repository roots, etc.).
	var matchedRoot paths.Root
	var matched bool
	if ft.resolver != nil {
		resolved, root, ok := ft.resolver.ResolveRoot(path)
		if ok {
			if boundName != "" && root.Name != boundRoot.Name {
				return "", false, fmt.Errorf("named root %q is not available here: this loop is bound to repository root %q, and repo_root restricts all file tools to that repository; use an unbound loop to access %q", root.Name, boundRoot.Name, root.Name)
			}
			path, matchedRoot, matched = resolved, root, true
		} else if boundName != "" && !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
			path, matchedRoot, matched = filepath.Join(boundRoot.Path, path), boundRoot, true
		}
	} else if hasPrefixColon(path) {
		return "", false, fmt.Errorf("no path resolver configured for prefixed path: %s", path)
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

	if boundName != "" && (!paths.ContainsPath(boundRoot.Path, absPath) || !paths.ContainsPath(boundRoot.Path, realPath)) {
		return "", false, fmt.Errorf("path is outside repository root %q; this loop's repo_root binding restricts file tools to root %q", boundRoot.Name, boundRoot.Name)
	}
	if matched && (!paths.ContainsPath(matchedRoot.Path, absPath) || !paths.ContainsPath(matchedRoot.Path, realPath)) {
		return "", false, fmt.Errorf("path escapes named root %q through a symlink", matchedRoot.Name)
	}

	readOnly := matched && matchedRoot.ReadOnly
	if root, ok := ft.resolver.RootForPath(realPath); ok && root.ReadOnly {
		readOnly = true
	}

	// A named root carries mutation policy, not filesystem admission. Keep the
	// workspace/read_only_dirs boundary independent so registering a legacy or
	// otherwise misplaced root cannot expand what file tools may read.
	workspaceAbs, err := filepath.Abs(ft.workspacePath)
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	if paths.ContainsPath(workspaceAbs, absPath) || paths.ContainsPath(workspaceAbs, realPath) {
		return realPath, readOnly, nil
	}

	// Check read-only directories
	for _, dir := range ft.readOnlyDirs {
		dirAbs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if paths.ContainsPath(dirAbs, absPath) || paths.ContainsPath(dirAbs, realPath) {
			return realPath, true, nil
		}
	}

	return "", false, fmt.Errorf("path escapes allowed directories: %s", path)
}

// Read reads the contents of a file.
func (ft *FileTools) Read(ctx context.Context, path string, offset, limit int) (string, error) {
	absPath, _, err := ft.resolvePath(ctx, path)
	if err != nil {
		return "", err
	}

	if err := ft.verifyPath(ctx, absPath, "file_tools_read"); err != nil {
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
		content = truncateUTF8(content, maxBytes) + "\n\n[... truncated, use offset/limit for more ...]"
	}

	return content, nil
}

// Write writes content to a file, creating directories as needed.
func (ft *FileTools) Write(ctx context.Context, path, content string) error {
	absPath, readOnly, err := ft.resolvePath(ctx, path)
	if err != nil {
		return err
	}
	if readOnly {
		return fmt.Errorf("path is read-only: %s", path)
	}

	if err := ft.verifyMutationPath(ctx, absPath, "file_tools_write"); err != nil {
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
	absPath, readOnly, err := ft.resolvePath(ctx, path)
	if err != nil {
		return err
	}
	if readOnly {
		return fmt.Errorf("path is read-only: %s", path)
	}

	if err := ft.verifyMutationPath(ctx, absPath, "file_tools_edit"); err != nil {
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
			return fmt.Errorf("old text not found in file (first 100 bytes: %q...)", truncateUTF8(oldText, 100))
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
	absPath, _, err := ft.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := ft.verifyPath(ctx, absPath, "file_tools_list"); err != nil {
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
// Results inside a named root use root-prefixed paths; other results are
// workspace-relative.
func (ft *FileTools) Search(ctx context.Context, dir, pattern string, maxDepth int) (string, error) {
	absDir, _, err := ft.resolvePath(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := ft.verifyPath(ctx, absDir, "file_tools_search"); err != nil {
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

	searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	const maxResults = 500
	var matches []string
	visited := 0
	limit := ft.visitedLimit()
	displayPath := ft.newDisplayPathFormatter()

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip inaccessible entries
		}
		if searchCtx.Err() != nil {
			return searchCtx.Err()
		}
		if path != absDir {
			if err := ft.verifyPath(searchCtx, path, "file_tools_search"); err != nil {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}

		visited++
		if visited > limit {
			return errVisitedLimit
		}

		// Skip known-heavy directories.
		if d.IsDir() && skipDirs[d.Name()] {
			return fs.SkipDir
		}

		// Enforce depth limit relative to the search root
		rel, _ := filepath.Rel(absDir, path)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() && depth >= maxDepth {
			return fs.SkipDir
		}

		// Only match files, not directories
		if d.IsDir() {
			return nil
		}

		matched, _ := filepath.Match(pattern, d.Name())
		if matched {
			matches = append(matches, displayPath(path))
			if len(matches) >= maxResults {
				return errResultLimit
			}
		}
		return nil
	})

	// Build a warning suffix for partial results.
	var warning string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		warning = "\n\n[⚠️ search timed out — results are partial, try a narrower directory]"
	case errors.Is(err, context.Canceled):
		warning = "\n\n[⚠️ search was canceled — results may be incomplete]"
	case errors.Is(err, errVisitedLimit):
		warning = fmt.Sprintf("\n\n[⚠️ visited %d entries without finishing — results are partial, try a narrower directory]", visited)
	case errors.Is(err, errResultLimit):
		warning = fmt.Sprintf("\n\n[... truncated at %d results ...]", maxResults)
	case err != nil:
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(matches) == 0 {
		msg := "No files matching pattern: " + pattern
		if warning != "" {
			msg += warning
		}
		return msg, nil
	}

	return strings.Join(matches, "\n") + warning, nil
}

// Grep searches file contents for a regular expression pattern. When
// filePattern is non-empty, only basenames matching that glob are read.
// Results are formatted as path:line_number:matching_line.
func (ft *FileTools) Grep(ctx context.Context, dir, pattern, filePattern string, maxDepth int, caseInsensitive bool) (string, error) {
	absDir, _, err := ft.resolvePath(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := ft.verifyPath(ctx, absDir, "file_tools_grep"); err != nil {
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
	if filePattern != "" {
		if _, err := filepath.Match(filePattern, "test"); err != nil {
			return "", fmt.Errorf("invalid file_pattern glob: %w", err)
		}
	}

	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxDepth > 20 {
		maxDepth = 20
	}

	grepCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	const (
		maxMatches  = 100
		maxFileSize = 1 << 20 // 1MB
	)

	var results []string
	matchCount := 0
	visited := 0
	limit := ft.visitedLimit()
	displayPath := ft.newDisplayPathFormatter()

	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if grepCtx.Err() != nil {
			return grepCtx.Err()
		}
		if path != absDir {
			if err := ft.verifyPath(grepCtx, path, "file_tools_grep"); err != nil {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}

		visited++
		if visited > limit {
			return errVisitedLimit
		}

		// Skip known-heavy directories.
		if d.IsDir() && skipDirs[d.Name()] {
			return fs.SkipDir
		}

		rel, _ := filepath.Rel(absDir, path)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() && depth >= maxDepth {
			return fs.SkipDir
		}

		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if filePattern != "" {
			matched, _ := filepath.Match(filePattern, d.Name())
			if !matched {
				return nil
			}
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

		formattedPath := displayPath(path)

		scanner := bufio.NewScanner(bytes.NewReader(data))
		// Increase buffer to handle long lines up to the file-size cap.
		scanner.Buffer(make([]byte, 0, 64*1024), maxFileSize)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				// Truncate very long matching lines
				if len(line) > 200 {
					line = truncateUTF8(line, 200) + "..."
				}
				results = append(results, fmt.Sprintf("%s:%d:%s", formattedPath, lineNum, line))
				matchCount++
				if matchCount >= maxMatches {
					return errResultLimit
				}
			}
		}
		// scanner.Err() is non-nil if scanning stopped due to an error
		// other than EOF (e.g., token too long). Safe to ignore here
		// since we sized the buffer to the file-size cap.

		return nil
	})

	// Build a warning suffix for partial results.
	var warning string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		warning = "\n\n[⚠️ grep timed out — results are partial, try a narrower directory]"
	case errors.Is(err, context.Canceled):
		warning = "\n\n[⚠️ grep was canceled — results may be incomplete]"
	case errors.Is(err, errVisitedLimit):
		warning = fmt.Sprintf("\n\n[⚠️ visited %d entries without finishing — results are partial, try a narrower directory]", visited)
	case errors.Is(err, errResultLimit):
		warning = fmt.Sprintf("\n\n[... truncated at %d matches ...]", maxMatches)
	case err != nil:
		return "", fmt.Errorf("grep failed: %w", err)
	}

	if len(results) == 0 {
		msg := "No matches for pattern: " + pattern
		if warning != "" {
			msg += warning
		}
		return msg, nil
	}

	return strings.Join(results, "\n") + warning, nil
}

type displayRoot struct {
	name string
	path string
}

// newDisplayPathFormatter snapshots and canonicalizes the small root roster
// once per search. Match formatting then needs only lexical filepath.Rel calls
// instead of resolving symlinks for every root for every matched file.
func (ft *FileTools) newDisplayPathFormatter() func(string) string {
	roots := make([]displayRoot, 0)
	for _, name := range ft.resolver.Prefixes() {
		root, ok := ft.resolver.Root(name)
		if !ok {
			continue
		}
		roots = append(roots, displayRoot{name: root.Name, path: canonicalDisplayBase(root.Path)})
	}
	sort.Slice(roots, func(i, j int) bool {
		if len(roots[i].path) == len(roots[j].path) {
			return roots[i].name < roots[j].name
		}
		return len(roots[i].path) > len(roots[j].path)
	})
	workspace := canonicalDisplayBase(ft.workspacePath)

	return func(path string) string {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return path
		}
		for _, root := range roots {
			if rel, contained := lexicalRelativePath(root.path, absPath); contained {
				if rel == "." {
					return root.name + ":"
				}
				return root.name + ":" + filepath.ToSlash(rel)
			}
		}
		if rel, contained := lexicalRelativePath(workspace, absPath); contained {
			return filepath.ToSlash(rel)
		}
		return path
	}
}

func canonicalDisplayBase(path string) string {
	absPath, err := filepath.Abs(paths.ExpandHome(path))
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved
	}
	return absPath
}

func lexicalRelativePath(root, candidate string) (string, bool) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// Stat returns detailed information about one or more files or directories.
// Paths should be comma-separated. Each path is resolved through the workspace sandbox.
func (ft *FileTools) Stat(ctx context.Context, paths string) (string, error) {
	if ft.workspacePath == "" {
		return "", fmt.Errorf("workspace not configured")
	}

	pathList := strings.Split(paths, ",")

	var results []string
	now := time.Now()
	for _, p := range pathList {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		absPath, _, err := ft.resolvePath(ctx, p)
		if err != nil {
			results = append(results, fmt.Sprintf("%s: %s", p, err))
			continue
		}
		if err := ft.verifyPath(ctx, absPath, "file_tools_stat"); err != nil {
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
			"%s: type=%s size=%s permissions=%s modified_delta=%s",
			p, kind, humanSize(info.Size()), info.Mode().Perm(), promptfmt.FormatDeltaOnly(info.ModTime(), now),
		))
	}

	return strings.Join(results, "\n"), nil
}

// Tree renders a directory tree with indentation.
// The output includes a summary of total directories and files.
func (ft *FileTools) Tree(ctx context.Context, dir string, maxDepth int) (string, error) {
	absDir, _, err := ft.resolvePath(ctx, dir)
	if err != nil {
		return "", err
	}
	if err := ft.verifyPath(ctx, absDir, "file_tools_tree"); err != nil {
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
		result = truncateUTF8(result, maxBytes) + "\n\n[... truncated ...]"
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

	visible := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entryPath := filepath.Join(dir, entry.Name())
		if err := ft.verifyPath(ctx, entryPath, "file_tools_tree"); err != nil {
			continue
		}
		visible = append(visible, entry)
	}

	for i, entry := range visible {
		entryPath := filepath.Join(dir, entry.Name())
		isLast := i == len(visible)-1
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
			err := ft.renderTree(buf, entryPath, prefix+childPrefix, maxDepth, currentDepth+1, dirCount, fileCount, ctx)
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

// hasPrefixColon detects paths that look like named prefix references
// (e.g., "kb:foo" or "kb:") but are not absolute paths or Windows drive
// letters. Single-character prefixes are excluded to avoid matching drive
// letters like "C:\".
func hasPrefixColon(path string) bool {
	i := strings.IndexByte(path, ':')
	return i > 1 && !strings.ContainsAny(path[:i], "/\\")
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
