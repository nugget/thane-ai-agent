package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestLoopToolsAreCatalogued guards against the loop_reparent (#1105) gap: a loop
// tool can ship with a working handler yet be invisible to every model loop if it
// isn't actually offered to loops-gated sessions. Being offered takes two things —
// a toolcatalog entry AND the `loops` capability tag on that entry, since the tag
// is what gates visibility. loop_reparent was registered but never catalogued, so
// the model was never offered it and reparented loops by hand.
//
// This enumerates the loop tool families (definition + runtime) and asserts every
// registered tool is both catalogued and `loops`-tagged, so a future loop tool
// can't silently ship dead either way: by skipping the catalog entirely, or by
// landing a catalog entry without the tag that actually gates it into scope.
func TestLoopToolsAreCatalogued(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	// Start from an empty registry and configure only the loop tool families, so
	// AllToolNames() is exactly the set under test.
	r := NewEmptyRegistry()
	// Stub registries are enough to register the tools; handlers aren't called.
	r.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{Registry: defs})
	r.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{Registry: looppkg.NewRegistry()})

	var problems []string
	for _, name := range r.AllToolNames() {
		spec, ok := toolcatalog.LookupBuiltinToolSpec(name)
		if !ok {
			problems = append(problems, name+" — no builtin catalog entry")
			continue
		}
		if !slices.Contains(spec.Tags, "loops") {
			problems = append(problems, name+" — catalogued but missing the \"loops\" tag")
		}
	}
	if len(problems) > 0 {
		t.Errorf("loop tools that will not be offered to a loops-gated model session:\n  %s",
			strings.Join(problems, "\n  "))
	}
}
