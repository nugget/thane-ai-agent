package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// digestFacetedSpec is facetedSpec with a digest declared, so the publish
// tool renders every compact projection a faceted writer can carry.
func digestFacetedSpec() looppkg.Spec {
	spec := facetedSpec()
	spec.Outputs[0].Facets = append(spec.Outputs[0].Facets, looppkg.FacetSpec{Name: looppkg.OutputFacetDigest})
	return spec
}

// TestPublishToolTeachesValidationRefusalAndBoundedDigest pins what the
// generated publish surface says before the model ever hits a refusal:
// validation is one of the refusal kinds, it names the lever, notes do not
// land with a refused publish, and the digest parameter carries the shared
// bounded-current-state guidance.
func TestPublishToolTeachesValidationRefusalAndBoundedDigest(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store, documentTools: documents.NewTools(store)}
	hydrated, err := app.hydrateLoopOutputs(digestFacetedSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	for _, want := range []string{
		"says what happened — usually one of three things",
		"a projection failed validation",
		"an over-budget one carries its overage and whether rewording closes it or whole items must go",
		"fix them all in the next call",
		"A refused publish writes no notes either",
	} {
		if !strings.Contains(publish.Description, want) {
			t.Errorf("publish description lacks %q: %s", want, publish.Description)
		}
	}

	digest, ok := documentfacets.FieldByKey(string(documentfacets.Digest))
	if !ok {
		t.Fatal("digest field not found")
	}
	properties, _ := publish.Parameters["properties"].(map[string]any)
	property, _ := properties["digest"].(map[string]any)
	if description, _ := property["description"].(string); !strings.Contains(description, digest.Guidance) {
		t.Errorf("publish digest description = %q, want the shared digest guidance", description)
	}
}

// TestPublishToolValidationRefusalWritesNeitherHalf: a publish refused for
// an over-budget projection names the overage and the lever, and neither
// the document nor the working notes passed alongside it land.
func TestPublishToolValidationRefusalWritesNeitherHalf(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store, documentTools: documents.NewTools(store)}
	hydrated, err := app.hydrateLoopOutputs(facetedSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	const note = "Theory: the printer jams on Mondays."
	_, err = publish.Handler(context.Background(), map[string]any{
		"status_line": strings.Repeat("x", 200),
		"teaser":      "Fine.",
		"full":        "Fine.",
		"notes":       note,
	})
	if err == nil {
		t.Fatal("publish handler error = nil for an over-budget status line")
	}
	for _, want := range []string{"nothing was written", "correct every listed field", "status_line is 200 characters", "(80 over)", "remove whole items"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("publish error = %q, want it to contain %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(coreDir, "office.md")); !os.IsNotExist(statErr) {
		t.Fatal("a rejected publish must not write the document")
	}
	notes, readErr := os.ReadFile(filepath.Join(coreDir, "office-notes.md"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read notes: %v", readErr)
	}
	if strings.Contains(string(notes), note) {
		t.Fatalf("a rejected publish wrote its working notes: %q", notes)
	}
}
