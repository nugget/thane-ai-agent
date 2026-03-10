package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// WorkspaceFile represents a loaded workspace bootstrap file.
type WorkspaceFile struct {
	// Name is the filename (e.g., "AGENTS.md").
	Name string

	// Path is the path to the file on disk, constructed by joining the
	// workspace directory and Name. It is absolute only when the directory
	// passed to LoadWorkspaceFiles is absolute.
	Path string

	// Content is the file content, possibly truncated.
	Content string

	// Missing is true when the file does not exist on disk or is
	// effectively missing (empty or whitespace-only content).
	Missing bool
}

// bootstrapEntry defines a workspace file to load.
type bootstrapEntry struct {
	name      string
	mainOnly  bool // if true, only loaded in main (non-subagent) sessions
	mustExist bool // if false, silently skip when missing instead of marking [MISSING]
}

// bootstrapOrder is the fixed file injection order matching OpenClaw v2026.2.9.
// See openclaw/src/agents/workspace.ts loadWorkspaceBootstrapFiles().
var bootstrapOrder = []bootstrapEntry{
	{name: "AGENTS.md", mainOnly: false, mustExist: true},
	{name: "SOUL.md", mainOnly: true, mustExist: true},
	{name: "TOOLS.md", mainOnly: false, mustExist: true},
	{name: "IDENTITY.md", mainOnly: true, mustExist: true},
	{name: "USER.md", mainOnly: true, mustExist: true},
	{name: "HEARTBEAT.md", mainOnly: true, mustExist: true},
	{name: "BOOTSTRAP.md", mainOnly: true, mustExist: false},
	// MEMORY.md is appended separately in LoadWorkspaceFiles: only loaded
	// when it exists on disk, never marked [MISSING]. This matches OC's
	// treatment of memory as optional bootstrapping context.
}

// LoadWorkspaceFiles reads OpenClaw bootstrap files from dir in the
// canonical injection order. When subagent is true, only AGENTS.md and
// TOOLS.md are loaded (matching OpenClaw's SUBAGENT_BOOTSTRAP_ALLOWLIST).
//
// Files that do not exist are included with Missing=true and a placeholder
// content, unless they are optional (mustExist=false), in which case they
// are silently omitted. Files exceeding maxChars are truncated using the
// 70/20 head/tail strategy.
func LoadWorkspaceFiles(dir string, subagent bool, maxChars int) []WorkspaceFile {
	if maxChars <= 0 {
		maxChars = 20000
	}

	var files []WorkspaceFile

	for _, entry := range bootstrapOrder {
		if subagent && entry.mainOnly {
			continue
		}

		fp := filepath.Join(dir, entry.name)
		wf := loadOneFile(fp, entry.name, maxChars)

		if wf.Missing && !entry.mustExist {
			continue // optional file, skip silently
		}
		files = append(files, wf)
	}

	// MEMORY.md — only in main sessions, only if it exists.
	// Check both casings but stop after the first hit to avoid
	// loading the same file twice on case-insensitive filesystems.
	if !subagent {
		for _, name := range []string{"MEMORY.md", "memory.md"} {
			fp := filepath.Join(dir, name)
			if _, err := os.Stat(fp); err == nil {
				wf := loadOneFile(fp, name, maxChars)
				if !wf.Missing {
					files = append(files, wf)
					break
				}
			}
		}
	}

	return files
}

// loadOneFile reads a single file and returns a WorkspaceFile.
func loadOneFile(path, name string, maxChars int) WorkspaceFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkspaceFile{
			Name:    name,
			Path:    path,
			Content: "[MISSING] Expected at: " + path,
			Missing: true,
		}
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		// Empty files are skipped (matching OC's buildBootstrapContextFiles).
		return WorkspaceFile{
			Name:    name,
			Path:    path,
			Content: "",
			Missing: true, // treat as missing so callers can filter
		}
	}

	if len(content) > maxChars {
		content = TruncateFile(content, maxChars)
	}

	return WorkspaceFile{
		Name:    name,
		Path:    path,
		Content: content,
	}
}

// TruncateFile truncates content to approximately maxChars (byte count)
// using OpenClaw's 70/20 head/tail strategy: keep 70% from the start and
// 20% from the end, with a truncation marker in between (consuming the
// remaining 10%). Slice points are adjusted to avoid splitting UTF-8 runes.
func TruncateFile(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}

	const marker = "\n\n[... content truncated ...]\n\n"

	// If maxChars is too small to fit the marker, just return the marker.
	available := maxChars - len(marker)
	if available <= 0 {
		return marker[:maxChars]
	}

	headChars := available * 7 / 9
	tailChars := available - headChars

	// Adjust slice points to rune boundaries.
	head := truncateToRuneBoundary(content, headChars)
	tail := truncateFromRuneBoundary(content, tailChars)
	return head + marker + tail
}

// truncateToRuneBoundary returns the longest prefix of s that is at most
// n bytes and does not split a UTF-8 rune.
func truncateToRuneBoundary(s string, n int) string {
	if n >= len(s) {
		return s
	}
	// Walk back from n until we're at the start of a rune.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// truncateFromRuneBoundary returns the longest suffix of s that is at most
// n bytes and does not split a UTF-8 rune.
func truncateFromRuneBoundary(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	// Walk forward until we're at the start of a rune.
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
