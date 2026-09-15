package contacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// TestGenericDocumentToolsRefuseAManagedDossier pins the lifecycle
// refusal against the real contacts-root validator: once
// contact_dossier_write owns a dossier, doc_delete and doc_move refuse
// it, name that tool, and leave it in place.
func TestGenericDocumentToolsRefuseAManagedDossier(t *testing.T) {
	ctx := context.Background()
	tools, contactID := newCitationTestTools(t)

	base := t.TempDir()
	roots := map[string]string{DossierRootName: filepath.Join(base, DossierRootName), "kb": filepath.Join(base, "kb")}
	for _, dir := range roots {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := documents.NewStoreWithOptions(db, roots, nil, documents.StoreOptions{
		RootValidators: map[string]documents.RootWriteValidator{
			DossierRootName: NewDossierWriteValidator(func(id uuid.UUID) (string, error) {
				contact, err := tools.store.Get(id)
				if err != nil {
					return "", err
				}
				return contact.FormattedName, nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	docTools := documents.NewTools(store)
	tools.ConfigureDossierDocuments(docTools.Read, docTools.WriteFaceted)

	if _, err := tools.WriteDossier(ctx, DossierWriteArgs{
		ContactID:  contactID,
		StatusLine: "Current.",
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       "Detail. — evidence: archive:session:" + citeUniqueID,
	}); err != nil {
		t.Fatalf("WriteDossier() error = %v", err)
	}
	ref := DossierRef(uuid.MustParse(contactID))

	for _, tc := range []struct {
		action string
		call   func() (string, error)
	}{
		{action: "doc_delete", call: func() (string, error) {
			return docTools.Delete(ctx, documents.DeleteArgs{Ref: ref})
		}},
		{action: "doc_move", call: func() (string, error) {
			return docTools.Move(ctx, documents.MoveArgs{Ref: ref, DestinationRef: "kb:people/dossier.md"})
		}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			result, err := tc.call()
			if err == nil {
				t.Fatalf("%s = %s, want a refusal", tc.action, result)
			}
			for _, want := range []string{tc.action + " cannot", ref, DossierWriteToolName + " owns that document", "no change was made"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal lacks %q: %v", want, err)
				}
			}
			if _, err := store.Read(ctx, ref); err != nil {
				t.Errorf("dossier after refused %s: read error = %v", tc.action, err)
			}
		})
	}
}
