package talents

import (
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

// repoTalentsDir returns the in-tree talents/ directory, located
// relative to this test file's path so the test is portable across
// developer checkouts and CI sandboxes.
func repoTalentsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate repo talents dir")
	}
	// Walk from internal/model/talents/*_test.go to the repo root.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "talents")
}

func loadRepoTalents(t *testing.T) []Talent {
	t.Helper()
	loader := NewLoader(repoTalentsDir(t))
	talents, err := loader.Talents()
	if err != nil {
		t.Fatalf("loading repo talents: %v", err)
	}
	if len(talents) == 0 {
		t.Fatal("no talents loaded from repo dir; the corpus regression tests would be meaningless")
	}
	return talents
}

// TestRepoTrailheadNextTagsResolve pins every in-tree trailhead's
// next_tags references against the union of (a) compiled built-in tags
// and (b) tags declared by any other loaded talent. A trailhead that
// points at a tag that no longer exists (or never existed) silently
// breaks the model's decision trail — activating the suggested tag
// loads nothing. Without this guard, a tag rename or deprecation can
// rot the trails it touches without any test surfacing it.
//
// Two valid references:
//
//   - **Built-in tag** — declared in [toolcatalog.BuiltinTagSpecs].
//     Activating it loads the tools the catalog binds to that tag.
//   - **Talent-declared tag** — any other loaded talent's `tags:`
//     field. Used for multi-node trailhead navigation: a sibling node
//     in the same file (or a related node in another file) declares
//     `tags: [self_name]` so trailheads can chain to it without
//     polluting the global tool-tag catalog. `loops-examples.md` is
//     the canonical example.
//
// Operator-defined catalog tags (declared via the CapabilityTags: YAML
// block per deployment) are intentionally rejected here: they're not
// portable across configurations and shouldn't appear in the bundled
// talent corpus. If a concept genuinely deserves a trailhead pointer,
// promote it to a real [toolcatalog.BuiltinTagSpec] entry (and back it
// with an in-repo talent tagged to match), or declare the tag on
// another loaded talent's `tags:` field for intra-talent navigation.
func TestRepoTrailheadNextTagsResolve(t *testing.T) {
	talents := loadRepoTalents(t)
	known := buildResolvableTagSet(talents)

	var problems []string
	trailheads := 0
	for _, talent := range talents {
		if talent.Kind != KindTrailhead {
			continue
		}
		trailheads++
		for _, tag := range talent.NextTags {
			if _, ok := known[tag]; !ok {
				problems = append(problems, talent.Name+": next_tags references unresolvable tag "+tag)
			}
		}
	}
	if trailheads == 0 {
		t.Fatal("no trailhead talents loaded; the in-repo corpus should contain several")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("trailhead next_tags reference tags neither in BuiltinTagSpecs nor declared by another talent:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// buildResolvableTagSet returns the union of built-in catalog tags
// and tags declared on the Tags field of any loaded talent. A talent
// next_tags reference is considered resolvable when its target is in
// this set.
func buildResolvableTagSet(talents []Talent) map[string]struct{} {
	known := make(map[string]struct{})
	for tag := range toolcatalog.BuiltinTagSpecs() {
		known[tag] = struct{}{}
	}
	for _, talent := range talents {
		for _, tag := range talent.Tags {
			known[tag] = struct{}{}
		}
	}
	return known
}

// toolReferenceRe matches `backticked_snake_case_tokens` in talent
// markdown bodies. The token must look like a Go-style identifier
// (lowercase + underscores + digits) and contain at least one
// underscore — single-word backticked terms like `INBOX` or `maintain`
// are intentionally not flagged, since the false-positive rate would
// drown the signal.
var toolReferenceRe = regexp.MustCompile("`([a-z][a-z0-9_]*_[a-z0-9_]*[a-z0-9])`")

// nonToolTokens are backticked snake_case identifiers that look
// tool-shaped to [TestRepoTalentToolReferences] but are actually
// configuration fields, frontmatter keys, or other non-tool concepts.
// Each entry documents a deliberate "this isn't a tool" decision so a
// future reader knows it was reviewed, not missed. Keep this list
// short and curate it consciously — every addition should be a real
// non-tool concept that legitimately appears in talent prose.
var nonToolTokens = map[string]struct{}{
	// LoopWakeTarget field name (also the field name on producer
	// tools like forge_repo_follow / media_follow / mqtt_wake_add).
	// Appears in talent prose describing routing, not as a tool call.
	"wake_loop": {},

	// Talent frontmatter keys. The authoring README documents these
	// alongside their tool-like siblings (`tags`, `kind`, `teaser`).
	// `next_tags` matches the regex but slips past the matcher
	// because neither `next` (first segment) nor `tags` (second
	// segment) appears in the catalog's prefix/second-segment sets.
	// `tags_all` is unlucky: its second segment `all` appears as
	// the second segment of `export_all_vcf`, so the matcher flags
	// the shape as tool-family-ish. Explicit allowlist entry keeps
	// the matcher's heuristics loose for real-tool catches without
	// flagging a documented frontmatter key.
	"tags_all": {},

	// Notification record field name (UUID identifying an outstanding
	// actionable). Appears as a parameter on resolve_actionable and
	// as an annotation in conversation history. The matcher flags it
	// because `request_` is a real tool prefix
	// (request_human_decision, request_human_escalation, etc.),
	// but `request_id` is a field name, not a tool.
	"request_id": {},
}

// TestRepoTalentToolReferences pins backticked tool-name references in
// talent prose against the compiled tool catalog. A reference like
// `email_compose` or `watch_entity` is a hallucination magnet — the
// model reads the talent, learns "use email_compose for this," then
// hits a tool-not-found error when it tries. This is the analogue of
// the docs-side drift test PR-907 introduced for docs/reference/tools.md.
//
// A token is treated as "claiming to be a tool" when it's backticked,
// snake_case, contains at least one underscore, and either:
//
//   - Its first segment matches a real tool family's prefix (catches
//     `email_compose`, `forge_yolo`, etc. — known family, wrong name).
//   - Its second segment matches a real tool family's second segment
//     (catches `watch_entity`, `unwatch_entity`, etc. — verb-noun
//     shape where the noun is one a real tool actually uses).
//
// Either match flags the token as a candidate; the test fails when
// any flagged token isn't itself a registered tool. Templates with
// non-identifier characters (`replace_output_<loop_name>`, `email_*`)
// are skipped by the regex.
//
// When a new tool is added to the catalog its segments automatically
// expand both gates, so future hallucinations in the same families
// get caught without changes here.
//
// Talent-declared tag names (the intra-talent navigation tags a
// multi-node tree uses to chain its branches together, e.g.
// `documents_read` declared by `documents.md`) are exempted: a tag
// can't be a tool by construction, and multi-node trees naturally
// produce snake_case names whose verb segments overlap with real tool
// families. Without this exemption, every `<parent>_read` /
// `<parent>_write` / `<parent>_curate` leaf name would require a
// per-name allowlist entry.
func TestRepoTalentToolReferences(t *testing.T) {
	talents := loadRepoTalents(t)
	knownTools, knownPrefixes, knownSecondSegments := buildToolReferenceSets()
	knownTags := buildResolvableTagSet(talents)

	var problems []string
	for _, talent := range talents {
		seen := make(map[string]struct{})
		for _, match := range toolReferenceRe.FindAllStringSubmatch(talent.Content, -1) {
			token := match[1]
			if _, dup := seen[token]; dup {
				continue
			}
			seen[token] = struct{}{}
			if _, ok := knownTools[token]; ok {
				continue
			}
			if _, ok := nonToolTokens[token]; ok {
				continue
			}
			if _, ok := knownTags[token]; ok {
				continue
			}
			segments := strings.Split(token, "_")
			prefixMatch := false
			if len(segments) > 0 {
				_, prefixMatch = knownPrefixes[segments[0]]
			}
			secondMatch := false
			if len(segments) > 1 {
				_, secondMatch = knownSecondSegments[segments[1]]
			}
			if !prefixMatch && !secondMatch {
				continue
			}
			problems = append(problems, talent.Name+": references `"+token+"` (shape matches a real tool family but no such tool is registered)")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("talent prose references tool names missing from the catalog:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// buildToolReferenceSets returns (a) the set of fully-qualified tool
// names from the compiled catalog, (b) the set of first-segment
// prefixes (the "family" — email, forge, doc, etc.), and (c) the set
// of second-segment terms (the verb's object — entity, fact, file,
// etc.). The prose drift check fires when a token matches the
// catalog's shape in either dimension but doesn't exist as a tool.
func buildToolReferenceSets() (map[string]struct{}, map[string]struct{}, map[string]struct{}) {
	specs := toolcatalog.BuiltinToolSpecs()
	known := make(map[string]struct{}, len(specs))
	prefixes := make(map[string]struct{})
	seconds := make(map[string]struct{})
	for name := range specs {
		known[name] = struct{}{}
		segments := strings.Split(name, "_")
		if len(segments) > 0 && segments[0] != "" {
			prefixes[segments[0]] = struct{}{}
		}
		if len(segments) > 1 && segments[1] != "" {
			seconds[segments[1]] = struct{}{}
		}
	}
	return known, prefixes, seconds
}
