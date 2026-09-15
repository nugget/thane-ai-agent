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
				for _, want := range []string{
					action + " cannot", tt.ref, tt.wantOwner + " owns that document", "no change was made", "report that to the operator",
					tt.wantOwner + " no longer reaches because its subject is gone",
				} {
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

// TestStoreMoveAndCopyRefuseToReplaceDomainOwnedDocuments pins the
// destination side of doc_move and doc_copy in a root with a write
// validator. Neither overwrites a document a narrower owner keeps there,
// whatever the source, and neither carries in a document stamped with
// such an owner, because the validator checks content alone and would
// accept a copy saved before the owner's last write. Ordinary roots and
// unowned content keep the generic behavior, and copying an owned
// document out stays allowed.
func TestStoreMoveAndCopyRefuseToReplaceDomainOwnedDocuments(t *testing.T) {
	const owner = "contact_dossier_write"
	tests := []struct {
		name string
		// seed maps each existing document's ref to its managed_by; ""
		// leaves it unmanaged. Every seeded document's title is its ref.
		seed      map[string]string
		src, dst  string
		overwrite bool
		copyOnly  bool
		// wantOwner names the tool the refusal must name; empty means the
		// move or copy lands.
		wantOwner   string
		wantRefusal ManagedLifecycleRefusal
	}{
		{
			name: "an owned copy onto the owned document", seed: map[string]string{"kb:snap.md": owner, "contacts:alice.md": owner},
			src: "kb:snap.md", dst: "contacts:alice.md", overwrite: true, wantOwner: owner, wantRefusal: RefusedOverwrite,
		},
		{
			name: "the owned document without overwrite", seed: map[string]string{"kb:snap.md": owner, "contacts:alice.md": owner},
			src: "kb:snap.md", dst: "contacts:alice.md", wantOwner: owner, wantRefusal: RefusedOverwrite,
		},
		{
			name: "unowned content onto the owned document", seed: map[string]string{"kb:notes.md": "", "contacts:alice.md": owner},
			src: "kb:notes.md", dst: "contacts:alice.md", overwrite: true, wantOwner: owner, wantRefusal: RefusedOverwrite,
		},
		{
			name: "owned content into a new path in the validated root", seed: map[string]string{"kb:snap.md": owner},
			src: "kb:snap.md", dst: "contacts:bob.md", wantOwner: owner, wantRefusal: RefusedArrival,
		},
		{
			name: "owned content onto a doc_write document in the validated root", seed: map[string]string{"kb:snap.md": owner, "contacts:carol.md": DocumentWriteToolName},
			src: "kb:snap.md", dst: "contacts:carol.md", overwrite: true, wantOwner: owner, wantRefusal: RefusedArrival,
		},
		{
			name: "unowned content onto a doc_write document in the validated root", seed: map[string]string{"kb:notes.md": "", "contacts:carol.md": DocumentWriteToolName},
			src: "kb:notes.md", dst: "contacts:carol.md", overwrite: true,
		},
		{
			name: "owned content onto an owned document in an ordinary root", seed: map[string]string{"kb:snap.md": owner, "kb:outputs/dave.md": "publish_output_digest"},
			src: "kb:snap.md", dst: "kb:outputs/dave.md", overwrite: true,
		},
		{
			name: "the owned document copied out to an ordinary root", seed: map[string]string{"contacts:alice.md": owner},
			src: "contacts:alice.md", dst: "kb:snap.md", copyOnly: true,
		},
	}

	for _, tt := range tests {
		for _, action := range []string{"doc_move", "doc_copy"} {
			if tt.copyOnly && action != "doc_copy" {
				continue
			}
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				store := newLifecycleStore(t)
				for ref, managedBy := range tt.seed {
					args := WriteArgs{Ref: ref, Title: ref, Body: stringPtr("Body.")}
					if managedBy != "" {
						args.Frontmatter = map[string][]string{documentfacets.ManagedByKey: {managedBy}}
					}
					if _, err := store.Write(ctx, args); err != nil {
						t.Fatalf("Write %s: %v", ref, err)
					}
				}

				var err error
				switch action {
				case "doc_move":
					_, err = store.Move(ctx, MoveArgs{Ref: tt.src, DestinationRef: tt.dst, Overwrite: tt.overwrite})
				case "doc_copy":
					_, err = store.Copy(ctx, CopyArgs{Ref: tt.src, DestinationRef: tt.dst, Overwrite: tt.overwrite})
				}

				if tt.wantOwner == "" {
					if err != nil {
						t.Fatalf("%s error = %v, want it to land", action, err)
					}
					assertLifecycleTitle(t, store, tt.dst, tt.src)
					if action == "doc_move" {
						if _, readErr := store.Read(ctx, tt.src); !IsNotFound(readErr) {
							t.Errorf("source after doc_move: error = %v, want not found", readErr)
						}
					} else {
						assertLifecycleTitle(t, store, tt.src, tt.src)
					}
					return
				}

				var refusal *ManagedDocumentLifecycleError
				if !errors.As(err, &refusal) || refusal.WriteTool != tt.wantOwner || refusal.Refusal != tt.wantRefusal {
					t.Fatalf("%s error = %v, want refusal %d naming %s", action, err, tt.wantRefusal, tt.wantOwner)
				}
				for _, want := range []string{action + " cannot", tt.src, tt.dst, tt.wantOwner, "no change was made"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal lacks %q: %v", want, err)
					}
				}
				// Refused by owner first, so the model is never sent to
				// retry a refused overwrite with overwrite=true.
				if strings.Contains(err.Error(), "overwrite=true") {
					t.Errorf("refusal points at overwrite=true: %v", err)
				}
				assertLifecycleTitle(t, store, tt.src, tt.src)
				if _, seeded := tt.seed[tt.dst]; seeded {
					assertLifecycleTitle(t, store, tt.dst, tt.dst)
				} else if _, readErr := store.Read(ctx, tt.dst); !IsNotFound(readErr) {
					t.Errorf("refused %s wrote the destination: read error = %v", action, readErr)
				}
			})
		}
	}
}

// assertLifecycleTitle checks which seeded document now sits at ref, by
// the title it was seeded with.
func assertLifecycleTitle(t *testing.T, store *Store, ref, wantTitle string) {
	t.Helper()
	record, err := store.Read(context.Background(), ref)
	if err != nil {
		t.Fatalf("Read %s: %v", ref, err)
	}
	if record.Title != wantTitle {
		t.Errorf("%s holds %q, want %q", ref, record.Title, wantTitle)
	}
}
