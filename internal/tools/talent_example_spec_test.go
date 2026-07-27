package tools

import (
	"encoding/json"
	"os"
	"reflect"
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
	// The example teaches both kinds of facet, so it has to carry both:
	// every reading projection, and at least one rendering target whose
	// slots the publish sample fills.
	var reading []looppkg.OutputFacet
	var targets []string
	for _, facet := range faceted.Facets {
		if facet.IsTarget() {
			targets = append(targets, facet.Target)
			continue
		}
		reading = append(reading, facet.Name)
	}
	wantReading := []looppkg.OutputFacet{looppkg.OutputFacetStatusLine, looppkg.OutputFacetTeaser, looppkg.OutputFacetDigest}
	if !reflect.DeepEqual(reading, wantReading) {
		t.Errorf("reading projections = %v, want %v", reading, wantReading)
	}
	if len(targets) == 0 {
		t.Fatal("the example declares no target facet, but its prose teaches one")
	}

	assertExamplePublishSampleIsValid(t, node[start:], faceted)
	if got := faceted.ToolName(); !strings.HasPrefix(got, "publish_output_") {
		t.Errorf("generated tool = %q, want publish_output_* — the example claims it swaps", got)
	}

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

// assertExamplePublishSampleIsValid runs the example's publish payload
// through the same validation a real call meets.
//
// The spec and the sample are taught together, so a sample that would be
// rejected teaches a call that fails — and the target slots are exactly
// where that is easy to get wrong, because their budgets live in the
// registry rather than in this file.
func assertExamplePublishSampleIsValid(t *testing.T, section string, output looppkg.OutputSpec) {
	t.Helper()

	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(section, -1)
	if len(blocks) < 2 {
		t.Fatalf("expected a spec block and a publish block, found %d", len(blocks))
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(blocks[1][1]), &args); err != nil {
		t.Fatalf("publish sample does not parse: %v", err)
	}

	// Every declared field has to be present: a publish carries the whole
	// payload, so a sample missing one would be rejected in practice.
	for _, field := range output.FacetFields() {
		if _, ok := args[field.Key]; !ok {
			t.Errorf("publish sample is missing the %q argument", field.Key)
		}
	}
	for key := range args {
		if key == "notes" {
			continue
		}
		if _, ok := facetFieldByKey(output, key); !ok {
			t.Errorf("publish sample passes %q, which this output does not declare", key)
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

func facetFieldByKey(output looppkg.OutputSpec, key string) (looppkg.FacetField, bool) {
	for _, field := range output.FacetFields() {
		if field.Key == key {
			return field, true
		}
	}
	return looppkg.FacetField{}, false
}
