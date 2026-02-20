package talents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter_Tags(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantTags []string
		wantBody string
	}{
		{
			name:     "no frontmatter",
			raw:      "# Hello\n\nSome content.",
			wantTags: nil,
			wantBody: "# Hello\n\nSome content.",
		},
		{
			name:     "tags with bracket syntax",
			raw:      "---\ntags: [ha, physical]\n---\n# Device Control",
			wantTags: []string{"ha", "physical"},
			wantBody: "# Device Control",
		},
		{
			name:     "single tag",
			raw:      "---\ntags: [memory]\n---\nContent here.",
			wantTags: []string{"memory"},
			wantBody: "Content here.",
		},
		{
			name:     "no closing delimiter",
			raw:      "---\ntags: [ha]\nContent without close.",
			wantTags: nil,
			wantBody: "---\ntags: [ha]\nContent without close.",
		},
		{
			name:     "empty tags list",
			raw:      "---\ntags: []\n---\nContent here.",
			wantTags: nil,
			wantBody: "Content here.",
		},
		{
			name:     "frontmatter with extra fields",
			raw:      "---\nauthor: test\ntags: [core, search]\npriority: 1\n---\nBody.",
			wantTags: []string{"core", "search"},
			wantBody: "Body.",
		},
		{
			name:     "no tags line in frontmatter",
			raw:      "---\nauthor: test\n---\nBody.",
			wantTags: nil,
			wantBody: "Body.",
		},
		{
			name:     "opening delimiter not at start",
			raw:      "Some text\n---\ntags: [ha]\n---\nBody.",
			wantTags: nil,
			wantBody: "Some text\n---\ntags: [ha]\n---\nBody.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags, body := parseFrontmatter(tt.raw)

			if tt.wantTags == nil && tags != nil {
				t.Errorf("tags = %v, want nil", tags)
			}
			if tt.wantTags != nil {
				if len(tags) != len(tt.wantTags) {
					t.Fatalf("len(tags) = %d, want %d: %v", len(tags), len(tt.wantTags), tags)
				}
				for i, want := range tt.wantTags {
					if tags[i] != want {
						t.Errorf("tags[%d] = %q, want %q", i, tags[i], want)
					}
				}
			}

			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestFilterByTags(t *testing.T) {
	all := []Talent{
		{Name: "core", Tags: nil, Content: "core guidance"},
		{Name: "ha-tools", Tags: []string{"ha"}, Content: "ha guidance"},
		{Name: "search-tools", Tags: []string{"search"}, Content: "search guidance"},
		{Name: "multi", Tags: []string{"ha", "physical"}, Content: "multi guidance"},
	}

	tests := []struct {
		name       string
		activeTags map[string]bool
		want       []string // substrings that must appear
		wantAbsent []string // substrings that must NOT appear
	}{
		{
			name:       "nil active tags includes all",
			activeTags: nil,
			want:       []string{"core guidance", "ha guidance", "search guidance", "multi guidance"},
		},
		{
			name:       "ha tag active",
			activeTags: map[string]bool{"ha": true},
			want:       []string{"core guidance", "ha guidance", "multi guidance"},
			wantAbsent: []string{"search guidance"},
		},
		{
			name:       "search tag active",
			activeTags: map[string]bool{"search": true},
			want:       []string{"core guidance", "search guidance"},
			wantAbsent: []string{"ha guidance", "multi guidance"},
		},
		{
			name:       "multiple tags active",
			activeTags: map[string]bool{"ha": true, "search": true},
			want:       []string{"core guidance", "ha guidance", "search guidance", "multi guidance"},
		},
		{
			name:       "unknown tag only loads untagged",
			activeTags: map[string]bool{"nonexistent": true},
			want:       []string{"core guidance"},
			wantAbsent: []string{"ha guidance", "search guidance", "multi guidance"},
		},
		{
			name:       "empty active tags map loads only untagged",
			activeTags: map[string]bool{},
			want:       []string{"core guidance"},
			wantAbsent: []string{"ha guidance", "search guidance", "multi guidance"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterByTags(all, tt.activeTags)
			for _, want := range tt.want {
				if !strings.Contains(result, want) {
					t.Errorf("result missing %q:\n%s", want, result)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(result, absent) {
					t.Errorf("result should not contain %q:\n%s", absent, result)
				}
			}
		})
	}
}

func TestFilterByTags_Empty(t *testing.T) {
	result := FilterByTags(nil, nil)
	if result != "" {
		t.Errorf("FilterByTags(nil, nil) = %q, want empty", result)
	}

	result = FilterByTags([]Talent{}, nil)
	if result != "" {
		t.Errorf("FilterByTags([], nil) = %q, want empty", result)
	}
}

func TestLoadAll(t *testing.T) {
	dir := t.TempDir()

	// Write talent files with and without frontmatter.
	writeFile(t, dir, "core.md", "# Core\nAlways loaded.")
	writeFile(t, dir, "ha-tools.md", "---\ntags: [ha]\n---\n# HA Tools\nHome Assistant guidance.")
	writeFile(t, dir, "search.md", "---\ntags: [search, web]\n---\n# Search\nSearch guidance.")

	loader := NewLoader(dir)
	talents, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(talents) != 3 {
		t.Fatalf("len(talents) = %d, want 3", len(talents))
	}

	// Sorted by filename: core, ha-tools, search
	if talents[0].Name != "core" {
		t.Errorf("talents[0].Name = %q, want %q", talents[0].Name, "core")
	}
	if talents[0].Tags != nil {
		t.Errorf("talents[0].Tags = %v, want nil", talents[0].Tags)
	}
	if !strings.Contains(talents[0].Content, "Always loaded") {
		t.Errorf("talents[0].Content missing expected text")
	}

	if talents[1].Name != "ha-tools" {
		t.Errorf("talents[1].Name = %q, want %q", talents[1].Name, "ha-tools")
	}
	if len(talents[1].Tags) != 1 || talents[1].Tags[0] != "ha" {
		t.Errorf("talents[1].Tags = %v, want [ha]", talents[1].Tags)
	}

	if talents[2].Name != "search" {
		t.Errorf("talents[2].Name = %q, want %q", talents[2].Name, "search")
	}
	if len(talents[2].Tags) != 2 {
		t.Fatalf("len(talents[2].Tags) = %d, want 2", len(talents[2].Tags))
	}
	if talents[2].Tags[0] != "search" || talents[2].Tags[1] != "web" {
		t.Errorf("talents[2].Tags = %v, want [search web]", talents[2].Tags)
	}
}

func TestLoadAll_EmptyDir(t *testing.T) {
	loader := NewLoader("")
	talents, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if talents != nil {
		t.Errorf("LoadAll() = %v, want nil for empty dir", talents)
	}
}

func TestLoadAll_MissingDir(t *testing.T) {
	loader := NewLoader("/nonexistent/path")
	talents, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if talents != nil {
		t.Errorf("LoadAll() = %v, want nil for missing dir", talents)
	}
}

func TestGenerateManifest(t *testing.T) {
	entries := []ManifestEntry{
		{Tag: "ha", Description: "Home Assistant tools", Tools: []string{"get_state", "call_service"}, AlwaysActive: true},
		{Tag: "search", Description: "Web search tools", Tools: []string{"web_search", "web_fetch"}, AlwaysActive: false},
	}

	talent := GenerateManifest(entries)
	if talent == nil {
		t.Fatal("GenerateManifest() returned nil")
	}

	if talent.Name != "_capability_manifest" {
		t.Errorf("Name = %q, want %q", talent.Name, "_capability_manifest")
	}
	if talent.Tags != nil {
		t.Errorf("Tags = %v, want nil (untagged)", talent.Tags)
	}
	if !strings.Contains(talent.Content, "ha") {
		t.Error("manifest should mention 'ha' tag")
	}
	if !strings.Contains(talent.Content, "always active") {
		t.Error("manifest should mention 'always active' for ha tag")
	}
	if !strings.Contains(talent.Content, "search") {
		t.Error("manifest should mention 'search' tag")
	}
	if !strings.Contains(talent.Content, "available") {
		t.Error("manifest should mention 'available' for non-always-active tags")
	}
	if !strings.Contains(talent.Content, "request_capability") {
		t.Error("manifest should mention request_capability tool")
	}
	if !strings.Contains(talent.Content, "delegate") {
		t.Error("manifest should mention delegate as alternative")
	}
}

func TestGenerateManifest_Empty(t *testing.T) {
	if GenerateManifest(nil) != nil {
		t.Error("GenerateManifest(nil) should return nil")
	}
	if GenerateManifest([]ManifestEntry{}) != nil {
		t.Error("GenerateManifest([]) should return nil")
	}
}

func TestShouldIncludeTalent(t *testing.T) {
	tests := []struct {
		name       string
		talent     Talent
		activeTags map[string]bool
		want       bool
	}{
		{
			name:       "untagged with nil active tags",
			talent:     Talent{Tags: nil},
			activeTags: nil,
			want:       true,
		},
		{
			name:       "untagged with active tags",
			talent:     Talent{Tags: nil},
			activeTags: map[string]bool{"ha": true},
			want:       true,
		},
		{
			name:       "tagged with nil active tags",
			talent:     Talent{Tags: []string{"ha"}},
			activeTags: nil,
			want:       true,
		},
		{
			name:       "tagged with matching active tag",
			talent:     Talent{Tags: []string{"ha"}},
			activeTags: map[string]bool{"ha": true},
			want:       true,
		},
		{
			name:       "tagged with non-matching active tag",
			talent:     Talent{Tags: []string{"ha"}},
			activeTags: map[string]bool{"search": true},
			want:       false,
		},
		{
			name:       "multi-tagged with one match",
			talent:     Talent{Tags: []string{"ha", "physical"}},
			activeTags: map[string]bool{"physical": true},
			want:       true,
		},
		{
			name:       "empty tags slice is untagged",
			talent:     Talent{Tags: []string{}},
			activeTags: map[string]bool{"ha": true},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldIncludeTalent(tt.talent, tt.activeTags); got != tt.want {
				t.Errorf("shouldIncludeTalent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
