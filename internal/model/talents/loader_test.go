package talents

import (
	"context"
	"encoding/json"
	"errors"
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
			name:     "quoted tags are normalized",
			raw:      "---\ntags: [\"knowledge\", 'web']\n---\nContent here.",
			wantTags: []string{"knowledge", "web"},
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
			raw:      "---\nauthor: test\ntags: [core, web]\npriority: 1\n---\nBody.",
			wantTags: []string{"core", "web"},
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
			tags, body := ParseFrontmatter(tt.raw)

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

func TestParseFrontmatterMetadata(t *testing.T) {
	raw := "---\nkind: entry_point\ntags: [development, forge]\nteaser: \"Activate when the next move is about repos or code.\"\nnext_tags: [forge, files, web]\nauthor: test\n---\nBody."

	meta, body := ParseFrontmatterMetadata(raw)

	if body != "Body." {
		t.Fatalf("body = %q, want %q", body, "Body.")
	}
	if meta.Kind != "entry_point" {
		t.Fatalf("kind = %q, want entry_point", meta.Kind)
	}
	if got := strings.Join(meta.Tags, ","); got != "development,forge" {
		t.Fatalf("tags = %q, want development,forge", got)
	}
	if meta.Teaser != "Activate when the next move is about repos or code." {
		t.Fatalf("teaser = %q", meta.Teaser)
	}
	if got := strings.Join(meta.NextTags, ","); got != "forge,files,web" {
		t.Fatalf("next_tags = %q, want forge,files,web", got)
	}
}

func TestFilterByTags(t *testing.T) {
	all := []Talent{
		{Name: "core", Tags: nil, Content: "core guidance"},
		{Name: "ha-tools", Tags: []string{"ha"}, Content: "ha guidance"},
		{Name: "web-tools", Tags: []string{"web"}, Content: "web guidance"},
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
			want:       []string{"core guidance", "ha guidance", "web guidance", "multi guidance"},
		},
		{
			name:       "ha tag active",
			activeTags: map[string]bool{"ha": true},
			want:       []string{"core guidance", "ha guidance", "multi guidance"},
			wantAbsent: []string{"web guidance"},
		},
		{
			name:       "web tag active",
			activeTags: map[string]bool{"web": true},
			want:       []string{"core guidance", "web guidance"},
			wantAbsent: []string{"ha guidance", "multi guidance"},
		},
		{
			name:       "multiple tags active",
			activeTags: map[string]bool{"ha": true, "web": true},
			want:       []string{"core guidance", "ha guidance", "web guidance", "multi guidance"},
		},
		{
			name:       "unknown tag only loads untagged",
			activeTags: map[string]bool{"nonexistent": true},
			want:       []string{"core guidance"},
			wantAbsent: []string{"ha guidance", "web guidance", "multi guidance"},
		},
		{
			name:       "empty active tags map loads only untagged",
			activeTags: map[string]bool{},
			want:       []string{"core guidance"},
			wantAbsent: []string{"ha guidance", "web guidance", "multi guidance"},
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

func TestFilterByTags_EntryPointsPrecedeTaggedDoctrine(t *testing.T) {
	all := []Talent{
		{Name: "core", Tags: nil, Content: "CORE"},
		{Name: "interactive-communication", Tags: []string{"interactive"}, Content: "INTERACTIVE_COMM"},
		{Name: "interactive-entry-point", Tags: []string{"interactive"}, Kind: "entry_point", Content: "INTERACTIVE_ENTRY"},
		{Name: "interactive-doctrine", Tags: []string{"interactive"}, Content: "INTERACTIVE_DOCTRINE"},
	}

	result := FilterByTags(all, map[string]bool{"interactive": true})
	coreIdx := strings.Index(result, "CORE")
	entryIdx := strings.Index(result, "INTERACTIVE_ENTRY")
	commIdx := strings.Index(result, "INTERACTIVE_COMM")
	doctrineIdx := strings.Index(result, "INTERACTIVE_DOCTRINE")
	if coreIdx < 0 || entryIdx < 0 || commIdx < 0 || doctrineIdx < 0 {
		t.Fatalf("missing expected markers in result:\n%s", result)
	}
	if coreIdx >= entryIdx || entryIdx >= commIdx || entryIdx >= doctrineIdx {
		t.Fatalf("unexpected ordering:\n%s", result)
	}
}

func TestSplitByTags_PreservesAlwaysOnAndTaggedOrdering(t *testing.T) {
	all := []Talent{
		{Name: "manifest", Tags: nil, Content: "MANIFEST"},
		{Name: "core", Tags: nil, Content: "CORE"},
		{Name: "interactive-communication", Tags: []string{"interactive"}, Content: "INTERACTIVE_COMM"},
		{Name: "interactive-entry-point", Tags: []string{"interactive"}, Kind: "entry_point", Content: "INTERACTIVE_ENTRY"},
		{Name: "interactive-doctrine", Tags: []string{"interactive"}, Content: "INTERACTIVE_DOCTRINE"},
	}

	alwaysOn, tagged := SplitByTags(all, map[string]bool{"interactive": true})

	if strings.Contains(alwaysOn, "INTERACTIVE_") {
		t.Fatalf("alwaysOn should not contain tagged talents:\n%s", alwaysOn)
	}
	if !strings.Contains(alwaysOn, "MANIFEST") || !strings.Contains(alwaysOn, "CORE") {
		t.Fatalf("alwaysOn missing expected content:\n%s", alwaysOn)
	}

	entryIdx := strings.Index(tagged, "INTERACTIVE_ENTRY")
	commIdx := strings.Index(tagged, "INTERACTIVE_COMM")
	doctrineIdx := strings.Index(tagged, "INTERACTIVE_DOCTRINE")
	if entryIdx < 0 || commIdx < 0 || doctrineIdx < 0 {
		t.Fatalf("tagged missing expected content:\n%s", tagged)
	}
	if entryIdx >= commIdx || entryIdx >= doctrineIdx {
		t.Fatalf("entry point should precede tagged doctrine:\n%s", tagged)
	}
}

func TestTalents(t *testing.T) {
	dir := t.TempDir()

	// Write talent files with and without frontmatter.
	writeFile(t, dir, "core.md", "# Core\nAlways loaded.")
	writeFile(t, dir, "ha-tools.md", "---\nkind: entry_point\ntags: [ha]\nteaser: \"Open when the work touches home state.\"\nnext_tags: [ha_admin, notifications]\n---\n# HA Tools\nHome Assistant guidance.")
	writeFile(t, dir, "web.md", "---\ntags: [web, remote]\n---\n# Web\nWeb guidance.")

	loader := NewLoader(dir)
	talents, err := loader.Talents()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(talents) != 3 {
		t.Fatalf("len(talents) = %d, want 3", len(talents))
	}

	// Sorted by filename: core, ha-tools, web
	if talents[0].Name != "core" {
		t.Errorf("talents[0].Name = %q, want %q", talents[0].Name, "core")
	}
	if talents[0].Tags != nil {
		t.Errorf("talents[0].Tags = %v, want nil", talents[0].Tags)
	}
	if talents[0].Kind != "" {
		t.Errorf("talents[0].Kind = %q, want empty", talents[0].Kind)
	}
	if !strings.Contains(talents[0].Content, "Always loaded") {
		t.Errorf("talents[0].Content missing expected text")
	}
	if talents[0].SourcePath != filepath.Join(dir, "core.md") {
		t.Errorf("talents[0].SourcePath = %q, want core.md path", talents[0].SourcePath)
	}

	if talents[1].Name != "ha-tools" {
		t.Errorf("talents[1].Name = %q, want %q", talents[1].Name, "ha-tools")
	}
	if len(talents[1].Tags) != 1 || talents[1].Tags[0] != "ha" {
		t.Errorf("talents[1].Tags = %v, want [ha]", talents[1].Tags)
	}
	if talents[1].Kind != "entry_point" {
		t.Errorf("talents[1].Kind = %q, want entry_point", talents[1].Kind)
	}
	if talents[1].Teaser != "Open when the work touches home state." {
		t.Errorf("talents[1].Teaser = %q, want menu teaser", talents[1].Teaser)
	}
	if got := strings.Join(talents[1].NextTags, ","); got != "ha_admin,notifications" {
		t.Errorf("talents[1].NextTags = %q, want ha_admin,notifications", got)
	}

	if talents[2].Name != "web" {
		t.Errorf("talents[2].Name = %q, want %q", talents[2].Name, "web")
	}
	if len(talents[2].Tags) != 2 {
		t.Fatalf("len(talents[2].Tags) = %d, want 2", len(talents[2].Tags))
	}
	if talents[2].Tags[0] != "web" || talents[2].Tags[1] != "remote" {
		t.Errorf("talents[2].Tags = %v, want [web remote]", talents[2].Tags)
	}
}

type recordingTalentVerifier struct {
	err       error
	paths     []string
	consumers []string
}

func (v *recordingTalentVerifier) VerifyPath(_ context.Context, path string, consumer string) error {
	v.paths = append(v.paths, path)
	v.consumers = append(v.consumers, consumer)
	return v.err
}

func TestTalentsVerified_VerifiesBeforeRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "core.md", "# Core\n")

	loader := NewLoader(dir)
	verifier := &recordingTalentVerifier{err: errors.New("untrusted talent")}
	_, err := loader.TalentsVerified(context.Background(), verifier.VerifyPath, "talents")
	if err == nil {
		t.Fatal("TalentsVerified should fail when verifier rejects a talent file")
	}
	if !strings.Contains(err.Error(), "verify talent core.md") {
		t.Fatalf("error = %v, want verify talent wrapper", err)
	}
	if len(verifier.paths) != 1 || verifier.paths[0] != filepath.Join(dir, "core.md") {
		t.Fatalf("verified paths = %v, want core.md", verifier.paths)
	}
	if len(verifier.consumers) != 1 || verifier.consumers[0] != "talents" {
		t.Fatalf("verified consumers = %v, want talents", verifier.consumers)
	}
}

func TestTalents_EmptyDir(t *testing.T) {
	loader := NewLoader("")
	talents, err := loader.Talents()
	if err != nil {
		t.Fatalf("Talents() error = %v", err)
	}
	if talents != nil {
		t.Errorf("Talents() = %v, want nil for empty dir", talents)
	}
}

func TestTalents_MissingDir(t *testing.T) {
	loader := NewLoader("/nonexistent/path")
	talents, err := loader.Talents()
	if err != nil {
		t.Fatalf("Talents() error = %v", err)
	}
	if talents != nil {
		t.Errorf("Talents() = %v, want nil for missing dir", talents)
	}
}

func TestGenerateManifest(t *testing.T) {
	entries := []ManifestEntry{
		{Tag: "ha", Description: "Home Assistant tools", Tools: []string{"get_state", "call_service"}, AlwaysActive: true, KBArticles: 3, LiveContext: true},
		{Tag: "web", Description: "Web retrieval tools", Tools: []string{"web_search", "web_fetch"}, AlwaysActive: false},
		{Tag: "hpde", AdHoc: true, KBArticles: 2},
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

	// Preamble text.
	if !strings.Contains(talent.Content, "activate_capability") {
		t.Error("manifest should mention activate_capability in preamble")
	}
	if !strings.Contains(talent.Content, "delegate") {
		t.Error("manifest should mention delegate in preamble")
	}

	// Extract and parse the JSON portion.
	jsonStart := strings.Index(talent.Content, "{")
	if jsonStart < 0 {
		t.Fatal("manifest should contain JSON block")
	}
	jsonStr := talent.Content[jsonStart:]

	var parsed struct {
		Kind         string `json:"kind"`
		Capabilities map[string]struct {
			Status      string `json:"status"`
			Description string `json:"description"`
			ToolCount   int    `json:"tool_count"`
			Context     *struct {
				KBArticles int  `json:"kb_articles"`
				Live       bool `json:"live"`
			} `json:"context"`
		} `json:"capability_menu"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("manifest JSON should be valid: %v\nJSON: %s", err, jsonStr)
	}
	if parsed.Kind != "capability_menu" {
		t.Fatalf("kind = %q, want capability_menu", parsed.Kind)
	}

	// Configured tag: ha
	ha, ok := parsed.Capabilities["ha"]
	if !ok {
		t.Fatal("missing ha capability")
	}
	if ha.Status != "always_active" {
		t.Errorf("ha status = %q, want always_active", ha.Status)
	}
	if ha.ToolCount != 2 {
		t.Errorf("ha tools = %d, want 2", ha.ToolCount)
	}
	if ha.Context == nil || ha.Context.KBArticles != 3 || !ha.Context.Live {
		t.Errorf("ha context = %+v, want kb=3 live=true", ha.Context)
	}

	// Configured tag: web
	web, ok := parsed.Capabilities["web"]
	if !ok {
		t.Fatal("missing web capability")
	}
	if web.Status != "available" {
		t.Errorf("web status = %q, want available", web.Status)
	}

	// Ad-hoc tag: hpde
	hpde, ok := parsed.Capabilities["hpde"]
	if !ok {
		t.Fatal("missing hpde discoverable capability")
	}
	if hpde.Status != "discoverable" {
		t.Errorf("hpde status = %q, want discoverable", hpde.Status)
	}
	if hpde.Context == nil || hpde.Context.KBArticles != 2 {
		t.Errorf("hpde context = %+v, want kb=2", hpde.Context)
	}

	// Tool names should NOT appear in the output.
	if strings.Contains(talent.Content, "get_state") {
		t.Error("manifest should not list individual tool names")
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
			activeTags: map[string]bool{"web": true},
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

func TestParseFrontmatterMetadata_TagsAll(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantTags    []string
		wantTagsAll []string
	}{
		{
			name: "tags_all alone",
			raw: `---
tags_all: [owner, message_channel]
---
body`,
			wantTagsAll: []string{"owner", "message_channel"},
		},
		{
			name: "tags and tags_all together",
			raw: `---
tags: [forge, ha]
tags_all: [owner]
---
body`,
			wantTags:    []string{"forge", "ha"},
			wantTagsAll: []string{"owner"},
		},
		{
			name: "tags_all with quoted entries",
			raw: `---
tags_all: ["a", 'b']
---
body`,
			wantTagsAll: []string{"a", "b"},
		},
		{
			name: "tags_all empty list",
			raw: `---
tags_all: []
---
body`,
			wantTagsAll: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, _ := ParseFrontmatterMetadata(tc.raw)
			if !equalStrings(meta.Tags, tc.wantTags) {
				t.Errorf("Tags = %v, want %v", meta.Tags, tc.wantTags)
			}
			if !equalStrings(meta.TagsAll, tc.wantTagsAll) {
				t.Errorf("TagsAll = %v, want %v", meta.TagsAll, tc.wantTagsAll)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseFrontmatterBlocks_SingleBlock(t *testing.T) {
	raw := `---
tags: [forge]
---

# Forge Entry

content
`
	blocks := ParseFrontmatterBlocks(raw)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := blocks[0].Frontmatter.Tags; len(got) != 1 || got[0] != "forge" {
		t.Errorf("tags = %v, want [forge]", got)
	}
	if !strings.Contains(blocks[0].Content, "# Forge Entry") {
		t.Errorf("content missing heading: %q", blocks[0].Content)
	}
}

func TestParseFrontmatterBlocks_MultiBlock(t *testing.T) {
	raw := `---
name: root
tags: [loops_examples]
kind: entry_point
next_tags: [loops_examples_curate]
---

# Root

choose your path

---
name: curate
tags: [loops_examples_curate]
kind: entry_point
---

# Curate

curate-specific guidance
`
	blocks := ParseFrontmatterBlocks(raw)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Frontmatter.Name != "root" {
		t.Errorf("block[0].Name = %q, want root", blocks[0].Frontmatter.Name)
	}
	if blocks[1].Frontmatter.Name != "curate" {
		t.Errorf("block[1].Name = %q, want curate", blocks[1].Frontmatter.Name)
	}
	if !strings.Contains(blocks[0].Content, "choose your path") {
		t.Errorf("block[0] content missing root body: %q", blocks[0].Content)
	}
	if !strings.Contains(blocks[1].Content, "curate-specific guidance") {
		t.Errorf("block[1] content missing curate body: %q", blocks[1].Content)
	}
	if strings.Contains(blocks[0].Content, "curate-specific guidance") {
		t.Error("block[0] content leaked into next block's body")
	}
}

func TestParseFrontmatterBlocks_EmptyBodyBetweenNodes(t *testing.T) {
	// A node with no body means the next node's frontmatter starts
	// immediately after the closing "---". The boundary scanner must
	// detect a boundary at position 0 of the body (no preceding
	// newline to anchor the search).
	raw := `---
name: a
tags: [x]
---
---
name: b
tags: [y]
---

body for b
`
	blocks := ParseFrontmatterBlocks(raw)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (empty body between nodes should not collapse them)", len(blocks))
	}
	if blocks[0].Frontmatter.Name != "a" || blocks[1].Frontmatter.Name != "b" {
		t.Fatalf("names = [%q, %q], want [a, b]", blocks[0].Frontmatter.Name, blocks[1].Frontmatter.Name)
	}
	if strings.TrimSpace(blocks[0].Content) != "" {
		t.Errorf("block[0].Content = %q, want empty", blocks[0].Content)
	}
	if !strings.Contains(blocks[1].Content, "body for b") {
		t.Errorf("block[1].Content missing body: %q", blocks[1].Content)
	}
}

func TestParseFrontmatterBlocks_HRWithDistantKeyParagraphStaysContent(t *testing.T) {
	// A "---" line followed by a blank line and then a paragraph that
	// happens to start with a frontmatter-key word ("name:", "tags:")
	// must stay as a markdown horizontal rule. Only a "---" whose very
	// next line is a "key: value" pair counts as a node boundary.
	raw := `---
name: only
tags: [x]
---

# Heading

paragraph one

---

name: looks like a key but is actually prose
this paragraph continues
`
	blocks := ParseFrontmatterBlocks(raw)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (HR with later key paragraph should not split)", len(blocks))
	}
	if !strings.Contains(blocks[0].Content, "name: looks like a key") {
		t.Errorf("body lost prose after HR: %q", blocks[0].Content)
	}
}

func TestParseFrontmatterBlocks_HRInBodyStaysContent(t *testing.T) {
	// A "---" line followed by prose (not a frontmatter key) is a
	// markdown horizontal rule and must not split the block.
	raw := `---
name: only
tags: [x]
---

# Heading

paragraph one

---

paragraph two
`
	blocks := ParseFrontmatterBlocks(raw)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (HR should not split)", len(blocks))
	}
	if !strings.Contains(blocks[0].Content, "paragraph one") || !strings.Contains(blocks[0].Content, "paragraph two") {
		t.Errorf("HR-separated content lost: %q", blocks[0].Content)
	}
}

func TestParseFrontmatterMetadata_DelegatesToFirstBlock(t *testing.T) {
	raw := `---
name: first
tags: [a]
---

first body

---
name: second
tags: [b]
---

second body
`
	meta, content := ParseFrontmatterMetadata(raw)
	if meta.Name != "first" {
		t.Errorf("meta.Name = %q, want first (multi-node should return first block's meta)", meta.Name)
	}
	if !strings.Contains(content, "first body") {
		t.Errorf("content missing first body: %q", content)
	}
	if strings.Contains(content, "second body") {
		t.Errorf("content leaked second body: %q", content)
	}
}

func TestTalents_MultiNodeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tree.md"), []byte(`---
name: tree_root
tags: [tree]
kind: entry_point
teaser: "root teaser"
next_tags: [tree_branch_a, tree_branch_b]
---

# Root

choose

---
name: tree_branch_a
tags: [tree_branch_a]
kind: entry_point
---

# Branch A

---
name: tree_branch_b
tags: [tree_branch_b]
kind: entry_point
---

# Branch B
`), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader(dir)
	all, err := loader.Talents()
	if err != nil {
		t.Fatalf("Talents: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d talents, want 3 from one multi-node file", len(all))
	}
	wantNames := []string{"tree_root", "tree_branch_a", "tree_branch_b"}
	for i, talent := range all {
		if talent.Name != wantNames[i] {
			t.Errorf("talent[%d].Name = %q, want %q", i, talent.Name, wantNames[i])
		}
		if !strings.HasSuffix(talent.SourcePath, "tree.md") {
			t.Errorf("talent[%d].SourcePath = %q, want tree.md", i, talent.SourcePath)
		}
	}
}

func TestTalents_MultiNodeRequiresName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(`---
tags: [a]
---

first

---
tags: [b]
---

second (missing name)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewLoader(dir).Talents()
	if err == nil {
		t.Fatal("expected error for multi-node file with missing name, got nil")
	}
	if !strings.Contains(err.Error(), "name:") {
		t.Errorf("error %q should mention the missing name field", err.Error())
	}
}

func TestTalents_DuplicateNameWithinFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dupe.md"), []byte(`---
name: same
tags: [a]
---

a

---
name: same
tags: [b]
---

b
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewLoader(dir).Talents()
	if err == nil {
		t.Fatal("expected error for duplicate node name, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should mention duplicate", err.Error())
	}
}

func TestTalents_SingleNodeNameOptional(t *testing.T) {
	// Single-node file without name: falls back to filename for
	// backward compatibility.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "legacy.md"), []byte(`---
tags: [legacy]
---

# Legacy
`), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := NewLoader(dir).Talents()
	if err != nil {
		t.Fatalf("Talents: %v", err)
	}
	if len(all) != 1 || all[0].Name != "legacy" {
		t.Fatalf("got %+v, want one talent named legacy (filename fallback)", all)
	}
}

func TestTalents_SingleNodeDeclaredNameWinsOverFilename(t *testing.T) {
	// A single-node file with an explicit name: uses it, not the
	// filename. Lets authors decouple identity from disk layout.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "filename.md"), []byte(`---
name: declared_name
tags: [x]
---

body
`), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := NewLoader(dir).Talents()
	if err != nil {
		t.Fatalf("Talents: %v", err)
	}
	if len(all) != 1 || all[0].Name != "declared_name" {
		t.Fatalf("got %+v, want declared_name", all)
	}
}
