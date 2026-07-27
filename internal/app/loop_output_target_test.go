package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

const testTargetID = "apple_watch.rectangular"

// targetedSpec declares a status line plus one rendering surface.
func targetedSpec() looppkg.Spec {
	return looppkg.Spec{
		Name:       "ranch_office",
		Enabled:    true,
		Task:       "Curate the office domain.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{
			{
				Name: "office status",
				Type: looppkg.OutputTypeMaintainedDocument,
				Ref:  "core:office.md",
				Facets: []looppkg.FacetSpec{
					{Name: looppkg.OutputFacetStatusLine},
					{Target: testTargetID},
				},
			},
		},
	}
}

func targetPublishTool(t *testing.T, app *App) looppkg.RuntimeTool {
	t.Helper()
	hydrated, err := app.hydrateLoopOutputs(targetedSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	return findRuntimeTool(t, hydrated, "publish_output_office_status")
}

// TestTargetFacetArgumentIsTheSlotObject is what declaring a target buys
// over a bare json facet: the model is offered the surface's actual
// slots, typed and bounded, instead of a string it has to encode by hand.
func TestTargetFacetArgumentIsTheSlotObject(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	publish := targetPublishTool(t, &App{documentStore: store})

	properties, _ := publish.Parameters["properties"].(map[string]any)
	arg, ok := properties["apple_watch_rectangular"].(map[string]any)
	if !ok {
		t.Fatalf("publish tool has no apple_watch_rectangular argument; properties = %v", properties)
	}
	if arg["type"] != "object" {
		t.Errorf("target argument type = %v, want object", arg["type"])
	}
	slots, _ := arg["properties"].(map[string]any)
	for _, name := range []string{"value", "title", "subtitle", "fraction", "gauge_color"} {
		if _, ok := slots[name]; !ok {
			t.Errorf("target argument is missing the %q slot", name)
		}
	}
	// The budget has to reach the model, or it learns the limit by being
	// rejected.
	value, _ := slots["value"].(map[string]any)
	if value["maxLength"] != 12 {
		t.Errorf("value slot maxLength = %v, want the registry's 12", value["maxLength"])
	}
	if !strings.Contains(arg["description"].(string), "slots you omit are cleared") {
		t.Errorf("target argument description does not say a publish replaces the whole set: %v", arg["description"])
	}
}

func TestPublishWritesCanonicalSlotsIntoTheDocument(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	publish := targetPublishTool(t, &App{documentStore: store})

	if _, err := publish.Handler(context.Background(), map[string]any{
		"status_line": "Printer online; 2 packages waiting.",
		"full":        "# Office\n\nTwo packages.",
		"apple_watch_rectangular": map[string]any{
			"value":       "  2 pkg  ",
			"title":       "Office",
			"fraction":    0.5,
			"gauge_color": "3fb950",
		},
	}); err != nil {
		t.Fatalf("publish handler: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(coreDir, "office.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	written := string(raw)
	if !strings.Contains(written, "## Apple Watch rectangular complication") {
		t.Fatalf("document is missing the target section:\n%s", written)
	}

	payload := targetedSpec().Outputs[0].ParseFacetDocument(mustBody(t, store, "core:office.md"))
	var slots map[string]any
	if err := json.Unmarshal([]byte(payload.Targets[testTargetID]), &slots); err != nil {
		t.Fatalf("stored slots are not JSON: %v\n%s", err, payload.Targets[testTargetID])
	}
	// Normalization is what the store keeps: trimmed text, canonical hex.
	if slots["value"] != "2 pkg" {
		t.Errorf("stored value = %q, want the trimmed form", slots["value"])
	}
	if slots["gauge_color"] != "#3FB950" {
		t.Errorf("stored gauge_color = %q, want the canonical #RRGGBB form", slots["gauge_color"])
	}
	if slots["fraction"] != 0.5 {
		t.Errorf("stored fraction = %v, want 0.5", slots["fraction"])
	}
}

func TestPublishRejectsSlotValuesTheSurfaceCannotRender(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	publish := targetPublishTool(t, &App{documentStore: store})

	_, err := publish.Handler(context.Background(), map[string]any{
		"status_line": "All clear.",
		"full":        "Body.",
		"apple_watch_rectangular": map[string]any{
			"value": "a headline far too long for the slot",
		},
	})
	if err == nil {
		t.Fatal("publish handler error = nil for an over-budget slot, want an error")
	}
	for _, want := range []string{"apple_watch_rectangular", "value", "at most 12"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}

	// Nothing was written: a rejected publish must not leave a document
	// holding half a payload.
	if _, err := os.Stat(filepath.Join(coreDir, "office.md")); !os.IsNotExist(err) {
		t.Errorf("a rejected publish wrote the document anyway (stat err = %v)", err)
	}
}

// TestPublishAcceptsAStringEncodedSlotObject covers the model families
// that send a nested object as its JSON text rather than as an object.
func TestPublishAcceptsAStringEncodedSlotObject(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	publish := targetPublishTool(t, &App{documentStore: store})

	if _, err := publish.Handler(context.Background(), map[string]any{
		"status_line":             "All clear.",
		"full":                    "Body.",
		"apple_watch_rectangular": `{"value":"2 pkg","title":"Office"}`,
	}); err != nil {
		t.Fatalf("publish handler: %v", err)
	}

	payload := targetedSpec().Outputs[0].ParseFacetDocument(mustBody(t, store, "core:office.md"))
	if !strings.Contains(payload.Targets[testTargetID], `"value": "2 pkg"`) {
		t.Fatalf("string-encoded slots did not land: %q", payload.Targets[testTargetID])
	}
}

func mustBody(t *testing.T, store *documents.Store, ref string) string {
	t.Helper()
	doc, err := store.Read(context.Background(), ref)
	if err != nil {
		t.Fatalf("Read(%s): %v", ref, err)
	}
	return doc.Body
}
