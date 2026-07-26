package tools

import (
	"context"
	"strings"
	"testing"
)

func newEgressTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(nil, nil, nil)
	for _, name := range []string{"signal_send_message", "doc_read"} {
		toolName := name
		r.Register(&Tool{
			Name:        toolName,
			Description: "test tool",
			Parameters:  map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (string, error) {
				return toolName + " ran", nil
			},
		})
	}
	return r
}

func TestWithholdDirectHumanEgressBlocksEgressToolsOnly(t *testing.T) {
	r := newEgressTestRegistry(t)
	r.WithholdDirectHumanEgress()

	_, err := r.Execute(context.Background(), "signal_send_message", "{}")
	if err == nil {
		t.Fatal("an egress tool must be refused while the config is unverified")
	}
	// The refusal has to read as a withheld capability, not a missing
	// tool, or the model looks for another way to reach the same person.
	if !strings.Contains(err.Error(), "withheld") || !strings.Contains(err.Error(), "trust boundary") {
		t.Fatalf("error should explain the state: %v", err)
	}
	if !strings.Contains(err.Error(), "reply instead") {
		t.Fatalf("error should say what to do instead: %v", err)
	}

	if got, err := r.Execute(context.Background(), "doc_read", "{}"); err != nil || got != "doc_read ran" {
		t.Fatalf("a non-egress tool must still run: %q %v", got, err)
	}
}

func TestEgressToolsRunWhenVerified(t *testing.T) {
	r := newEgressTestRegistry(t)
	if got, err := r.Execute(context.Background(), "signal_send_message", "{}"); err != nil || got != "signal_send_message ran" {
		t.Fatalf("egress must work on a verified instance: %q %v", got, err)
	}
}

func TestIsDirectHumanEgressToolCoversTheDeclaredSet(t *testing.T) {
	for _, name := range DirectHumanEgressToolNames() {
		if !IsDirectHumanEgressTool(name) {
			t.Fatalf("%q is in the declared egress set but not recognized", name)
		}
	}
	if IsDirectHumanEgressTool("doc_read") {
		t.Fatal("doc_read is not direct human egress")
	}
}

// TestWithholdingSurvivesEveryDerivedRegistry is the test that would
// have caught the original bug: withholding lived on the base registry
// while every scoped copy silently regained the capability. Loops and
// delegates run through those copies, so the denial held precisely where
// it did not matter and lapsed where it did.
func TestWithholdingSurvivesEveryDerivedRegistry(t *testing.T) {
	base := newEgressTestRegistry(t)
	base.Register(&Tool{
		Name:        "tagged_tool",
		Description: "tag-scoped",
		Parameters:  map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) (string, error) { return "ok", nil },
	})
	base.WithholdDirectHumanEgress()

	derived := map[string]*Registry{
		"FilteredCopy":          base.FilteredCopy([]string{"signal_send_message", "doc_read"}),
		"FilteredCopyExcluding": base.FilteredCopyExcluding([]string{"tagged_tool"}),
		"FilterByTags/untagged": base.FilterByTags(nil),
		"WithRuntimeTools":      base.WithRuntimeTools([]*Tool{{Name: "runtime_tool", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }}}),
		"WithDynamicTools":      base.WithDynamicTools([]*Tool{{Name: "dynamic_tool", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }}}, nil),
	}
	// A copy of a copy is the realistic shape: a delegate's registry is
	// derived from a loop's, which is derived from the base.
	derived["nested"] = derived["FilterByTags/untagged"].FilteredCopy([]string{"signal_send_message"})

	for name, reg := range derived {
		if reg.Get("signal_send_message") == nil {
			continue // this copy legitimately excludes the tool
		}
		if _, err := reg.Execute(context.Background(), "signal_send_message", "{}"); err == nil {
			t.Fatalf("%s regained direct human egress after the base withheld it", name)
		}
	}
}

func TestWithholdingAppliesToCopiesMadeBeforeItWasSet(t *testing.T) {
	// Policy is shared state, not a snapshot: a registry derived during
	// wiring must honor a withholding decision made after it was built.
	base := newEgressTestRegistry(t)
	derived := base.FilterByTags(nil)

	base.WithholdDirectHumanEgress()

	if _, err := derived.Execute(context.Background(), "signal_send_message", "{}"); err == nil {
		t.Fatal("a registry derived before the decision must still honor it")
	}
}
