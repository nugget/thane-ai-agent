package delegate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestExpandPathPrefixes(t *testing.T) {
	prefixes := map[string]string{
		"events": "~/Sync/Vault/Events",
		"tde":    "~/Sync/Vault/Events/TDE",
		"t":      "~/Sync/Vault/Temp",
	}

	tests := []struct {
		name     string
		toolName string
		argsJSON string
		want     string // expected "path" value after expansion
		noChange bool   // if true, argsJSON should be returned unchanged
	}{
		{
			name:     "basic prefix with subpath",
			toolName: "file_read",
			argsJSON: `{"path":"events/2023-06 MSRH.md"}`,
			want:     "~/Sync/Vault/Events/2023-06 MSRH.md",
		},
		{
			name:     "longer prefix matched first",
			toolName: "file_read",
			argsJSON: `{"path":"tde/session.md"}`,
			want:     "~/Sync/Vault/Events/TDE/session.md",
		},
		{
			name:     "short prefix still works",
			toolName: "file_list",
			argsJSON: `{"path":"t/scratch.txt"}`,
			want:     "~/Sync/Vault/Temp/scratch.txt",
		},
		{
			name:     "exact prefix without subpath",
			toolName: "file_list",
			argsJSON: `{"path":"events"}`,
			want:     "~/Sync/Vault/Events",
		},
		{
			name:     "absolute path unchanged",
			toolName: "file_read",
			argsJSON: `{"path":"/etc/hosts"}`,
			noChange: true,
		},
		{
			name:     "home-relative path unchanged",
			toolName: "file_read",
			argsJSON: `{"path":"~/Documents/foo.md"}`,
			noChange: true,
		},
		{
			name:     "unknown prefix unchanged",
			toolName: "file_read",
			argsJSON: `{"path":"unknown/foo.md"}`,
			noChange: true,
		},
		{
			name:     "non-path args untouched",
			toolName: "file_search",
			argsJSON: `{"pattern":"*.md","path":"events/sub"}`,
			want:     "~/Sync/Vault/Events/sub",
		},
		{
			name:     "empty path unchanged",
			toolName: "file_read",
			argsJSON: `{"path":""}`,
			noChange: true,
		},
		{
			name:     "no path key unchanged",
			toolName: "file_write",
			argsJSON: `{"content":"hello"}`,
			noChange: true,
		},
		{
			name:     "empty prefixes",
			toolName: "file_read",
			argsJSON: `{"path":"events/foo"}`,
			noChange: true,
		},
		{
			name:     "nested subpath",
			toolName: "file_read",
			argsJSON: `{"path":"tde/2023/06/report.md"}`,
			want:     "~/Sync/Vault/Events/TDE/2023/06/report.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := prefixes
			if tt.name == "empty prefixes" {
				p = nil
			}

			got := expandPathPrefixes(tt.toolName, tt.argsJSON, p)

			if tt.noChange {
				if got != tt.argsJSON {
					t.Errorf("expected no change, got %q", got)
				}
				return
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}
			pathVal, _ := result["path"].(string)
			if pathVal != tt.want {
				t.Errorf("path = %q, want %q", pathVal, tt.want)
			}
		})
	}
}

func TestExpandPathPrefixes_FileStat(t *testing.T) {
	prefixes := map[string]string{
		"events": "~/Sync/Vault/Events",
		"tde":    "~/Sync/Vault/Events/TDE",
	}

	tests := []struct {
		name      string
		argsJSON  string
		wantPaths string // expected "paths" value after expansion
		noChange  bool
	}{
		{
			name:      "single path expanded",
			argsJSON:  `{"paths":"events/foo.md"}`,
			wantPaths: "~/Sync/Vault/Events/foo.md",
		},
		{
			name:      "comma-separated paths expanded",
			argsJSON:  `{"paths":"events/a.md,tde/b.md"}`,
			wantPaths: "~/Sync/Vault/Events/a.md,~/Sync/Vault/Events/TDE/b.md",
		},
		{
			name:      "mixed known and unknown",
			argsJSON:  `{"paths":"events/a.md,unknown/b.md"}`,
			wantPaths: "~/Sync/Vault/Events/a.md,unknown/b.md",
		},
		{
			name:     "absolute paths unchanged",
			argsJSON: `{"paths":"/etc/hosts,~/foo"}`,
			noChange: true,
		},
		{
			name:      "spaces around commas trimmed",
			argsJSON:  `{"paths":"events/a.md, tde/b.md"}`,
			wantPaths: "~/Sync/Vault/Events/a.md,~/Sync/Vault/Events/TDE/b.md",
		},
		{
			name:     "empty paths unchanged",
			argsJSON: `{"paths":""}`,
			noChange: true,
		},
		{
			name:     "no paths key unchanged",
			argsJSON: `{"path":"events/foo.md"}`,
			noChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandPathPrefixes("file_stat", tt.argsJSON, prefixes)

			if tt.noChange {
				if got != tt.argsJSON {
					t.Errorf("expected no change, got %q", got)
				}
				return
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatalf("failed to unmarshal result: %v", err)
			}
			pathsVal, _ := result["paths"].(string)
			if pathsVal != tt.wantPaths {
				t.Errorf("paths = %q, want %q", pathsVal, tt.wantPaths)
			}
		})
	}
}

func TestExpandPrefix(t *testing.T) {
	prefixes := map[string]string{
		"a":  "/short",
		"ab": "/longer",
	}

	tests := []struct {
		path string
		want string
	}{
		{"ab/file.txt", "/longer/file.txt"},
		{"a/file.txt", "/short/file.txt"},
		{"abc/file.txt", "abc/file.txt"},
		{"ab", "/longer"},
		{"a", "/short"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := expandPrefix(tt.path, prefixes)
			if got != tt.want {
				t.Errorf("expandPrefix(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestExpandPrefix_TrailingSlash(t *testing.T) {
	prefixes := map[string]string{
		"docs": "/path/to/docs/",
	}

	tests := []struct {
		path string
		want string
	}{
		{"docs/file.md", "/path/to/docs/file.md"},
		{"docs", "/path/to/docs"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := expandPrefix(tt.path, prefixes)
			if got != tt.want {
				t.Errorf("expandPrefix(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestFormatPrefixPrompt(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := formatPrefixPrompt(nil)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"docs": "/nonexistent/path/abc123",
		})
		// No listing section when directory doesn't exist.
		want := "Path prefixes available:\n  docs/ → /nonexistent/path/abc123/\nUse these prefixes at the start of file tool paths instead of full paths."
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("multiple sorted", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"z": "/nonexistent/z-path",
			"a": "/nonexistent/a-path",
		})
		if got == "" {
			t.Fatal("expected non-empty prompt")
		}
		// "a" should come before "z" in the output.
		aIdx := strings.Index(got, "  a/")
		zIdx := strings.Index(got, "  z/")
		if aIdx < 0 || zIdx < 0 {
			t.Fatalf("expected both prefixes in output:\n%s", got)
		}
		if aIdx >= zIdx {
			t.Errorf("expected 'a' before 'z' in output:\n%s", got)
		}
	})

	t.Run("trailing slash normalized", func(t *testing.T) {
		// Input has trailing slash — output should have exactly one.
		got := formatPrefixPrompt(map[string]string{
			"docs": "/nonexistent/Documents/",
		})
		want := "Path prefixes available:\n  docs/ → /nonexistent/Documents/\nUse these prefixes at the start of file tool paths instead of full paths."
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("directory listing included", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/alpha.md", []byte("a"), 0o644)
		os.WriteFile(dir+"/beta.txt", []byte("b"), 0o644)
		os.Mkdir(dir+"/subdir", 0o755)

		got := formatPrefixPrompt(map[string]string{
			"vault": dir,
		})

		// Should contain the mapping header.
		if !strings.Contains(got, "vault/ → "+dir) {
			t.Errorf("missing prefix mapping in output:\n%s", got)
		}

		// Should contain a listing section.
		if !strings.Contains(got, "vault/ contents:") {
			t.Errorf("missing contents section in output:\n%s", got)
		}

		// Entries should be listed.
		if !strings.Contains(got, "  alpha.md") {
			t.Errorf("missing alpha.md in listing:\n%s", got)
		}
		if !strings.Contains(got, "  beta.txt") {
			t.Errorf("missing beta.txt in listing:\n%s", got)
		}
		// Directories get trailing slash.
		if !strings.Contains(got, "  subdir/") {
			t.Errorf("missing subdir/ in listing:\n%s", got)
		}
	})

	t.Run("listing capped at max entries", func(t *testing.T) {
		dir := t.TempDir()
		for i := range maxPrefixEntries + 10 {
			os.WriteFile(fmt.Sprintf("%s/file_%03d.txt", dir, i), []byte("x"), 0o644)
		}

		got := formatPrefixPrompt(map[string]string{
			"big": dir,
		})

		if !strings.Contains(got, "... (list truncated") {
			t.Errorf("expected truncation notice in output:\n%s", got)
		}

		// Count listed entries (lines starting with two spaces under
		// the contents header, excluding the truncation line).
		lines := strings.Split(got, "\n")
		var entryCount int
		inContents := false
		for _, line := range lines {
			if strings.HasSuffix(line, "contents:") {
				inContents = true
				continue
			}
			if inContents && strings.HasPrefix(line, "  ") {
				if !strings.HasPrefix(line, "  ...") {
					entryCount++
				}
			}
		}
		if entryCount != maxPrefixEntries {
			t.Errorf("expected %d entries, got %d", maxPrefixEntries, entryCount)
		}
	})
}

func TestListPrefixDir(t *testing.T) {
	t.Run("nonexistent", func(t *testing.T) {
		got := listPrefixDir("/nonexistent/path/abc123")
		if got != nil {
			t.Errorf("expected nil for nonexistent dir, got %v", got)
		}
	})

	t.Run("empty dir", func(t *testing.T) {
		dir := t.TempDir()
		got := listPrefixDir(dir)
		if len(got) != 0 {
			t.Errorf("expected empty listing, got %v", got)
		}
	})

	t.Run("files and dirs", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/readme.md", []byte("hi"), 0o644)
		os.Mkdir(dir+"/sub", 0o755)

		got := listPrefixDir(dir)
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %v", got)
		}
		// os.ReadDir returns sorted entries.
		if got[0] != "readme.md" {
			t.Errorf("got[0] = %q, want readme.md", got[0])
		}
		if got[1] != "sub/" {
			t.Errorf("got[1] = %q, want sub/", got[1])
		}
	})
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/foo", home + "/foo"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~notuser/foo", "~notuser/foo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandHome(tt.input)
			if got != tt.want {
				t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
