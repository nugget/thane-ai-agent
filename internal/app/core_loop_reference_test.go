package app

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/app/coreloops"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// loopDefinitionsReferencePath is the operator-facing reference for the
// loop definition document format, relative to this package.
const loopDefinitionsReferencePath = "../../docs/reference/loop-definitions.md"

// TestLoopDefinitionsReferenceCoversEverySpecKey pins the reference
// contract: docs/reference/loop-definitions.md documents the whole
// authorable spec surface. The document loader refuses unknown keys, so
// the accepted key names are discoverable nowhere except that reference
// — a Spec field it doesn't mention is invisible exactly where an
// operator would look. Adding a field to Spec fails this test until the
// reference documents it (backticked, the doc's convention for key
// names) in the same PR.
func TestLoopDefinitionsReferenceCoversEverySpecKey(t *testing.T) {
	raw, err := os.ReadFile(loopDefinitionsReferencePath)
	if err != nil {
		t.Fatalf("read reference doc: %v", err)
	}
	doc := string(raw)

	typ := reflect.TypeOf(looppkg.Spec{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if tag == "-" {
			continue
		}
		if tag == "" {
			t.Fatalf("Spec.%s has no yaml tag; authorable fields need one, runtime-only fields need yaml:\"-\"", field.Name)
		}
		if !strings.Contains(doc, "`"+tag+"`") {
			t.Errorf("docs/reference/loop-definitions.md does not document spec key %q; every authorable Spec field must appear there, backticked", tag)
		}
	}
}

// TestShippedCoreLoopDocumentsPointAtTheReference keeps the pointer
// honest: each shipped definition document opens its spec block with a
// comment naming the reference, so an operator editing an override
// knows where the full surface is documented without reading Go. The
// position is the contract, not just the presence — a pointer buried
// in prose or below the keys is not where an editing eye starts — so
// the check parses the ## Spec fence with the loader's own section
// helpers and requires the reference path inside the block's leading
// comment lines.
func TestShippedCoreLoopDocumentsPointAtTheReference(t *testing.T) {
	entries, err := coreloops.Documents.ReadDir("defaults")
	if err != nil {
		t.Fatalf("read embedded defaults: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded core loop documents; run go generate ./internal/app/coreloops/")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, err := coreloops.Documents.ReadFile("defaults/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		specSection, ok := splitCoreLoopSections(string(raw))[coreLoopSpecHeading]
		if !ok {
			t.Errorf("%s: no %q section", entry.Name(), "## "+coreLoopSpecHeading)
			continue
		}
		specYAML, ok := unfenceYAML(specSection)
		if !ok {
			t.Errorf("%s: the %q section is not a single yaml fence", entry.Name(), "## "+coreLoopSpecHeading)
			continue
		}
		var lead []string
		for _, line := range strings.Split(specYAML, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				lead = append(lead, trimmed)
				continue
			}
			break
		}
		if !strings.Contains(strings.Join(lead, "\n"), "docs/reference/loop-definitions.md") {
			t.Errorf("%s: the spec block does not open with a comment pointing at docs/reference/loop-definitions.md; the pointer is how an operator finds the full key reference, and its place is the top of the block", entry.Name())
		}
	}
}
