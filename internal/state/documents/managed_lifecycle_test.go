package documents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// newLifecycleStore opens a store with a "contacts" root guarded by a
// write validator, the shape of a domain-owned root, and an ordinary
// "kb" root without one.
func newLifecycleStore(t *testing.T) *Store {
	t.Helper()
	base := t.TempDir()
	roots := map[string]string{"contacts": filepath.Join(base, "contacts"), "kb": filepath.Join(base, "kb")}
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
	store, err := NewStoreWithOptions(db, roots, nil, StoreOptions{
		RootValidators: map[string]RootWriteValidator{
			"contacts": func(DocumentWriteCandidate) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store
}

// TestStoreDeleteAndMoveRefuseDomainOwnedDocuments pins which documents
// doc_delete and doc_move leave to their owner: one stamped with an owner
// narrower than doc_write in a root with a write validator. Everything
// else keeps its generic lifecycle.
func TestStoreDeleteAndMoveRefuseDomainOwnedDocuments(t *testing.T) {
	tests := []struct {
		name      string
		ref       string
		managedBy string
		// wantOwner names the tool the refusal must name; empty means the
		// delete or move lands.
		wantOwner string
	}{
		{name: "domain-owned document in a validated root", ref: "contacts:alice.md", managedBy: "contact_dossier_write", wantOwner: "contact_dossier_write"},
		{name: "doc_write document in a validated root", ref: "contacts:bob.md", managedBy: DocumentWriteToolName},
		{name: "unmanaged document in a validated root", ref: "contacts:carol.md"},
		{name: "managed document in an ordinary root", ref: "kb:outputs/dave.md", managedBy: "publish_output_digest"},
		{name: "unmanaged document in an ordinary root", ref: "kb:notes/eve.md"},
	}
	const destination = "kb:archive/moved.md"

	for _, tt := range tests {
		for _, action := range []string{"doc_delete", "doc_move"} {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				store := newLifecycleStore(t)
				args := WriteArgs{Ref: tt.ref, Title: "Lifecycle", Body: stringPtr("Body.")}
				if tt.managedBy != "" {
					args.Frontmatter = map[string][]string{documentfacets.ManagedByKey: {tt.managedBy}}
				}
				if _, err := store.Write(ctx, args); err != nil {
					t.Fatalf("Write: %v", err)
				}

				var err error
				switch action {
				case "doc_delete":
					_, err = store.Delete(ctx, DeleteArgs{Ref: tt.ref})
				case "doc_move":
					_, err = store.Move(ctx, MoveArgs{Ref: tt.ref, DestinationRef: destination})
				}

				if tt.wantOwner == "" {
					if err != nil {
						t.Fatalf("%s error = %v, want it to land", action, err)
					}
					if _, readErr := store.Read(ctx, tt.ref); !IsNotFound(readErr) {
						t.Errorf("source after %s: error = %v, want not found", action, readErr)
					}
					return
				}

				var refusal *ManagedDocumentLifecycleError
				if !errors.As(err, &refusal) || refusal.WriteTool != tt.wantOwner {
					t.Fatalf("%s error = %v, want a lifecycle refusal naming %s", action, err, tt.wantOwner)
				}
				for _, want := range []string{action + " cannot", tt.ref, tt.wantOwner + " owns that document", "no change was made", "report that to the operator"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal lacks %q: %v", want, err)
					}
				}
				if _, readErr := store.Read(ctx, tt.ref); readErr != nil {
					t.Errorf("refused %s changed the source: read error = %v", action, readErr)
				}
				if _, readErr := store.Read(ctx, destination); !IsNotFound(readErr) {
					t.Errorf("refused %s wrote the destination: read error = %v", action, readErr)
				}
			})
		}
	}
}
