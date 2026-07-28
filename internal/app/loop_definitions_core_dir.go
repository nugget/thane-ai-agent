package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"gopkg.in/yaml.v3"
)

// coreLoopsDirName is the subdirectory of the core document root holding
// operator-authored loop definitions, one loop per markdown document.
const coreLoopsDirName = "loops"

// Reserved section headings in a loop definition document.
//
// A definition is a markdown document because that is what the rest of
// this corpus already is: talents, dossiers, and every managed document
// take this shape, core is already a document root, and an agent tuning
// its own prompt should reach for the document tools it already has
// rather than a format invented for this one job.
//
// The spec rides a fenced yaml block under its own heading rather than
// in frontmatter, for two reasons. The document layer flattens
// frontmatter to map[string][]string, so nested fields — an output's
// facets, a profile, a subscription list — would survive on disk and be
// quietly mangled by anything that wrote frontmatter back. And a fence
// that gets clobbered fails to parse, naming its file, where flattened
// frontmatter yields a spec that parses and is wrong. Loud beats silent
// when the thing at stake is how a loop thinks.
//
// The heading is what makes the fence unambiguous: a prompt may
// legitimately contain a yaml example — the archivist's teaches
// structure — and only the fence under this heading is read.
const (
	coreLoopSpecHeading       = "Spec"
	coreLoopTaskHeading       = "Task"
	coreLoopSupervisorHeading = "Supervisor Review"
)

// excludeToolsDirectHumanEgress expands to every direct human-egress
// tool name at load.
//
// The list is decided in Go — a tool becomes human-egress by what it
// does, not by an author remembering to add it — so a definition that
// spelled the names out would be a copy that goes stale the first time
// one is added. The token says the intent instead, which is also what a
// reader of the spec needs to know.
//
// The "group:" prefix rather than a sigil: YAML reserves @ and ` as
// indicators, so a token starting with one is a parse error unless
// quoted, and a marker that silently requires quoting is a trap laid for
// whoever writes the next definition. A colon with no following space is
// an ordinary plain scalar, and no tool name contains one.
const excludeToolsDirectHumanEgress = "group:direct_human_egress"

// loadCoreLoopDefinitions reads every loop definition under <core>/loops.
//
// These are authoritative. A core-defined loop is part of the signed
// root the agent boots from, so the document is the definition: it wins
// over the built-in spec of the same name, and it is re-read every boot
// rather than seeded once. An operator or agent edits the document and
// restarts.
//
// A missing directory is not an error — an install with no core-defined
// loops is ordinary — but a file that is present and unreadable is,
// because the alternative is booting with a definition silently absent.
func loadCoreLoopDefinitions(corePath string) ([]looppkg.Spec, error) {
	if strings.TrimSpace(corePath) == "" {
		return nil, nil
	}
	dir := filepath.Join(corePath, coreLoopsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		// Regular files only. A symlink here would read content from
		// outside the signed root while looking like part of it, which is
		// the one thing a definition living in core is supposed to rule
		// out. Refused loudly rather than skipped: a definition silently
		// absent is how a loop stops existing without anyone noticing.
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file (%s); a loop definition must be a file in the signed root, not a link to content outside it", filepath.Join(dir, entry.Name()), entry.Type())
		}
		names = append(names, entry.Name())
	}
	// Sorted so the definition order a boot produces is a property of the
	// directory rather than of the filesystem's enumeration order.
	sort.Strings(names)

	specs := make([]looppkg.Spec, 0, len(names))
	seen := make(map[string]string, len(names))
	for _, name := range names {
		spec, err := decodeCoreLoopDefinition(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		// Keyed on the trimmed name because that is what everything
		// downstream keys on: "ego" and "ego " are two entries here and
		// one loop by the time they reach the registry.
		key := strings.TrimSpace(spec.Name)
		if other, dup := seen[key]; dup {
			return nil, fmt.Errorf("%s and %s both define the loop %q; one document is one loop", other, name, key)
		}
		seen[key] = name
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeCoreLoopDefinition reads one definition document.
func decodeCoreLoopDefinition(path string) (looppkg.Spec, error) {
	file := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return looppkg.Spec{}, fmt.Errorf("read %s: %w", file, err)
	}

	sections := splitCoreLoopSections(string(raw))
	specBlock, ok := sections[coreLoopSpecHeading]
	if !ok {
		return looppkg.Spec{}, fmt.Errorf("%s: no %q section; a loop definition carries its spec in a yaml block under that heading", file, "## "+coreLoopSpecHeading)
	}
	specYAML, ok := unfenceYAML(specBlock)
	if !ok {
		return looppkg.Spec{}, fmt.Errorf("%s: the %q section is not a single ```yaml block; the spec is fenced so prose and machine-readable content stay distinguishable", file, "## "+coreLoopSpecHeading)
	}

	spec, err := decodeCoreLoopSpecYAML(specYAML, file)
	if err != nil {
		return looppkg.Spec{}, err
	}
	if err := adoptCoreLoopProse(&spec, sections, file); err != nil {
		return looppkg.Spec{}, err
	}

	spec.ExcludeTools = expandExcludeToolTokens(spec.ExcludeTools)
	if err := spec.ValidatePersistable(); err != nil {
		return looppkg.Spec{}, fmt.Errorf("%s: %w", file, err)
	}
	return spec, nil
}

// adoptCoreLoopProse moves the document's prose sections onto the spec.
//
// Declaring one in both places is refused rather than resolved: a silent
// precedence rule means an author edits the prompt they can read and the
// loop keeps running the one they cannot.
func adoptCoreLoopProse(spec *looppkg.Spec, sections map[string]string, file string) error {
	if task := strings.TrimSpace(sections[coreLoopTaskHeading]); task != "" {
		if strings.TrimSpace(spec.Task) != "" {
			return fmt.Errorf("%s: task is set in the spec block and in a %q section; declare it once — the section is the one meant for prose", file, "## "+coreLoopTaskHeading)
		}
		spec.Task = task
	}
	if instructions := strings.TrimSpace(sections[coreLoopSupervisorHeading]); instructions != "" {
		// A definition may declare the section without declaring a
		// supervisor_profile at all, which is the ordinary case: the
		// overlay exists to carry these instructions.
		if spec.SupervisorProfile == nil {
			spec.SupervisorProfile = &router.LoopProfile{}
		}
		if strings.TrimSpace(spec.SupervisorProfile.Instructions) != "" {
			return fmt.Errorf("%s: supervisor instructions are set in the spec block and in a %q section; declare them once — the section is the one meant for prose", file, "## "+coreLoopSupervisorHeading)
		}
		spec.SupervisorProfile.Instructions = instructions
	}
	return nil
}

// decodeCoreLoopSpecYAML decodes the fenced spec, refusing anything it
// cannot fully account for.
//
// KnownFields is on because a misspelled key is otherwise invisible: the
// loop boots, the setting its author wrote does nothing, and the only
// evidence is behaviour that never changes.
func decodeCoreLoopSpecYAML(specYAML, file string) (looppkg.Spec, error) {
	decoder := yaml.NewDecoder(strings.NewReader(specYAML))
	decoder.KnownFields(true)
	var spec looppkg.Spec
	if err := decoder.Decode(&spec); err != nil {
		return looppkg.Spec{}, fmt.Errorf("%s: %w", file, err)
	}
	// A stray "---" inside the fence starts a second YAML document, and
	// everything after it would be dropped without a word. That is the
	// same silence KnownFields exists to prevent, so it is refused the
	// same way.
	var extra looppkg.Spec
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return looppkg.Spec{}, fmt.Errorf("%s: the spec block holds more than one yaml document; a stray \"---\" splits it, and everything after the first document would be ignored", file)
	}
	return spec, nil
}

// splitCoreLoopSections collects the document's reserved sections.
// Anything before the first one — frontmatter, a title, a lead paragraph
// — is not a section and is ignored, which is what lets these be
// ordinary documents carrying ordinary metadata.
//
// Only an H2 whose text is exactly a reserved name delimits. A prompt is
// a document in its own right and uses H2 freely: the three being ported
// carry seventeen between them, "## Guidelines" and "## What To Do This
// Iteration" among them. Treating every "## " as a boundary would shred
// each prompt into fragments at load, so the rule is the one the facet
// contract already uses — a heading is structure only if the contract
// named it, and everything else is content.
//
// Fenced blocks are skipped for the same reason at one remove: a prompt
// may quote a markdown example, and a "## Spec" line inside it is a
// quotation rather than a section.
func splitCoreLoopSections(raw string) map[string]string {
	sections := make(map[string]string, 3)
	current := ""
	fenced := false
	var lines []string
	flush := func() {
		if current != "" {
			sections[current] = strings.TrimSpace(strings.Join(lines, "\n"))
		}
		lines = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
		}
		if !fenced {
			if heading, ok := reservedCoreLoopHeading(trimmed); ok {
				flush()
				current = heading
				continue
			}
		}
		if current != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return sections
}

// reservedCoreLoopHeading reports the section a line opens, if the line
// is an H2 naming one. Matching is case-insensitive because an author
// hand-writing the document should not have to match our capitalization.
func reservedCoreLoopHeading(trimmed string) (string, bool) {
	text, ok := strings.CutPrefix(trimmed, "## ")
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	for _, heading := range []string{coreLoopSpecHeading, coreLoopTaskHeading, coreLoopSupervisorHeading} {
		if strings.EqualFold(text, heading) {
			return heading, true
		}
	}
	return "", false
}

// unfenceYAML unwraps a fenced block, reporting whether the section was
// one. A value carrying an interior close is refused: unwrapping it
// would splice two blocks into one.
func unfenceYAML(section string) (string, bool) {
	trimmed := strings.TrimSpace(section)
	opener := ""
	for _, candidate := range []string{"```yaml", "```yml", "```"} {
		if strings.HasPrefix(trimmed, candidate) {
			opener = candidate
			break
		}
	}
	if opener == "" || !strings.HasSuffix(trimmed, "```") {
		return "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, opener), "```")
	if strings.Contains(inner, "```") {
		return "", false
	}
	return inner, true
}

// expandExcludeToolTokens replaces symbolic exclusions with the names
// they stand for, leaving ordinary tool names untouched and dropping
// duplicates an expansion may introduce.
func expandExcludeToolTokens(excludes []string) []string {
	if len(excludes) == 0 {
		return excludes
	}
	out := make([]string, 0, len(excludes))
	seen := make(map[string]struct{}, len(excludes))
	add := func(name string) {
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, exclude := range excludes {
		if strings.TrimSpace(exclude) == excludeToolsDirectHumanEgress {
			for _, name := range tools.DirectHumanEgressToolNames() {
				add(name)
			}
			continue
		}
		add(exclude)
	}
	return out
}
