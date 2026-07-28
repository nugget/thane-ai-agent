package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"gopkg.in/yaml.v3"
)

// coreLoopsDirName is the subdirectory of the core document root holding
// operator-authored loop definitions, one spec per YAML file.
const coreLoopsDirName = "loops"

// excludeToolsDirectHumanEgress expands to every direct human-egress
// tool name at load.
//
// The list is decided in Go — a tool becomes human-egress by what it
// does, not by an operator remembering to add it — so a YAML file that
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

// loadCoreLoopDefinitions reads every loop definition declared under
// <core>/loops.
//
// These are authoritative. A core-defined loop is part of the signed
// root the agent boots from, so the file is the definition: it wins over
// the built-in spec of the same name, and it is re-read every boot
// rather than seeded once. An operator editing the YAML and restarting
// is the supported way to change one, and a runtime edit that the file
// does not agree with does not survive.
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
		if entry.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".yaml", ".yml":
			names = append(names, entry.Name())
		}
	}
	// Sorted so the definition order a boot produces is a property of the
	// directory rather than of the filesystem's enumeration order.
	sort.Strings(names)

	specs := make([]looppkg.Spec, 0, len(names))
	seen := make(map[string]string, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		spec, err := decodeCoreLoopDefinition(path)
		if err != nil {
			return nil, err
		}
		if other, dup := seen[spec.Name]; dup {
			return nil, fmt.Errorf("%s and %s both define the loop %q; one file is one loop", other, name, spec.Name)
		}
		seen[spec.Name] = name
		specs = append(specs, spec)
	}
	return specs, nil
}

// decodeCoreLoopDefinition reads one spec file, refusing anything it
// cannot fully account for.
//
// KnownFields is on because a misspelled key in a definition file is
// otherwise invisible: the loop boots, the setting the operator wrote
// does nothing, and the only evidence is behaviour that never changes.
func decodeCoreLoopDefinition(path string) (looppkg.Spec, error) {
	file, err := os.Open(path)
	if err != nil {
		return looppkg.Spec{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var spec looppkg.Spec
	if err := decoder.Decode(&spec); err != nil {
		return looppkg.Spec{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}

	spec.ExcludeTools = expandExcludeToolTokens(spec.ExcludeTools)
	if err := spec.ValidatePersistable(); err != nil {
		return looppkg.Spec{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return spec, nil
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
