package agent

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
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

// TestRun_DelegateShapedRequestSeesDynamicTools pins the documented
// contract for tools that reach runs through the dynamic overlay (MCP
// tools that appear after startup, and changed definitions of startup
// tools): delegates run through Loop.Run like the main loop, so a
// delegate whose tag scope covers such a tool is offered it, with the
// current definition, while tools outside its scope stay hidden. The
// request mirrors the delegate executor's loop launch.
func TestRun_DelegateShapedRequestSeesDynamicTools(t *testing.T) {
	mock := &mockLLM{responses: []*llm.ChatResponse{{
		Model:   "test-model",
		Message: llm.Message{Role: "assistant", Content: "done"},
	}}}
	capTags := map[string]config.CapabilityTagConfig{
		"base":  {Tools: []string{"base_tool"}},
		"game":  {Tools: []string{"mcp_game_look"}},
		"other": {Tools: []string{"other_tool"}},
		"admin": {},
	}
	loop := setupCapabilityLoopWithClient(mock, []string{"base_tool", "mcp_game_look", "other_tool"}, capTags, "test-model")
	loop.SetDynamicToolSource(fakeDynamicSource{
		tools: []*tools.Tool{
			dynTool("mcp_game_move"),  // first appeared after startup
			dynTool("mcp_game_look"),  // definition changed after startup
			dynTool("mcp_admin_wipe"), // appeared after startup, outside the scope
		},
		tagAdds: map[string][]string{
			"game":  {"mcp_game_move", "mcp_game_look"},
			"admin": {"mcp_admin_wipe"},
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages:         []Message{{Role: "user", Content: "Move the player north."}},
		InitialTags:      []string{"game", "base"},
		DelegationGating: "disabled",
		ExcludeTools:     []string{"thane_delegate"},
		UsageRole:        "delegate",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(mock.calls) == 0 {
		t.Fatal("no LLM call recorded")
	}

	offered := make(map[string]string)
	for _, def := range mock.calls[0].Tools {
		fn, _ := def["function"].(map[string]any)
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		offered[name] = desc
	}
	for _, name := range []string{"base_tool", "mcp_game_move", "mcp_game_look"} {
		if _, ok := offered[name]; !ok {
			t.Errorf("delegate-shaped run was not offered %s; offered %v", name, toolNames(mock.calls[0].Tools))
		}
	}
	if got := offered["mcp_game_look"]; got != "dynamic mcp_game_look" {
		t.Errorf("mcp_game_look description = %q, want the changed definition from the overlay", got)
	}
	for _, name := range []string{"mcp_admin_wipe", "other_tool"} {
		if _, ok := offered[name]; ok {
			t.Errorf("delegate-shaped run was offered %s outside its tag scope", name)
		}
	}
}
