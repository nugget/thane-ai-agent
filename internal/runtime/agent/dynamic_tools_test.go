package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

type fakeDynamicSource struct {
	tools   []*tools.Tool
	tagAdds map[string][]string
}

func (f fakeDynamicSource) Snapshot() ([]*tools.Tool, map[string][]string) {
	return f.tools, f.tagAdds
}

func dynTool(name string) *tools.Tool {
	return &tools.Tool{
		Name:        name,
		Description: "dynamic " + name,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return name, nil
		},
	}
}

func contains(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// TestApplyDynamicTools_NilSourceIsIdentity confirms the overlay is a no-op
// when no source is installed.
func TestApplyDynamicTools_NilSourceIsIdentity(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	l := &Loop{tools: reg}
	if got := l.applyDynamicTools(reg); got != reg {
		t.Error("nil dynamic source should return base unchanged")
	}
}

// TestApplyDynamicTools_EmptySnapshotIsIdentity confirms a connected-but-empty
// source (e.g. a companion with no authored tools) does not allocate a copy.
func TestApplyDynamicTools_EmptySnapshotIsIdentity(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	l := &Loop{tools: reg, dynamicTools: fakeDynamicSource{}}
	if got := l.applyDynamicTools(reg); got != reg {
		t.Error("empty snapshot should return base unchanged")
	}
}

// TestApplyDynamicTools_LayersAndTagGates confirms the loop layers in the
// source's tools, tag-gated, exactly as the per-run seam requires.
func TestApplyDynamicTools_LayersAndTagGates(t *testing.T) {
	reg := tools.NewEmptyRegistry()
	reg.Register(dynTool("base_tool"))
	reg.SetTagIndex(map[string][]string{"base": {"base_tool"}})

	src := fakeDynamicSource{
		tools:   []*tools.Tool{dynTool("macos_search_contacts")},
		tagAdds: map[string][]string{"companion": {"macos_search_contacts"}},
	}
	l := &Loop{tools: reg, dynamicTools: src}

	got := l.applyDynamicTools(reg)
	if got == reg {
		t.Fatal("expected a new overlay registry")
	}
	if got.Get("macos_search_contacts") == nil {
		t.Fatal("dynamic tool not layered into the run registry")
	}

	// Tag-gated: present under companion, absent under base.
	if names := got.FilterByTags([]string{"companion"}).AllToolNames(); !contains(names, "macos_search_contacts") {
		t.Error("dynamic tool should resolve under companion tag")
	}
	if names := got.FilterByTags([]string{"base"}).AllToolNames(); contains(names, "macos_search_contacts") {
		t.Error("dynamic tool should not leak into an unrelated tag")
	}

	// Source registry untouched.
	if contains(reg.AllToolNames(), "macos_search_contacts") {
		t.Error("overlay leaked into the shared registry")
	}
}
