package delegate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
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
	now := time.Now()

	t.Run("empty", func(t *testing.T) {
		got := formatPrefixPrompt(nil, now)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("nonexistent directory no entries", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"docs": "/nonexistent/path/abc123",
		}, now)
		info := parsePrefixJSON(t, got)
		pi, ok := info["docs"]
		if !ok {
			t.Fatal("missing 'docs' key in output")
		}
		if pi.Dir != "/nonexistent/path/abc123" {
			t.Errorf("dir = %q, want /nonexistent/path/abc123", pi.Dir)
		}
		if len(pi.Entries) != 0 {
			t.Errorf("expected no entries for nonexistent dir, got %v", pi.Entries)
		}
	})

	t.Run("both prefixes present", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"z": "/nonexistent/z-path",
			"a": "/nonexistent/a-path",
		}, now)
		info := parsePrefixJSON(t, got)
		if _, ok := info["a"]; !ok {
			t.Error("missing 'a' prefix")
		}
		if _, ok := info["z"]; !ok {
			t.Error("missing 'z' prefix")
		}
	})

	t.Run("trailing slash normalized", func(t *testing.T) {
		got := formatPrefixPrompt(map[string]string{
			"docs": "/nonexistent/Documents/",
		}, now)
		info := parsePrefixJSON(t, got)
		if info["docs"].Dir != "/nonexistent/Documents" {
			t.Errorf("dir = %q, want trailing slash stripped", info["docs"].Dir)
		}
	})

	t.Run("directory listing included", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/alpha.md", []byte("a"), 0o644)
		os.WriteFile(dir+"/beta.txt", []byte("bb"), 0o644)
		os.Mkdir(dir+"/subdir", 0o755)

		got := formatPrefixPrompt(map[string]string{
			"vault": dir,
		}, now)
		info := parsePrefixJSON(t, got)
		pi := info["vault"]

		if pi.Dir != dir {
			t.Errorf("dir = %q, want %q", pi.Dir, dir)
		}

		wantNames := map[string]string{
			"alpha.md": "file",
			"beta.txt": "file",
			"subdir":   "dir",
		}
		for _, e := range pi.Entries {
			wantType, ok := wantNames[e.Name]
			if !ok {
				t.Errorf("unexpected entry: %s", e.Name)
				continue
			}
			if e.Type != wantType {
				t.Errorf("%s: type = %q, want %q", e.Name, e.Type, wantType)
			}
			if e.ModTime == "" {
				t.Errorf("%s: missing mod time delta", e.Name)
			}
			if e.Type == "file" && e.Size == 0 {
				t.Errorf("%s: expected non-zero size for file", e.Name)
			}
			delete(wantNames, e.Name)
		}
		for missing := range wantNames {
			t.Errorf("missing entry: %s", missing)
		}
	})

	t.Run("listing capped at max entries", func(t *testing.T) {
		dir := t.TempDir()
		for i := range maxPrefixEntries + 10 {
			os.WriteFile(fmt.Sprintf("%s/file_%03d.txt", dir, i), []byte("x"), 0o644)
		}

		got := formatPrefixPrompt(map[string]string{
			"big": dir,
		}, now)
		info := parsePrefixJSON(t, got)
		pi := info["big"]

		if len(pi.Entries) != maxPrefixEntries {
			t.Errorf("expected %d entries, got %d", maxPrefixEntries, len(pi.Entries))
		}
		if !pi.Truncated {
			t.Error("expected truncated=true")
		}
	})

	t.Run("mod time is delta format", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/recent.md", []byte("r"), 0o644)

		got := formatPrefixPrompt(map[string]string{
			"test": dir,
		}, now)
		info := parsePrefixJSON(t, got)
		entries := info["test"].Entries
		if len(entries) == 0 {
			t.Fatal("expected entries")
		}
		// Mod time should be a delta like "-0s" or "-1s".
		mod := entries[0].ModTime
		if !strings.HasPrefix(mod, "-") && !strings.HasPrefix(mod, "+") {
			t.Errorf("mod time %q doesn't look like a delta", mod)
		}
		if !strings.HasSuffix(mod, "s") {
			t.Errorf("mod time %q doesn't end with 's'", mod)
		}
	})
}

// parsePrefixJSON extracts the JSON object from a formatPrefixPrompt
// result, skipping the header line.
func parsePrefixJSON(t *testing.T, prompt string) map[string]prefixInfo {
	t.Helper()
	idx := strings.Index(prompt, "\n")
	if idx < 0 {
		t.Fatalf("no newline in prompt: %q", prompt)
	}
	jsonPart := prompt[idx+1:]
	var info map[string]prefixInfo
	if err := json.Unmarshal([]byte(jsonPart), &info); err != nil {
		t.Fatalf("failed to parse prefix JSON: %v\nraw: %s", err, jsonPart)
	}
	return info
}

func TestListPrefixDir(t *testing.T) {
	now := time.Now()

	t.Run("nonexistent", func(t *testing.T) {
		got, truncated := listPrefixDir("/nonexistent/path/abc123", now)
		if got != nil {
			t.Errorf("expected nil for nonexistent dir, got %v", got)
		}
		if truncated {
			t.Error("expected truncated=false for nonexistent dir")
		}
	})

	t.Run("empty dir", func(t *testing.T) {
		dir := t.TempDir()
		got, _ := listPrefixDir(dir, now)
		if len(got) != 0 {
			t.Errorf("expected empty listing, got %v", got)
		}
	})

	t.Run("files and dirs", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(dir+"/readme.md", []byte("hi"), 0o644)
		os.Mkdir(dir+"/sub", 0o755)

		got, truncated := listPrefixDir(dir, now)
		if truncated {
			t.Error("unexpected truncation")
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %v", got)
		}
		// os.ReadDir returns sorted entries.
		if got[0].Name != "readme.md" || got[0].Type != "file" {
			t.Errorf("got[0] = %+v, want readme.md/file", got[0])
		}
		if got[0].Size != 2 {
			t.Errorf("got[0].Size = %d, want 2", got[0].Size)
		}
		if got[1].Name != "sub" || got[1].Type != "dir" {
			t.Errorf("got[1] = %+v, want sub/dir", got[1])
		}
		// Dirs should not have size.
		if got[1].Size != 0 {
			t.Errorf("got[1].Size = %d, want 0 for dir", got[1].Size)
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
