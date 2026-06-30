package tools

import (
	"sort"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestLoopToolsAreCatalogued guards against the loop_reparent (#1105) gap: a loop
// tool can ship with a working handler yet be invisible to every model loop if it
// has no toolcatalog entry — the catalog is what assigns the `loops` tag and makes
// a tool discoverable and offered. loop_reparent was registered but never
// catalogued, so the model was never offered it and reparented by hand.
//
// This enumerates the loop tool families (definition + runtime) and asserts every
// registered tool has a catalog entry, so a future loop tool can't silently ship
// dead the same way.
func TestLoopToolsAreCatalogued(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	r := NewEmptyRegistry()
	// Stub registries are enough to register the tools; handlers aren't called.
	r.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{Registry: defs})
	r.ConfigureLoopRuntimeTools(LoopRuntimeToolDeps{Registry: looppkg.NewRegistry()})

	var uncatalogued []string
	for name := range r.tools {
		if _, ok := toolcatalog.LookupBuiltinToolSpec(name); !ok {
			uncatalogued = append(uncatalogued, name)
		}
	}
	if len(uncatalogued) > 0 {
		sort.Strings(uncatalogued)
		t.Errorf("loop tools registered but absent from the builtin tool catalog — they will never be offered to a model loop: %v", uncatalogued)
	}
}
