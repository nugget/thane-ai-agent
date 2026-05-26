package toolcatalog

import (
	"sort"
	"strings"
	"testing"
)

// TestBuiltinTagSpecs_ParentsResolveToMenuTags pins the invariant that
// every Parent value points at a real menu tag. Catches the dangling
// reference that the loose prose "Usually leads to X, Y, Z" descriptions
// were prone to — a leaf claiming Parents: []string{"typo"} or pointing
// at a tag whose Kind isn't TagKindMenu would slip past code review
// without this guard.
func TestBuiltinTagSpecs_ParentsResolveToMenuTags(t *testing.T) {
	specs := BuiltinTagSpecs()
	var problems []string
	for name, spec := range specs {
		for _, parent := range spec.Parents {
			parentSpec, ok := specs[parent]
			if !ok {
				problems = append(problems, name+": parent "+parent+" is not a registered tag")
				continue
			}
			if !parentSpec.Kind.IsMenu() {
				problems = append(problems, name+": parent "+parent+" is registered but not a menu (Kind="+string(parentSpec.Kind)+")")
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("BuiltinTagSpecs Parents reference non-menu tags:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// TestBuiltinTagSpecs_AliasesResolveToCanonicals pins that every Alias
// is unique across the catalog (no two canonical tags claim the same
// alias) and doesn't collide with an existing canonical name. Without
// this, a future addition like Aliases: []string{"ha"} on a sibling tag
// would silently override the canonical ha entry in the reverse-alias
// map.
func TestBuiltinTagSpecs_AliasesResolveToCanonicals(t *testing.T) {
	specs := BuiltinTagSpecs()
	seenAliases := make(map[string]string)
	var problems []string
	for name, spec := range specs {
		for _, alias := range spec.Aliases {
			if _, ok := specs[alias]; ok {
				problems = append(problems, name+": alias "+alias+" collides with an existing canonical tag")
				continue
			}
			if owner, dup := seenAliases[alias]; dup {
				problems = append(problems, name+": alias "+alias+" already declared by "+owner)
				continue
			}
			seenAliases[alias] = name
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("BuiltinTagSpecs Aliases are not unique:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// TestCanonicalTagName_ResolvesHomeAssistantAlias is the worked example
// for alias resolution: the reverse-alias map populated at init must
// resolve homeassistant → ha, and the canonical name must round-trip
// unchanged.
func TestCanonicalTagName_ResolvesHomeAssistantAlias(t *testing.T) {
	if got := CanonicalTagName("homeassistant"); got != "ha" {
		t.Fatalf("CanonicalTagName(homeassistant) = %q, want ha", got)
	}
	if got := CanonicalTagName("ha"); got != "ha" {
		t.Fatalf("CanonicalTagName(ha) = %q, want ha (canonical round-trip)", got)
	}
	if got := CanonicalTagName("nonexistent"); got != "nonexistent" {
		t.Fatalf("CanonicalTagName(nonexistent) = %q, want nonexistent (unchanged for unknown)", got)
	}
}

// TestHasBuiltinTag_AcceptsAliases checks that aliases register as
// known tags. The runtime relies on this so validation against
// unknown-tag references (channel_tags pointing at homeassistant, KB
// articles tagged homeassistant) keeps working through the alias.
func TestHasBuiltinTag_AcceptsAliases(t *testing.T) {
	if !HasBuiltinTag("homeassistant") {
		t.Fatal("HasBuiltinTag(homeassistant) = false, want true via ha's alias")
	}
	if !HasBuiltinTag("ha") {
		t.Fatal("HasBuiltinTag(ha) = false, want true (canonical)")
	}
	if HasBuiltinTag("definitely_not_a_tag") {
		t.Fatal("HasBuiltinTag(definitely_not_a_tag) = true, want false")
	}
}

// TestLookupBuiltinTagSpec_ResolvesAliases confirms that fetching the
// spec for an alias returns the canonical spec.
func TestLookupBuiltinTagSpec_ResolvesAliases(t *testing.T) {
	viaCanonical, ok := LookupBuiltinTagSpec("ha")
	if !ok {
		t.Fatal("LookupBuiltinTagSpec(ha) not found")
	}
	viaAlias, ok := LookupBuiltinTagSpec("homeassistant")
	if !ok {
		t.Fatal("LookupBuiltinTagSpec(homeassistant) not found via alias")
	}
	if viaAlias.Description != viaCanonical.Description {
		t.Fatalf("alias resolution returned different spec: alias=%+v canonical=%+v",
			viaAlias, viaCanonical)
	}
}

func TestBuildCapabilitySurface_SortsTagsAndTools(t *testing.T) {
	surface := BuildCapabilitySurface(
		map[string][]string{
			"interactive": nil,
			"web":         {"web_search", "web_fetch"},
			"forge":       {"forge_pr_get", "forge_search"},
		},
		map[string]string{
			"interactive": "Interactive loop guidance",
			"web":         "Web tools",
			"forge":       "Forge tools",
		},
		map[string]bool{
			"forge": true,
		},
		map[string]bool{
			"web": true,
		},
	)

	if len(surface) != 3 {
		t.Fatalf("len(surface) = %d, want 3", len(surface))
	}
	if got := []string{surface[0].Tag, surface[1].Tag, surface[2].Tag}; strings.Join(got, ",") != "forge,interactive,web" {
		t.Fatalf("tags = %v, want [forge interactive web]", got)
	}
	if !surface[0].Core {
		t.Fatal("forge should be core")
	}
	if !surface[1].Kind.IsMenu() {
		t.Fatal("interactive should be a menu tag")
	}
	if !surface[2].Protected {
		t.Fatal("web should be protected")
	}
	if got := strings.Join(surface[2].Tools, ","); got != "web_fetch,web_search" {
		t.Fatalf("web tools = %q", got)
	}
}

func TestBuiltinToolCatalogIncludesCoreTools(t *testing.T) {
	// The canonical core set: 11 tools that survive
	// capability-tag filtering regardless of scope. list_loaded_capabilities
	// was previously a 12th member but was demoted because its output is
	// a strict subset of the "## Active Tags" section already
	// rendered into every prompt (see capability_tools.go's
	// registerListLoadedCapabilities Godoc for the full rationale).
	names := []string{
		"tag_activate",
		"tag_deactivate",
		"tag_reset",
		"tag_inspect",
		"lens_activate",
		"lens_deactivate",
		"lens_list",
		"thane_now",
		"thane_assign",
		"request_core_attention",
		"logs_query",
	}
	for _, name := range names {
		spec, ok := LookupBuiltinToolSpec(name)
		if !ok {
			t.Fatalf("LookupBuiltinToolSpec(%q) not found", name)
		}
		if spec.CanonicalID != "native:"+name {
			t.Fatalf("LookupBuiltinToolSpec(%q).CanonicalID = %q, want native:%s", name, spec.CanonicalID, name)
		}
		if spec.Source != NativeToolSource {
			t.Fatalf("LookupBuiltinToolSpec(%q).Source = %q, want %q", name, spec.Source, NativeToolSource)
		}
	}
}

func TestBuiltinToolCatalogIncludesLoopIntentFrontDoors(t *testing.T) {
	spec, ok := LookupBuiltinToolSpec("thane_create_container")
	if !ok {
		t.Fatal("thane_create_container missing from builtin tool catalog")
	}
	if !containsString(spec.Tags, "loops") {
		t.Fatalf("thane_create_container tags = %v, want loops", spec.Tags)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRenderLoadedCapabilitySummary_UsesDescriptionsAndFallsBackForUnknownTags(t *testing.T) {
	summary := RenderLoadedCapabilitySummary([]CapabilitySurface{
		{
			Tag:         "forge",
			Description: "Forge and code review tools.",
			Tools:       []string{"forge_pr_get", "forge_search"},
		},
	}, map[string]bool{
		"forge":   true,
		"unknown": true,
	})

	if !strings.Contains(summary, "\"kind\":\"loaded_capabilities\"") {
		t.Fatalf("summary = %q, want loaded-capabilities kind", summary)
	}
	if !strings.Contains(summary, "\"tag\":\"forge\"") {
		t.Fatalf("summary = %q, want forge detail", summary)
	}
	if !strings.Contains(summary, "\"description\":\"Forge and code review tools.\"") {
		t.Fatalf("summary = %q, want forge description", summary)
	}
	if !strings.Contains(summary, "\"tool_count\":2") {
		t.Fatalf("summary = %q, want forge tool_count", summary)
	}
	if !strings.Contains(summary, "\"tag\":\"unknown\"") {
		t.Fatalf("summary = %q, want unknown-tag fallback", summary)
	}
}

func TestRenderLoadedCapabilitySummary_EmptyStateExplainsAvailability(t *testing.T) {
	summary := RenderLoadedCapabilitySummary(nil, nil)
	if !strings.Contains(summary, "\"kind\":\"loaded_capabilities\"") {
		t.Fatalf("summary = %q, want loaded-capabilities kind", summary)
	}
	if !strings.Contains(summary, "\"loaded_capabilities\":[]") {
		t.Fatalf("summary = %q, want empty loaded_capabilities array", summary)
	}
	if strings.Contains(summary, "\"activation_tools\"") {
		t.Fatalf("summary = %q, want loaded-capabilities summary to stay state-only", summary)
	}
}

func TestRenderCapabilityManifestMarkdown_UsesExactToolNames(t *testing.T) {
	manifest := RenderCapabilityManifestMarkdown([]CapabilitySurface{
		{Tag: "development", Description: "Development trailhead.", Teaser: "Activate when the next move is about code or repos.", NextTags: []string{"forge", "files", "web"}, Kind: TagKindMenu},
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
	})
	if !strings.Contains(manifest, "\"kind\":\"tag_menu\"") {
		t.Fatalf("manifest = %q, want tag_menu kind", manifest)
	}
	if !strings.Contains(manifest, "\"activate\":\"tag_activate\"") {
		t.Fatalf("manifest = %q, want tag_activate example", manifest)
	}
	if !strings.Contains(manifest, "\"reset\":\"tag_reset\"") {
		t.Fatalf("manifest = %q, want tag_reset example", manifest)
	}
	if !strings.Contains(manifest, "\"inspect\":\"tag_inspect\"") {
		t.Fatalf("manifest = %q, want tag_inspect example", manifest)
	}
	if !strings.Contains(manifest, "\"delegate\":\"thane_now\"") {
		t.Fatalf("manifest = %q, want thane_now example", manifest)
	}
	if !strings.Contains(manifest, "\"development\"") {
		t.Fatalf("manifest = %q, want development menu entry", manifest)
	}
	if !strings.Contains(manifest, "\"teaser\":\"Activate when the next move is about code or repos.\"") {
		t.Fatalf("manifest = %q, want teaser", manifest)
	}
	if !strings.Contains(manifest, "\"next_tags\":[\"forge\",\"files\",\"web\"]") {
		t.Fatalf("manifest = %q, want next_tags", manifest)
	}
	if strings.Contains(manifest, "\"tag\":\"\"") {
		t.Fatalf("manifest = %q, want menu entries keyed by tag without empty inner tag fields", manifest)
	}
	if strings.Contains(manifest, "\"forge\":{") {
		t.Fatalf("manifest = %q, want non-menu forge hidden from menu entries", manifest)
	}
}

func TestRenderCapabilityActivationDescription_ShowsMenuTags(t *testing.T) {
	desc := RenderCapabilityActivationDescription([]CapabilitySurface{
		{Tag: "development", Description: "Development trailhead.", Teaser: "Activate when the next move is about code or repos.", NextTags: []string{"forge", "files", "web"}, Kind: TagKindMenu},
		{Tag: "forge", Description: "Forge tools.", Tools: []string{"forge_pr_get"}},
		{Tag: "owner", Description: "Owner guidance.", Protected: true},
	})

	if !strings.Contains(desc, "coarse-to-fine menu") {
		t.Fatalf("description = %q, want coarse-to-fine guidance", desc)
	}
	if !strings.Contains(desc, "`tag_reset`") {
		t.Fatalf("description = %q, want tag_reset exact tool name", desc)
	}
	if !strings.Contains(desc, "**development**") {
		t.Fatalf("description = %q, want development menu bullet", desc)
	}
	if !strings.Contains(desc, "Activate when the next move is about code or repos.") {
		t.Fatalf("description = %q, want teaser wording", desc)
	}
	if !strings.Contains(desc, "next: forge, files, web") {
		t.Fatalf("description = %q, want next-tags hint", desc)
	}
	if strings.Contains(desc, "**forge**") {
		t.Fatalf("description = %q, want forge omitted from top-level menu", desc)
	}
	if !strings.Contains(desc, "**owner**") {
		t.Fatalf("description = %q, want protected owner menu bullet", desc)
	}
	if !strings.Contains(desc, "protected, trustworthy when present, not manually activatable") {
		t.Fatalf("description = %q, want protected owner status note", desc)
	}
}
