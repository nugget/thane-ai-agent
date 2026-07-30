package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// TestThaneLoopCreateDryRunWritesNothing pins the property the whole
// mode exists for. A dry run answers "what would you build" — if it
// scaffolds the document or commits the definition, it has answered by
// building it, and the caller cannot inspect a derived spec without
// accepting it.
func TestThaneLoopCreateDryRunWritesNothing(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	args := map[string]any{
		"name":      "dry_run_probe",
		"intent":    "Watch something and keep a document current.",
		"operation": "service",
		"sleep_min": "10m",
		"sleep_max": "30m",
		"output": map[string]any{
			"document": "kb:dashboards/dry-run-probe.md",
		},
		"dry_run": true,
	}
	out, err := rig.tool.Handler(ctx, args)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", result["status"])
	}
	if result["spec"] == nil {
		t.Fatal("dry run must return the spec it would have committed")
	}
	if _, ok := result["loop_id"]; ok {
		t.Error("dry run reported a loop_id, so something launched")
	}

	// Nothing committed.
	if snap := rig.defRegistry.Snapshot(); snap != nil {
		for _, def := range snap.Definitions {
			if def.Name == "dry_run_probe" {
				t.Error("dry run committed a loop definition")
			}
		}
	}
	// Nothing scaffolded — the derived notes document included.
	if _, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/dry-run-probe.md"}); err == nil {
		t.Error("dry run scaffolded the output document")
	}
	if _, err := rig.docTools.Read(ctx, documents.RefArgs{Ref: "kb:dashboards/dry-run-probe-notes.md"}); err == nil {
		t.Error("dry run scaffolded the working-notes document")
	}

	// The returned spec must be the real thing, not a summary: same shape
	// loop_definition_set would accept, so a caller can adjust one field
	// and hand it straight over.
	specJSON, err := json.Marshal(result["spec"])
	if err != nil {
		t.Fatalf("re-marshal spec: %v", err)
	}
	var specArg map[string]any
	if err := json.Unmarshal(specJSON, &specArg); err != nil {
		t.Fatalf("spec is not an object: %v", err)
	}
	decoded, err := decodeLoopSpecArg(map[string]any{"spec": specArg}, "spec")
	if err != nil {
		t.Fatalf("dry-run spec does not decode through loop_definition_set's path: %v", err)
	}
	if err := decoded.ValidatePersistable(); err != nil {
		t.Fatalf("dry-run spec does not validate: %v", err)
	}
	if decoded.Name != "dry_run_probe" || len(decoded.Outputs) != 2 {
		t.Errorf("round-tripped spec lost detail: name=%q outputs=%d (want the document plus its notes)", decoded.Name, len(decoded.Outputs))
	}
	if !strings.HasPrefix(decoded.Outputs[0].ToolName(), "replace_output_") {
		t.Errorf("output tool = %q, want replace_output_* for maintain mode", decoded.Outputs[0].ToolName())
	}
}

// TestThaneLoopCreateDryRunContainer covers the operation most likely to
// be forgotten. A flag honoured by two of three operations and silently
// ignored by the third would create the very thing the caller asked to
// preview.
func TestThaneLoopCreateDryRunContainer(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()

	out, err := rig.tool.Handler(ctx, map[string]any{
		"name":      "dry_run_container",
		"intent":    "Group the ranch watchers.",
		"operation": "container",
		"dry_run":   true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["status"] != "dry_run" || result["spec"] == nil {
		t.Fatalf("container dry run = %v, want a dry_run status and a spec", result)
	}
	if snap := rig.defRegistry.Snapshot(); snap != nil {
		for _, def := range snap.Definitions {
			if def.Name == "dry_run_container" {
				t.Error("container dry run committed a definition")
			}
		}
	}
}
