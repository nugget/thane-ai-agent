package tools

import (
	"context"
	"testing"
)

func dynTool(name string) *Tool {
	return &Tool{
		Name:        name,
		Description: "dynamic " + name,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return name + "-result", nil
		},
	}
}

// TestWithDynamicTools_EmptyIsIdentity confirms an empty overlay returns the
// receiver unchanged (the zero-companion hot path must not allocate a copy).
func TestWithDynamicTools_EmptyIsIdentity(t *testing.T) {
	r := newTestRegistry()
	if got := r.WithDynamicTools(nil, nil); got != r {
		t.Error("empty overlay should return the receiver unchanged")
	}
	if got := r.WithDynamicTools([]*Tool{}, map[string][]string{}); got != r {
		t.Error("empty (non-nil) overlay should return the receiver unchanged")
	}
}

// TestWithDynamicTools_LayersTagGated confirms a dynamic tool is added,
// stays NON-Core, and is reachable only under its own tag.
func TestWithDynamicTools_LayersTagGated(t *testing.T) {
	r := newTestRegistry()
	r.SetTagIndex(map[string][]string{"group_a": {"alpha"}})

	overlay := r.WithDynamicTools(
		[]*Tool{dynTool("macos_search_contacts")},
		map[string][]string{"companion": {"macos_search_contacts"}},
	)

	// Present in the overlay registry.
	if overlay.Get("macos_search_contacts") == nil {
		t.Fatal("dynamic tool not registered in overlay")
	}
	// Must be NON-Core so it stays tag-gated.
	if overlay.Get("macos_search_contacts").Core {
		t.Error("dynamic tool must not be Core")
	}

	// Visible under its own tag.
	underCompanion := overlay.FilterByTags([]string{"companion"}).AllToolNames()
	if !containsName(underCompanion, "macos_search_contacts") {
		t.Error("dynamic tool should resolve under companion tag")
	}
	// Invisible under an unrelated tag.
	underA := overlay.FilterByTags([]string{"group_a"}).AllToolNames()
	if containsName(underA, "macos_search_contacts") {
		t.Error("dynamic tool should not leak into an unrelated tag")
	}
	if !containsName(underA, "alpha") {
		t.Error("pre-existing tagged tool should still resolve")
	}
}

// TestWithDynamicTools_DoesNotMutateSource guards the load-bearing invariant
// that the shared, lock-free registry and its tag index are never mutated.
func TestWithDynamicTools_DoesNotMutateSource(t *testing.T) {
	r := newTestRegistry()
	r.SetTagIndex(map[string][]string{"companion": {"alpha"}})

	_ = r.WithDynamicTools(
		[]*Tool{dynTool("macos_list_reminders")},
		map[string][]string{"companion": {"macos_list_reminders"}},
	)

	if r.Get("macos_list_reminders") != nil {
		t.Error("overlay leaked a tool into the source registry")
	}
	srcCompanion := r.FilterByTags([]string{"companion"}).AllToolNames()
	if containsName(srcCompanion, "macos_list_reminders") {
		t.Error("overlay mutated the source tag index")
	}
}

// TestWithDynamicTools_NameCollisionShadows confirms a dynamic tool with the
// same name as a registry tool shadows it in the overlay (last writer wins),
// matching Register semantics — this is how a Mac-authored calendar tool
// replaces the hand-coded floor.
func TestWithDynamicTools_NameCollisionShadows(t *testing.T) {
	r := newTestRegistry()
	out, err := r.Execute(context.Background(), "alpha", "")
	if err != nil || out != "alpha-result" {
		t.Fatalf("baseline alpha: out=%q err=%v", out, err)
	}

	shadow := &Tool{
		Name:        "alpha",
		Description: "shadowed alpha",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "shadowed", nil
		},
	}
	overlay := r.WithDynamicTools([]*Tool{shadow}, nil)

	out, err = overlay.Execute(context.Background(), "alpha", "")
	if err != nil || out != "shadowed" {
		t.Fatalf("overlay alpha should be shadowed: out=%q err=%v", out, err)
	}
	// Source unchanged.
	out, _ = r.Execute(context.Background(), "alpha", "")
	if out != "alpha-result" {
		t.Errorf("source alpha mutated: %q", out)
	}
}
