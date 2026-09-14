package tools

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

func contactDossierWriteToolForSchema(t *testing.T) *Tool {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments(nil, (&contactDossierWriterRecorder{}).Write)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)
	return registry.Get("contact_dossier_write")
}

// TestFacetedWriterSchemasTeachBoundedDigestAndRefusal checks the rendered
// schemas, not the contract: the digest guidance only teaches if every
// faceted writer's digest parameter actually carries it, and a writer that
// describes its failures must say a refusal writes nothing and how to size
// the repair.
func TestFacetedWriterSchemasTeachBoundedDigestAndRefusal(t *testing.T) {
	t.Parallel()

	digest, ok := documentfacets.FieldByKey(string(documentfacets.Digest))
	if !ok {
		t.Fatal("digest field not found")
	}
	tests := []struct {
		name             string
		tool             func(*testing.T) *Tool
		descriptionWants []string
	}{
		{
			name: "doc_write",
			tool: func(t *testing.T) *Tool {
				registry, _ := newTestDocumentRegistry(t)
				return registry.Get("doc_write")
			},
		},
		{
			name: "contact_dossier_write",
			tool: contactDossierWriteToolForSchema,
			descriptionWants: []string{
				"a rejected write stores nothing",
				"lists every violation in one error",
				"its overage and whether rewording closes it or whole items must go",
				"fix them all in the next call",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tool := tt.tool(t)
			if tool == nil {
				t.Fatalf("%s not registered", tt.name)
			}
			properties, _ := tool.Parameters["properties"].(map[string]any)
			property, _ := properties["digest"].(map[string]any)
			description, _ := property["description"].(string)
			if !strings.Contains(description, digest.Guidance) {
				t.Errorf("%s digest description = %q, want the shared digest guidance", tt.name, description)
			}
			for _, want := range tt.descriptionWants {
				if !strings.Contains(tool.Description, want) {
					t.Errorf("%s description lacks %q: %s", tt.name, want, tool.Description)
				}
			}
			if strings.Contains(tool.Description, "retry once") {
				t.Errorf("%s description caps retries: %s", tt.name, tool.Description)
			}
		})
	}
}
