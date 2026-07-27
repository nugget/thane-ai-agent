package tools

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTalentExampleSpecValidates decodes the loop spec published in the
// tiered curate example and runs it through the same decode-and-validate
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
	start := strings.Index(node, "# Curate: Dashboard (tiered publish)")
	if start < 0 {
		t.Fatal("tiered curate example not found")
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
		t.Fatalf("outputs = %d, want 2 (tiered document + working notes)", len(decoded.Outputs))
	}
	tiered := decoded.Outputs[0]
	if len(tiered.Tiers) != 3 {
		t.Errorf("tiers = %v, want three projections", tiered.Tiers)
	}
	if got := tiered.ToolName(); !strings.HasPrefix(got, "publish_output_") {
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
