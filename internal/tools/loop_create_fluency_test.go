package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// dryRunSpec runs thane_loop_create in dry-run and returns the decoded
// spec, exercising the guided path exactly as a caller would without
// needing a registry to accept the result.
func dryRunSpec(t *testing.T, args map[string]any) (looppkg.Spec, map[string]any) {
	t.Helper()
	rig := newCurateTestRig(t)
	args["dry_run"] = true
	out, err := rig.tool.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("thane_loop_create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	specJSON, err := json.Marshal(result["spec"])
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var specArg map[string]any
	if err := json.Unmarshal(specJSON, &specArg); err != nil {
		t.Fatalf("spec is not an object: %v", err)
	}
	spec, err := decodeLoopSpecArg(map[string]any{"spec": specArg}, "spec")
	if err != nil {
		t.Fatalf("guided spec does not decode: %v", err)
	}
	return spec, result
}

func curateArgs(extra map[string]any) map[string]any {
	out := map[string]any{
		"name":      "closet_guardian",
		"intent":    "Keep the server closet's state legible.",
		"operation": "service",
		"sleep_min": "10m",
		"sleep_max": "30m",
	}
	output := map[string]any{"document": "kb:dashboards/closet.md"}
	for k, v := range extra {
		output[k] = v
	}
	out["output"] = output
	return out
}

// TestGuidedCreateProducesTieredOutput is the hole that started #1287:
// the front door could not express the shape its own doctrine calls the
// important case, and dropped tiers silently when asked.
func TestGuidedCreateProducesTieredOutput(t *testing.T) {
	spec, result := dryRunSpec(t, curateArgs(map[string]any{
		"tiers": []any{"status_line", "teaser", "digest"},
	}))

	// The document plus the notes surface every document-owning loop gets.
	if len(spec.Outputs) != 2 {
		t.Fatalf("outputs = %d, want the tiered document plus its notes", len(spec.Outputs))
	}
	if got := len(spec.Outputs[0].Tiers); got != 3 {
		t.Fatalf("tiers = %v, want three projections", spec.Outputs[0].Tiers)
	}
	if got := spec.Outputs[0].ToolName(); !strings.HasPrefix(got, "publish_output_") {
		t.Errorf("generated tool = %q, want publish_output_*", got)
	}
	// The per-iteration task must describe publishing, not a body rewrite —
	// a loop told to "update the body" through a publish tool is being
	// taught the wrong shape by its own definition.
	if !strings.Contains(spec.Task, "Publish") {
		t.Errorf("task does not describe publishing: %q", spec.Task)
	}
	if result["tiers"] == nil {
		t.Error("result should report the declared tiers")
	}
}

// TestGuidedCreateWorkingNotes covers the other half of the curating
// shape. Notes are internal by construction, which is what keeps a
// loop's reasoning out of what it publishes.
func TestGuidedCreateWorkingNotes(t *testing.T) {
	// No opt-in argument: the notes surface is unconditional, and passing a
	// key the schema does not define would imply a flag that does not exist.
	spec, result := dryRunSpec(t, curateArgs(map[string]any{
		"tiers": []any{"status_line"},
	}))

	if len(spec.Outputs) != 2 {
		t.Fatalf("outputs = %d, want the tiered document plus its notes", len(spec.Outputs))
	}
	notes := spec.Outputs[1]
	if notes.Type != looppkg.OutputTypeWorkingNotes {
		t.Errorf("second output type = %q, want working_notes", notes.Type)
	}
	if notes.Ref != "kb:dashboards/closet-notes.md" {
		t.Errorf("notes ref = %q, want the document's path with a -notes suffix", notes.Ref)
	}
	if result["working_notes_document"] != "kb:dashboards/closet-notes.md" {
		t.Errorf("result should name the notes document it created, got %v", result["working_notes_document"])
	}
}

// TestGuidedCreateRefusesUnknownTier pins the failure direction. A tier
// name that is quietly dropped yields a loop publishing fewer
// projections than its author asked for, with nothing to say so.
func TestGuidedCreateRefusesUnknownTier(t *testing.T) {
	rig := newCurateTestRig(t)
	args := curateArgs(map[string]any{"tiers": []any{"status_line", "summary"}})
	args["dry_run"] = true
	if _, err := rig.tool.Handler(context.Background(), args); err == nil {
		t.Fatal("unknown tier should be refused, not dropped")
	} else if !strings.Contains(err.Error(), "summary") {
		t.Errorf("error should name the offending tier: %v", err)
	}
}

// TestGuidedCreateTierInputShapes covers the two array forms a caller can
// present and the element that is neither. A non-string coerced to ""
// would be reported as an empty tier name, which names the symptom
// rather than the mistake.
func TestGuidedCreateTierInputShapes(t *testing.T) {
	t.Run("[]string is accepted", func(t *testing.T) {
		got, err := parseOutputTiers([]string{"status_line", "digest"})
		if err != nil {
			t.Fatalf("[]string: %v", err)
		}
		if len(got) != 2 || got[0] != looppkg.OutputTierStatusLine {
			t.Errorf("tiers = %v", got)
		}
	})

	t.Run("non-string element is named by index and type", func(t *testing.T) {
		_, err := parseOutputTiers([]any{"status_line", 7})
		if err == nil {
			t.Fatal("a numeric tier should be refused")
		}
		for _, want := range []string{"[1]", "int"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})

	t.Run("a non-array is refused by type", func(t *testing.T) {
		if _, err := parseOutputTiers("status_line"); err == nil {
			t.Fatal("a bare string should be refused")
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		got, err := parseOutputTiers(nil)
		if err != nil || got != nil {
			t.Errorf("nil = (%v, %v), want (nil, nil)", got, err)
		}
	})
}

// TestGuidedCreateAlwaysDerivesNotes pins that the notes surface is not
// a choice. An opt-in every caller should take is a default in the wrong
// position, and the cost of an unused one is a single context-block line
// — nothing is scaffolded until the loop writes to it.
func TestGuidedCreateAlwaysDerivesNotes(t *testing.T) {
	spec, result := dryRunSpec(t, curateArgs(nil))
	if len(spec.Outputs) != 2 {
		t.Fatalf("outputs = %d, want the document plus its notes", len(spec.Outputs))
	}
	if spec.Outputs[1].Type != looppkg.OutputTypeWorkingNotes {
		t.Errorf("second output = %q, want working_notes", spec.Outputs[1].Type)
	}
	if result["working_notes_document"] == nil {
		t.Error("the derived notes document must be reported, not silently created")
	}
}

// TestGuidedCreateRefusesNotesCollision covers the hazard a derived path
// carries that a supplied one does not: the caller never chose this ref,
// so appending a loop's private reasoning onto whatever already lives
// there would be a surprise it had no way to anticipate.
func TestGuidedCreateRefusesNotesCollision(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	if _, err := rig.docTools.Write(ctx, documents.WriteArgs{
		Ref:   "kb:dashboards/closet-notes.md",
		Title: "Something else entirely",
		Body:  strPtr("prior content"),
	}); err != nil {
		t.Fatalf("seed colliding document: %v", err)
	}

	_, err := rig.tool.Handler(ctx, curateArgs(nil))
	if err == nil {
		t.Fatal("a derived notes ref that already exists should refuse")
	}
	if !strings.Contains(err.Error(), "closet-notes.md") {
		t.Errorf("error should name the colliding document: %v", err)
	}
}

func strPtr(s string) *string { return &s }
