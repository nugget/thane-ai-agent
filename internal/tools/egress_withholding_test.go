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
