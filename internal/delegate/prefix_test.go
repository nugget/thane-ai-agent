package delegate

import (
	"encoding/json"
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

	t.Run("single prefix", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"docs": "~/Documents",
		})
		want := "Path prefixes available:\n  docs/ → ~/Documents/\nUse these prefixes at the start of file tool paths instead of full paths."
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("multiple sorted", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"z": "/z-path",
			"a": "/a-path",
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

	t.Run("trailing slash stripped", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"docs": "~/Documents/",
		})
		want := "Path prefixes available:\n  docs/ → ~/Documents/\nUse these prefixes at the start of file tool paths instead of full paths."
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})
}
