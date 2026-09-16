package contacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// newDossierLifecycleStore opens a document store whose contacts root
// runs the real dossier validator, beside an ordinary "kb" root, and
// wires tools to write dossiers through it. It returns the contacts
// root's directory so a test can read a dossier's bytes.
func newDossierLifecycleStore(t *testing.T, tools *Tools) (*documents.Store, *documents.Tools, string) {
	t.Helper()
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
	return store, docTools, roots[DossierRootName]
}

func writeLifecycleDossier(t *testing.T, tools *Tools, contactID, full string) {
	t.Helper()
	if _, err := tools.WriteDossier(context.Background(), DossierWriteArgs{
		ContactID:  contactID,
		StatusLine: "Current.",
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       full,
	}); err != nil {
		t.Fatalf("WriteDossier() error = %v", err)
	}
}

// TestGenericDocumentToolsRefuseAManagedDossier pins the lifecycle
// refusal against the real contacts-root validator: once
// contact_dossier_write owns a dossier, doc_delete and doc_move refuse
// it, name that tool, and leave it in place.
func TestGenericDocumentToolsRefuseAManagedDossier(t *testing.T) {
	ctx := context.Background()
	tools, contactID := newCitationTestTools(t)
	store, docTools, _ := newDossierLifecycleStore(t, tools)
	writeLifecycleDossier(t, tools, contactID, "Detail. — evidence: archive:session:"+citeUniqueID)
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

// TestGenericDocumentToolsCannotRollBackAManagedDossier pins the
// destination side against the real contacts-root validator. A copy of
// the dossier saved before contact_dossier_write's last write passes that
// validator, which checks content alone, so doc_move and doc_copy with
// overwrite must refuse it by owner; otherwise they would roll the
// dossier back around the owner's operator gate and read-before-write
// check.
func TestGenericDocumentToolsCannotRollBackAManagedDossier(t *testing.T) {
	for _, action := range []string{"doc_move", "doc_copy"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			tools, contactID := newCitationTestTools(t)
			store, docTools, contactsDir := newDossierLifecycleStore(t, tools)
			ref := DossierRef(uuid.MustParse(contactID))
			dossierPath := filepath.Join(contactsDir, strings.TrimPrefix(ref, DossierRootName+":"))
			const snapshot = "kb:snap.md"

			writeLifecycleDossier(t, tools, contactID, "VERSIONONE. — evidence: archive:session:"+citeUniqueID)
			if _, err := docTools.Copy(ctx, documents.CopyArgs{Ref: ref, DestinationRef: snapshot}); err != nil {
				t.Fatalf("copying the dossier out: %v", err)
			}
			writeLifecycleDossier(t, tools, contactID, "VERSIONTWO. — evidence: archive:session:"+citeUniqueID)

			var err error
			switch action {
			case "doc_move":
				_, err = docTools.Move(ctx, documents.MoveArgs{Ref: snapshot, DestinationRef: ref, Overwrite: true})
			case "doc_copy":
				_, err = docTools.Copy(ctx, documents.CopyArgs{Ref: snapshot, DestinationRef: ref, Overwrite: true})
			}

			var refusal *documents.ManagedDocumentLifecycleError
			if !errors.As(err, &refusal) || refusal.Refusal != documents.RefusedOverwrite {
				t.Fatalf("%s onto the dossier: error = %v, want an overwrite refusal", action, err)
			}
			for _, want := range []string{action + " cannot overwrite " + ref + " with " + snapshot, DossierWriteToolName + " owns that document", "no change was made", "use " + DossierWriteToolName} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal lacks %q: %v", want, err)
				}
			}
			raw, readErr := os.ReadFile(dossierPath)
			if readErr != nil {
				t.Fatalf("reading the dossier: %v", readErr)
			}
			if !strings.Contains(string(raw), "VERSIONTWO") || strings.Contains(string(raw), "VERSIONONE") {
				t.Errorf("dossier after refused %s:\n%s\nwant the owner's last write", action, raw)
			}
			if _, readErr := store.Read(ctx, snapshot); readErr != nil {
				t.Errorf("snapshot after refused %s: read error = %v", action, readErr)
			}
		})
	}
}
