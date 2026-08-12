package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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

// TestGuidedCreateProducesFacetedOutput is the hole that started #1287:
// the front door could not express the shape its own doctrine calls the
// important case, and dropped facets silently when asked.
func TestGuidedCreateProducesFacetedOutput(t *testing.T) {
	spec, result := dryRunSpec(t, curateArgs(map[string]any{
		"facets": []any{"status_line", "teaser", "digest"},
	}))

	// The document plus the notes surface every document-owning loop gets.
	if len(spec.Outputs) != 2 {
		t.Fatalf("outputs = %d, want the faceted document plus its notes", len(spec.Outputs))
	}
	if got := len(spec.Outputs[0].Facets); got != 3 {
		t.Fatalf("facets = %v, want three projections", spec.Outputs[0].Facets)
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
	if result["facets"] == nil {
		t.Error("result should report the declared facets")
	}
}

// TestGuidedCreateWorkingNotes covers the other half of the curating
// shape. Notes are internal by construction, which is what keeps a
// loop's reasoning out of what it publishes.
func TestGuidedCreateWorkingNotes(t *testing.T) {
	// No opt-in argument: the notes surface is unconditional, and passing a
	// key the schema does not define would imply a flag that does not exist.
	spec, result := dryRunSpec(t, curateArgs(map[string]any{
		"facets": []any{"status_line"},
	}))

	if len(spec.Outputs) != 2 {
		t.Fatalf("outputs = %d, want the faceted document plus its notes", len(spec.Outputs))
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

// TestGuidedCreatePromptMode covers the front door's half of the #1171
// convention: an explicit "task" lands on the derived spec, and an
// omitted argument stays empty — the runtime default is full, but only
// a caller's explicit choice becomes a durable pin.
func TestGuidedCreatePromptMode(t *testing.T) {
	t.Run("explicit task mode lands on the spec", func(t *testing.T) {
		args := curateArgs(nil)
		args["prompt_mode"] = "task"
		spec, _ := dryRunSpec(t, args)
		if spec.PromptMode != agentctx.PromptModeTask {
			t.Errorf("PromptMode = %q, want task", spec.PromptMode)
		}
	})

	t.Run("omitted mode stays unset", func(t *testing.T) {
		spec, _ := dryRunSpec(t, curateArgs(nil))
		if spec.PromptMode != "" {
			t.Errorf("PromptMode = %q, want empty (no silent pin)", spec.PromptMode)
		}
	})

	t.Run("invalid mode is refused with a teaching error", func(t *testing.T) {
		rig := newCurateTestRig(t)
		args := curateArgs(nil)
		args["prompt_mode"] = "compact"
		args["dry_run"] = true
		_, err := rig.tool.Handler(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "prompt_mode") {
			t.Fatalf("err = %v, want prompt_mode rejection naming valid values", err)
		}
	})

	t.Run("containers refuse the argument", func(t *testing.T) {
		rig := newCurateTestRig(t)
		_, err := rig.tool.Handler(context.Background(), map[string]any{
			"name":        "grouping",
			"intent":      "Group related loops.",
			"operation":   "container",
			"prompt_mode": "task",
			"dry_run":     true,
		})
		if err == nil || !strings.Contains(err.Error(), "prompt_mode") {
			t.Fatalf("err = %v, want container prompt_mode rejection", err)
		}
	})
}

// TestGuidedCreateRefusesUnknownFacet pins the failure direction. A facet
// name that is quietly dropped yields a loop publishing fewer
// projections than its author asked for, with nothing to say so.
func TestGuidedCreateRefusesUnknownFacet(t *testing.T) {
	rig := newCurateTestRig(t)
	args := curateArgs(map[string]any{"facets": []any{"status_line", "summary"}})
	args["dry_run"] = true
	if _, err := rig.tool.Handler(context.Background(), args); err == nil {
		t.Fatal("unknown facet should be refused, not dropped")
	} else if !strings.Contains(err.Error(), "summary") {
		t.Errorf("error should name the offending facet: %v", err)
	}
}

// TestGuidedCreateFacetInputShapes covers the two array forms a caller can
// present and the element that is neither. A non-string coerced to ""
// would be reported as an empty facet name, which names the symptom
// rather than the mistake.
func TestGuidedCreateFacetInputShapes(t *testing.T) {
	t.Run("[]string is accepted", func(t *testing.T) {
		got, err := parseOutputFacets([]string{"status_line", "digest"})
		if err != nil {
			t.Fatalf("[]string: %v", err)
		}
		if len(got) != 2 || got[0].Name != looppkg.OutputFacetStatusLine {
			t.Errorf("facets = %v", got)
		}
	})

	t.Run("non-string element is named by index and type", func(t *testing.T) {
		_, err := parseOutputFacets([]any{"status_line", 7})
		if err == nil {
			t.Fatal("a numeric facet should be refused")
		}
		for _, want := range []string{"[1]", "int"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})

	t.Run("a non-array is refused by type", func(t *testing.T) {
		if _, err := parseOutputFacets("status_line"); err == nil {
			t.Fatal("a bare string should be refused")
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		got, err := parseOutputFacets(nil)
		if err != nil || got != nil {
			t.Errorf("nil = (%v, %v), want (nil, nil)", got, err)
		}
	})
}

// TestGuidedCreateAlwaysDerivesNotes pins that the notes surface is not
// a choice. An opt-in every caller should take is a default in the wrong
// position, and the cost of an unused one is a scaffolded stub and a
// context-block line saying it exists.
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

// TestGuidedCreateScaffoldsFacetSkeleton pins what the loop's first
// iteration sees: a faceted output's scaffold is the exact section
// skeleton its publish tool fills, stamped with the same ownership
// frontmatter every later publish re-stamps. A scaffold shaped unlike a
// published document would teach the first turn the wrong form.
func TestGuidedCreateScaffoldsFacetSkeleton(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	args := curateArgs(map[string]any{"facets": []any{"status_line", "digest"}})
	out, err := rig.tool.Handler(ctx, args)
	if err != nil {
		t.Fatalf("thane_loop_create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["document_state"] != "scaffolded" {
		t.Errorf("document_state = %v, want scaffolded", result["document_state"])
	}

	doc, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet.md"})
	if err != nil {
		t.Fatalf("read scaffold: %v", err)
	}
	for _, want := range []string{
		"## Status Line", "## Digest", "## Details",
		"awaiting first cycle",
		`"audience"`, `"published"`,
		`"managed_by"`, `"publish_output_closet_guardian"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("faceted scaffold missing %q:\n%s", want, doc)
		}
	}
	// Undeclared sections must not appear: a heading the publish tool
	// will never fill reads as structure the loop should maintain.
	if strings.Contains(doc, "## Teaser") {
		t.Errorf("scaffold has undeclared Teaser section:\n%s", doc)
	}
	if strings.Contains(doc, "Current State") {
		t.Errorf("faceted scaffold should not carry the unfaceted body shape:\n%s", doc)
	}
}

// TestGuidedCreateScaffoldsWorkingNotes pins that the derived notes
// document exists before the first cycle, marked internal from birth —
// the audience gate reads the document, not the spec, so a notes doc
// that only became internal on its first write would sit readable in
// search until the loop got around to thinking.
func TestGuidedCreateScaffoldsWorkingNotes(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	out, err := rig.tool.Handler(ctx, curateArgs(map[string]any{"facets": []any{"status_line"}}))
	if err != nil {
		t.Fatalf("thane_loop_create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["working_notes_state"] != "scaffolded" {
		t.Errorf("working_notes_state = %v, want scaffolded", result["working_notes_state"])
	}

	doc, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet-notes.md"})
	if err != nil {
		t.Fatalf("read notes scaffold: %v", err)
	}
	for _, want := range []string{
		`"audience"`, `"internal"`,
		`"managed_by"`, `"replace_output_closet_guardian_notes"`,
		"loop_definition_name",
		"awaiting first cycle",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("notes scaffold missing %q:\n%s", want, doc)
		}
	}
}

// TestGuidedCreateReplacePreservesDocuments pins the destructive edge of
// replace=true. Re-creating a definition is an iteration on the loop,
// not on its accumulated state: the maintained document carries the
// loop's current belief and the notes carry its private thinking, and a
// spec tweak that silently reset both to placeholders would destroy
// exactly what the next iteration needs most.
func TestGuidedCreateReplacePreservesDocuments(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	if _, err := rig.tool.Handler(ctx, curateArgs(map[string]any{"facets": []any{"status_line"}})); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// The loop has "run": both documents now hold real state.
	docBody := "## Status Line\n\nCloset nominal.\n\n## Details\n\nAccumulated belief."
	notesBody := "Current theory: the UPS fan is the noise."
	for ref, body := range map[string]string{
		"kb:dashboards/closet.md":       docBody,
		"kb:dashboards/closet-notes.md": notesBody,
	} {
		body := body
		if _, err := rig.docTools.Write(ctx, documents.WriteArgs{Ref: ref, Body: &body}); err != nil {
			t.Fatalf("simulate loop write to %s: %v", ref, err)
		}
	}

	args := curateArgs(map[string]any{"facets": []any{"status_line"}})
	args["replace"] = true
	out, err := rig.tool.Handler(ctx, args)
	if err != nil {
		t.Fatalf("replace create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["document_state"] != "preserved_existing" {
		t.Errorf("document_state = %v, want preserved_existing", result["document_state"])
	}
	if result["working_notes_state"] != "preserved_existing" {
		t.Errorf("working_notes_state = %v, want preserved_existing", result["working_notes_state"])
	}

	doc, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet.md"})
	if err != nil {
		t.Fatalf("read document after replace: %v", err)
	}
	if !strings.Contains(doc, "Accumulated belief.") {
		t.Errorf("replace clobbered the maintained document:\n%s", doc)
	}
	if strings.Contains(doc, "awaiting first cycle") {
		t.Errorf("replace re-scaffolded the maintained document:\n%s", doc)
	}
	notes, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet-notes.md"})
	if err != nil {
		t.Fatalf("read notes after replace: %v", err)
	}
	if !strings.Contains(notes, "UPS fan") {
		t.Errorf("replace clobbered the working notes:\n%s", notes)
	}
}

// TestGuidedCreateFailsClosedOnUnreadableOutputDoc pins the existence
// probe's failure direction. The probe's answer decides between
// scaffolding and preserving, so a read that fails for any reason
// other than not-found — unknown root, provenance verification, IO —
// must stop the create: misread as "absent", it would scaffold over
// whatever is actually there.
func TestGuidedCreateFailsClosedOnUnreadableOutputDoc(t *testing.T) {
	rig := newCurateTestRig(t)
	args := curateArgs(nil)
	args["output"] = map[string]any{"document": "vault:dashboards/closet.md"}

	_, err := rig.tool.Handler(context.Background(), args)
	if err == nil {
		t.Fatal("an unreadable output ref should refuse, not scaffold")
	}
	if !strings.Contains(err.Error(), "inspect output document") {
		t.Errorf("error should name the failed probe, got: %v", err)
	}
	if !strings.Contains(err.Error(), "vault:dashboards/closet.md") {
		t.Errorf("error should name the ref, got: %v", err)
	}
}

// seededArgs is curateArgs with a full-ladder facet declaration and a
// complete output.initial seed.
func seededArgs(initial map[string]any) map[string]any {
	return curateArgs(map[string]any{
		"facets":  []any{"status_line", "digest"},
		"initial": initial,
	})
}

// TestGuidedCreateSeedsFirstPublish pins the create-time seed: the
// creating model has just surveyed the domain, and the loop inheriting
// the document may run on a smaller model, so output.initial becomes a
// real first publish — rendered through the same contract the publish
// tool uses — instead of a placeholder skeleton.
func TestGuidedCreateSeedsFirstPublish(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	out, err := rig.tool.Handler(ctx, seededArgs(map[string]any{
		"status_line": "Closet 21.4°C, UPS on mains.",
		"digest":      "Environment stable all afternoon; dehumidifier idle.",
		"full":        "Temperature 21.4°C and steady. UPS on mains, full charge.",
		"notes":       "Starting theory: the UPS fan dominates the noise floor.",
	}))
	if err != nil {
		t.Fatalf("thane_loop_create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["document_state"] != "seeded" {
		t.Errorf("document_state = %v, want seeded", result["document_state"])
	}
	if result["working_notes_state"] != "seeded" {
		t.Errorf("working_notes_state = %v, want seeded", result["working_notes_state"])
	}

	doc, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet.md"})
	if err != nil {
		t.Fatalf("read seeded doc: %v", err)
	}
	for _, want := range []string{
		"## Status Line", "Closet 21.4°C, UPS on mains.",
		"## Digest", "dehumidifier idle",
		"## Details", "full charge",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("seeded document missing %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "awaiting first cycle") {
		t.Errorf("seeded document still carries placeholders:\n%s", doc)
	}
	notes, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet-notes.md"})
	if err != nil {
		t.Fatalf("read seeded notes: %v", err)
	}
	if !strings.Contains(notes, "UPS fan dominates") {
		t.Errorf("notes seed missing:\n%s", notes)
	}
}

// TestGuidedCreateSeedValidation pins that a seed passes the loop's own
// publish contract — a seed the publish tool would refuse must not
// become the document's first state, and the errors teach the same
// corrections.
func TestGuidedCreateSeedValidation(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]any
		wantErr string
	}{
		{
			name: "missing declared facet",
			initial: map[string]any{
				"status_line": "One line.",
				"full":        "Body.",
			},
			wantErr: "digest",
		},
		{
			name: "over budget status line",
			initial: map[string]any{
				"status_line": strings.Repeat("x", 121),
				"digest":      "Fine.",
				"full":        "Fine.",
			},
			wantErr: "limit is 120",
		},
		{
			name: "reserved heading inside content",
			initial: map[string]any{
				"status_line": "One line.",
				"digest":      "Fine.",
				"full":        "## Digest\n\nsmuggled section",
			},
			wantErr: "reserved section heading",
		},
		{
			name:    "unknown key refused, not dropped",
			initial: map[string]any{"summary": "nope"},
			wantErr: "output.initial.summary",
		},
		{
			name:    "teaser not declared by this output",
			initial: map[string]any{"teaser": "undeclared"},
			wantErr: "output.initial.teaser",
		},
		{
			name:    "empty notes",
			initial: map[string]any{"notes": "   "},
			wantErr: "output.initial.notes is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newCurateTestRig(t)
			_, err := rig.tool.Handler(context.Background(), seededArgs(tt.initial))
			if err == nil {
				t.Fatal("invalid seed should refuse")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestGuidedCreateSeedNotesOnly pins the half-seed: a starting theory
// without a first publish leaves the document on its placeholder
// skeleton while the notes open with a belief to revise.
func TestGuidedCreateSeedNotesOnly(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	out, err := rig.tool.Handler(ctx, seededArgs(map[string]any{
		"notes": "Watch whether the light sensor earns its keep.",
	}))
	if err != nil {
		t.Fatalf("thane_loop_create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["document_state"] != "scaffolded" {
		t.Errorf("document_state = %v, want scaffolded", result["document_state"])
	}
	if result["working_notes_state"] != "seeded" {
		t.Errorf("working_notes_state = %v, want seeded", result["working_notes_state"])
	}
	doc, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet.md"})
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	if !strings.Contains(doc, "awaiting first cycle") {
		t.Errorf("notes-only seed should leave the document scaffold:\n%s", doc)
	}
}

// TestGuidedCreateSeedUnfacetedTakesFullOnly pins the unfaceted shape:
// the document's whole body rides the full key, and projection keys are
// refused because this declaration publishes none.
func TestGuidedCreateSeedUnfacetedTakesFullOnly(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	out, err := rig.tool.Handler(ctx, curateArgs(map[string]any{
		"initial": map[string]any{"full": "# Closet\n\nFirst complete body."},
	}))
	if err != nil {
		t.Fatalf("thane_loop_create: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["document_state"] != "seeded" {
		t.Errorf("document_state = %v, want seeded", result["document_state"])
	}
	doc, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/closet.md"})
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	if !strings.Contains(doc, "First complete body.") {
		t.Errorf("unfaceted seed body missing:\n%s", doc)
	}

	rig2 := newCurateTestRig(t)
	_, err = rig2.tool.Handler(ctx, curateArgs(map[string]any{
		"initial": map[string]any{"status_line": "no ladder declared"},
	}))
	if err == nil || !strings.Contains(err.Error(), "output.initial.status_line") {
		t.Errorf("projection seed on an unfaceted output should refuse by key, got: %v", err)
	}
}

// TestGuidedCreateSeedRefusesExistingDocument pins the conflict
// direction: an existing document's accumulated state wins over
// create-time content, and dropping the seed silently would leave the
// caller believing content was published that never landed.
func TestGuidedCreateSeedRefusesExistingDocument(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	if _, err := rig.tool.Handler(ctx, curateArgs(map[string]any{"facets": []any{"status_line", "digest"}})); err != nil {
		t.Fatalf("first create: %v", err)
	}
	args := seededArgs(map[string]any{
		"status_line": "Fresh.",
		"digest":      "Fresh.",
		"full":        "Fresh.",
	})
	args["replace"] = true
	_, err := rig.tool.Handler(ctx, args)
	if err == nil {
		t.Fatal("seed against an existing document should refuse")
	}
	if !strings.Contains(err.Error(), "current state wins") {
		t.Errorf("error should explain the preserve-wins rule, got: %v", err)
	}
}

// TestGuidedCreateSeedDryRunWritesNothing pins that a valid seed still
// respects dry_run's whole reason to exist.
func TestGuidedCreateSeedDryRunWritesNothing(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	args := seededArgs(map[string]any{
		"status_line": "One line.",
		"digest":      "Enough to act on.",
		"full":        "Everything.",
		"notes":       "A theory.",
	})
	args["dry_run"] = true
	if _, err := rig.tool.Handler(ctx, args); err != nil {
		t.Fatalf("dry run with seed: %v", err)
	}
	for _, ref := range []string{"kb:dashboards/closet.md", "kb:dashboards/closet-notes.md"} {
		if _, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: ref}); err == nil {
			t.Errorf("dry run wrote %s", ref)
		}
	}
}

// TestGuidedCreateSalienceFlagsAreRegistryTargetOnly pins the guard on
// the create door: the salience flags widen area/label/floor expansion,
// so on a concrete entity they signal a misunderstanding the tool
// should correct rather than ignore.
func TestGuidedCreateSalienceFlagsAreRegistryTargetOnly(t *testing.T) {
	t.Run("accepted on an area target", func(t *testing.T) {
		entities, err := parseEntityList("entities", []any{
			map[string]any{"entity_id": "area:office", "include_hidden": true, "include_diagnostic": true},
		})
		if err != nil {
			t.Fatalf("area target with salience flags: %v", err)
		}
		if !entities[0].IncludeHidden || !entities[0].IncludeDiagnostic {
			t.Errorf("flags not carried: %+v", entities[0])
		}
	})
	t.Run("refused on a concrete entity", func(t *testing.T) {
		_, err := parseEntityList("entities", []any{
			map[string]any{"entity_id": "sensor.office_temperature", "include_hidden": true},
		})
		if err == nil {
			t.Fatal("include_hidden on a concrete entity should refuse")
		}
		if !strings.Contains(err.Error(), "always watched") {
			t.Errorf("error should teach the boundary, got: %v", err)
		}
	})
	t.Run("flags land on the spec subscription", func(t *testing.T) {
		spec, _ := dryRunSpec(t, map[string]any{
			"name": "office_watch", "intent": "Watch the office.",
			"operation": "service", "sleep_min": "10m", "sleep_max": "30m",
			"entities": []any{map[string]any{"entity_id": "area:office", "include_diagnostic": true}},
		})
		if len(spec.Subscriptions) != 1 || !spec.Subscriptions[0].IncludeDiagnostic {
			t.Errorf("subscription = %+v, want include_diagnostic carried", spec.Subscriptions)
		}
	})
}
