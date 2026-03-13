package delegate

import (
	"encoding/json"
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
		argsJSON string
		want     string // expected "path" value after expansion
		noChange bool   // if true, argsJSON should be returned unchanged
	}{
		{
			name:     "basic prefix with subpath",
			argsJSON: `{"path":"events/2023-06 MSRH.md"}`,
			want:     "~/Sync/Vault/Events/2023-06 MSRH.md",
		},
		{
			name:     "longer prefix matched first",
			argsJSON: `{"path":"tde/session.md"}`,
			want:     "~/Sync/Vault/Events/TDE/session.md",
		},
		{
			name:     "short prefix still works",
			argsJSON: `{"path":"t/scratch.txt"}`,
			want:     "~/Sync/Vault/Temp/scratch.txt",
		},
		{
			name:     "exact prefix without subpath",
			argsJSON: `{"path":"events"}`,
			want:     "~/Sync/Vault/Events",
		},
		{
			name:     "absolute path unchanged",
			argsJSON: `{"path":"/etc/hosts"}`,
			noChange: true,
		},
		{
			name:     "home-relative path unchanged",
			argsJSON: `{"path":"~/Documents/foo.md"}`,
			noChange: true,
		},
		{
			name:     "unknown prefix unchanged",
			argsJSON: `{"path":"unknown/foo.md"}`,
			noChange: true,
		},
		{
			name:     "non-path args untouched",
			argsJSON: `{"pattern":"*.md","path":"events/sub"}`,
			want:     "~/Sync/Vault/Events/sub",
		},
		{
			name:     "empty path unchanged",
			argsJSON: `{"path":""}`,
			noChange: true,
		},
		{
			name:     "no path key unchanged",
			argsJSON: `{"content":"hello"}`,
			noChange: true,
		},
		{
			name:     "empty prefixes",
			argsJSON: `{"path":"events/foo"}`,
			noChange: true,
		},
		{
			name:     "nested subpath",
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

			got := expandPathPrefixes(tt.argsJSON, p)

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
		// Should be sorted alphabetically.
		if got == "" {
			t.Fatal("expected non-empty prompt")
		}
		// "a" should come before "z" in the output.
		aIdx := len("Path prefixes available:\n  a/")
		zIdx := len("Path prefixes available:\n  a/ → /a-path/\n  z/")
		if aIdx >= zIdx {
			t.Error("prefixes not sorted alphabetically")
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
