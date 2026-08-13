package tools

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestTalentExampleSpecValidates decodes the loop spec published in the
// faceted curate example and runs it through the same decode-and-validate
// path loop_definition_set uses.
//
// A worked example is what a model copies, so an example that would not
// validate is worse than no example: it teaches a call that fails. This
// one caught a missing task: on the first run.
//
// Deliberately targeted rather than sweeping every JSON block in the
// file — the others are thane_loop_create arguments and publish
// payloads, which are different shapes and would fail spec validation
// for reasons that are not defects.
func TestTalentExampleSpecValidates(t *testing.T) {
	raw, err := os.ReadFile("../../talents/loops-examples.md")
	if err != nil {
		t.Fatalf("read talent: %v", err)
	}
	node := string(raw)
	start := strings.Index(node, "# Curate: Dashboard (faceted publish)")
	if start < 0 {
		t.Fatal("faceted curate example not found")
	}
	block := regexp.MustCompile("(?s)```json\n(.*?)```").FindStringSubmatch(node[start:])
	if block == nil {
		t.Fatal("no json block in the example")
	}

	var spec map[string]any
	if err := json.Unmarshal([]byte(block[1]), &spec); err != nil {
		t.Fatalf("example json does not parse: %v", err)
	}
	decoded, err := decodeLoopSpecArg(map[string]any{"spec": spec}, "spec")
	if err != nil {
		t.Fatalf("example spec does not decode: %v", err)
	}
	if err := decoded.ValidatePersistable(); err != nil {
		t.Fatalf("example spec does not validate: %v", err)
	}

	if len(decoded.Outputs) != 2 {
		t.Fatalf("outputs = %d, want 2 (faceted document + working notes)", len(decoded.Outputs))
	}
	faceted := decoded.Outputs[0]
	if len(faceted.Facets) != 3 {
		t.Errorf("facets = %v, want three projections", faceted.Facets)
	}
	if got := faceted.ToolName(); !strings.HasPrefix(got, "publish_output_") {
		t.Errorf("generated tool = %q, want publish_output_* — the example claims it swaps", got)
	}

	assertExamplePublishSampleIsValid(t, node[start:], faceted, decoded.Outputs)

	// doc_read is gated behind the documents tag, and the example's own
	// prose tells the loop to read its document when the injected body is
	// truncated. A spec can validate perfectly and still describe a task
	// its tag set cannot perform, so this is asserted rather than trusted.
	var hasDocuments bool
	for _, tag := range decoded.Tags {
		if tag == "documents" {
			hasDocuments = true
		}
	}
	if !hasDocuments {
		t.Error("a loop that owns documents needs the documents tag to reach doc_read")
	}
}

// TestSupervisorExampleSpecIsCanonical decodes the battery_watch
// supervisor example through the same path loop_definition_set uses
// and pins its authoring shape: supervisor routing lives in
// supervisor_profile, not the legacy top-level compat keys
// (supervisor_quality_floor, supervisor_context), and capability tags
// are spec-level tags, not a profile field json.Unmarshal drops
// silently. The legacy keys still decode — with a one-shot WARN — so
// validation alone cannot catch a regression to the retired shape;
// the raw text is asserted alongside the decoded spec.
func TestSupervisorExampleSpecIsCanonical(t *testing.T) {
	raw, err := os.ReadFile("../../talents/loops-examples.md")
	if err != nil {
		t.Fatalf("read talent: %v", err)
	}
	node := string(raw)
	start := strings.Index(node, "## Supervisor turns on service loops")
	if start < 0 {
		t.Fatal("supervisor example not found")
	}
	block := regexp.MustCompile("(?s)```json\n(.*?)```").FindStringSubmatch(node[start:])
	if block == nil {
		t.Fatal("no json block in the supervisor example")
	}

	for _, retired := range []string{"supervisor_quality_floor", "supervisor_context", "initial_tags"} {
		if strings.Contains(block[1], retired) {
			t.Errorf("supervisor example teaches retired or misplaced key %q", retired)
		}
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(block[1]), &payload); err != nil {
		t.Fatalf("example json does not parse: %v", err)
	}
	decoded, err := decodeLoopSpecArg(payload, "spec")
	if err != nil {
		t.Fatalf("example spec does not decode: %v", err)
	}
	if err := decoded.ValidatePersistable(); err != nil {
		t.Fatalf("example spec does not validate: %v", err)
	}

	if decoded.SupervisorProfile == nil {
		t.Fatal("example spec decodes without a supervisor_profile")
	}
	if decoded.SupervisorProfile.QualityFloor != 9 {
		t.Errorf("supervisor_profile.quality_floor = %d, want 9", decoded.SupervisorProfile.QualityFloor)
	}
	if decoded.SupervisorProfile.Instructions == "" {
		t.Error("supervisor_profile.instructions is empty — the supervisor turn has no step-back guidance")
	}
	if decoded.Profile.QualityFloor != 4 {
		t.Errorf("profile.quality_floor = %d, want 4", decoded.Profile.QualityFloor)
	}
	wantTags := map[string]bool{"home": false, "knowledge": false, "documents": false}
	for _, tag := range decoded.Tags {
		if _, ok := wantTags[tag]; ok {
			wantTags[tag] = true
		}
	}
	for tag, seen := range wantTags {
		if !seen {
			t.Errorf("spec-level tags missing %q — the loop launches without its tool surface", tag)
		}
	}
}

// assertExamplePublishSampleIsValid runs the example's publish payload
// through the same validation a real call meets.
//
// The spec and the sample are taught together, and the sample is what a
// model copies verbatim, so a sample that would be rejected teaches a
// call that fails. Argument names are the sharp edge: this file taught a
// "note" argument the tool spells "notes" for long enough to ship, and
// the failure is silent — the publish succeeds and the loop's thinking
// is dropped on the floor.
func assertExamplePublishSampleIsValid(t *testing.T, section string, output looppkg.OutputSpec, outputs []looppkg.OutputSpec) {
	t.Helper()

	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(section, -1)
	if len(blocks) < 2 {
		t.Fatalf("expected a spec block and a publish block, found %d", len(blocks))
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(blocks[1][1]), &args); err != nil {
		t.Fatalf("publish sample does not parse: %v", err)
	}

	// Every declared projection has to be present: a publish carries the
	// whole payload, so a sample missing one would be rejected in practice.
	valid := map[string]bool{}
	for _, field := range output.FacetFields() {
		valid[field.Key] = true
		if _, ok := args[field.Key]; !ok {
			t.Errorf("publish sample is missing the %q argument", field.Key)
		}
	}
	// The notes argument exists only when the loop declared somewhere for
	// them to land, which this example does — so the sample must show it,
	// under the name the tool actually reads.
	var hasNotes bool
	for _, out := range outputs {
		if out.Type == looppkg.OutputTypeWorkingNotes {
			hasNotes = true
		}
	}
	if hasNotes {
		valid["notes"] = true
		if _, ok := args["notes"]; !ok {
			t.Error("the example declares working_notes but its publish sample never passes notes; the argument is how a publish and its thinking stay one call")
		}
	}
	for key := range args {
		if !valid[key] {
			t.Errorf("publish sample passes %q, which this loop's publish tool does not accept", key)
		}
	}

	payload, err := output.FacetPayloadFromArgs(args)
	if err != nil {
		t.Fatalf("publish sample does not decode: %v", err)
	}
	if err := output.ValidateFacetPayload(payload); err != nil {
		t.Fatalf("publish sample would be rejected: %v", err)
	}
}
