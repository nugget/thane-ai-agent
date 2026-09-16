package documents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// contendedMutationGrace bounds how long these tests wait for a doc_delete,
// doc_move, or doc_copy that must not finish while another write holds the
// same root. It is a negative control, not a sleep that hopes to hit a
// window: the interleaving itself is forced by the paused writer below, and
// every call under test is launched only once that writer is demonstrably
// inside the root. A longer grace can only slow a correct implementation
// down; a guard that does not couple its check to its mutation finishes
// immediately and fails on the first select, whatever the grace is.
const contendedMutationGrace = 250 * time.Millisecond

// lifecycleHookWriter is a RootWriter that materializes documents itself
// and offers every mutation to a hook first, so a test can hold one write
// open at the instant another tool reads the same root.
type lifecycleHookWriter struct {
	root string
	mu   sync.Mutex
	hook func(op, filename string)
}

func (w *lifecycleHookWriter) setHook(hook func(op, filename string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hook = hook
}

func (w *lifecycleHookWriter) offer(op, filename string) {
	w.mu.Lock()
	hook := w.hook
	w.mu.Unlock()
	if hook != nil {
		hook(op, filename)
	}
}

func (w *lifecycleHookWriter) Write(_ context.Context, filename, content, _ string) error {
	w.offer("write", filename)
	path := filepath.Join(w.root, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (w *lifecycleHookWriter) WriteIfRevision(ctx context.Context, filename, content, message, _ string) (string, error) {
	return "", w.Write(ctx, filename, content, message)
}

func (w *lifecycleHookWriter) Delete(_ context.Context, filename, _ string) error {
	w.offer("delete", filename)
	return os.Remove(filepath.Join(w.root, filename))
}

// pauseFirstWrite holds the first write of filename open: it announces the
// write on started and returns only once release is closed. Later writes of
// the same file — a move landing its content there, say — pass straight
// through, so only the intended interleaving is staged.
func pauseFirstWrite(filename string, started chan<- struct{}, release <-chan struct{}) func(string, string) {
	var paused atomic.Bool
	return func(op, name string) {
		if op != "write" || name != filename || paused.Swap(true) {
			return
		}
		close(started)
		<-release
	}
}

// newLifecycleRaceStore opens the lifecycle store of
// [newLifecycleStore] with the contacts root's writes routed through a hook
// writer, the seam these tests pause a mutation in.
func newLifecycleRaceStore(t *testing.T) (*Store, *lifecycleHookWriter) {
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
	writer := &lifecycleHookWriter{root: roots["contacts"]}
	store, err := NewStoreWithOptions(db, roots, nil, StoreOptions{
		RootWriters: map[string]RootWriter{"contacts": writer},
		RootValidators: map[string]RootWriteValidator{
			"contacts": func(DocumentWriteCandidate) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store, writer
}

// seedLifecycleDocument writes ref with title as its title, stamped with
// managedBy when that is not empty.
func seedLifecycleDocument(t *testing.T, store *Store, ref, title, managedBy string) {
	t.Helper()
	args := WriteArgs{Ref: ref, Title: title, Body: stringPtr("Body of " + title + ".")}
	if managedBy != "" {
		args.Frontmatter = map[string][]string{documentfacets.ManagedByKey: {managedBy}}
	}
	if _, err := store.Write(context.Background(), args); err != nil {
		t.Fatalf("Write %s: %v", ref, err)
	}
}

// startPausedStampingWrite launches a write of ref that stops inside the
// contacts root and stays there until the returned release is closed. It
// returns once the write is in flight — the document on disk is still the
// version a guard reading it now would see, and the root is held.
func startPausedStampingWrite(t *testing.T, store *Store, writer *lifecycleHookWriter, ref, title, managedBy string) (release chan struct{}, done <-chan error) {
	t.Helper()
	_, relPath, err := parseRef(ref)
	if err != nil {
		t.Fatalf("parseRef %s: %v", ref, err)
	}
	started := make(chan struct{})
	release = make(chan struct{})
	writer.setHook(pauseFirstWrite(relPath, started, release))

	finished := make(chan error, 1)
	go func() {
		args := WriteArgs{Ref: ref, Title: title, Body: stringPtr("Body of " + title + ".")}
		if managedBy != "" {
			args.Frontmatter = map[string][]string{documentfacets.ManagedByKey: {managedBy}}
		}
		_, writeErr := store.Write(context.Background(), args)
		finished <- writeErr
	}()
	<-started
	return release, finished
}

// TestStoreLifecycleGuardSeesAWriteThatLandsAfterItsOwnershipRead drives the
// interleaving the ownership guard used to lose: doc_delete or doc_move
// reads a document that is not yet a dossier, and contact_dossier_write
// stamps it before the retirement lands. The guard's read and its mutation
// hold the same root, so the stamping write cannot get between them — the
// retirement either refuses the dossier or retires the version it actually
// read.
func TestStoreLifecycleGuardSeesAWriteThatLandsAfterItsOwnershipRead(t *testing.T) {
	const (
		source      = "contacts:alice.md"
		destination = "kb:archive/alice.md"
		owner       = "contact_dossier_write"
	)
	tests := []struct {
		name string
		// stamp is the managed_by the intervening write applies; empty
		// makes it an ordinary update, which stays retirable.
		stamp     string
		wantOwner string
	}{
		{name: "a dossier stamped after the ownership read", stamp: owner, wantOwner: owner},
		{name: "an ordinary update after the ownership read", stamp: ""},
	}

	for _, tt := range tests {
		for _, action := range []string{"doc_delete", "doc_move"} {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				store, writer := newLifecycleRaceStore(t)
				seedLifecycleDocument(t, store, source, "Alice", "")

				release, stampDone := startPausedStampingWrite(t, store, writer, source, "Alice dossier", tt.stamp)
				retired := make(chan error, 1)
				go func() {
					var err error
					switch action {
					case "doc_delete":
						_, err = store.Delete(ctx, DeleteArgs{Ref: source})
					case "doc_move":
						_, err = store.Move(ctx, MoveArgs{Ref: source, DestinationRef: destination})
					}
					retired <- err
				}()

				select {
				case err := <-retired:
					t.Fatalf("%s finished while a write held the contacts root (error = %v); its ownership check is not coupled to its mutation", action, err)
				case <-time.After(contendedMutationGrace):
				}
				close(release)
				if err := <-stampDone; err != nil {
					t.Fatalf("intervening write: %v", err)
				}
				err := <-retired

				if tt.wantOwner == "" {
					if err != nil {
						t.Fatalf("%s error = %v, want it to retire the updated document", action, err)
					}
					if _, readErr := store.Read(ctx, source); !IsNotFound(readErr) {
						t.Errorf("source after %s: error = %v, want not found", action, readErr)
					}
					if action == "doc_move" {
						// The move must carry the version that landed, not
						// the one its ownership read saw.
						assertLifecycleTitle(t, store, destination, "Alice dossier")
					}
					return
				}

				var refusal *ManagedDocumentLifecycleError
				if !errors.As(err, &refusal) || refusal.WriteTool != tt.wantOwner {
					t.Fatalf("%s error = %v, want a lifecycle refusal naming %s", action, err, tt.wantOwner)
				}
				record, readErr := store.Read(ctx, source)
				if readErr != nil {
					t.Fatalf("refused %s removed the dossier: %v", action, readErr)
				}
				if record.ManagedBy != tt.wantOwner {
					t.Errorf("dossier managed_by = %q, want %q", record.ManagedBy, tt.wantOwner)
				}
				if _, readErr := store.Read(ctx, destination); !IsNotFound(readErr) {
					t.Errorf("refused %s wrote the destination: error = %v", action, readErr)
				}
			})
		}
	}
}

// TestStoreTransferGuardSeesADestinationThatArrivesAfterItsRead drives the
// destination half of the same race: doc_move or doc_copy finds no dossier
// at the destination, and contact_dossier_write creates one before the
// transfer writes. The destination read and the write hold the same roots,
// so the transfer sees the dossier and refuses it rather than replacing it,
// while an ordinary document arriving there is still overwritten.
func TestStoreTransferGuardSeesADestinationThatArrivesAfterItsRead(t *testing.T) {
	const (
		source      = "kb:notes.md"
		destination = "contacts:alice.md"
		owner       = "contact_dossier_write"
	)
	tests := []struct {
		name string
		// stamp is the managed_by of the document that arrives at the
		// destination; empty makes it an ordinary document.
		stamp     string
		wantOwner string
	}{
		{name: "a dossier created after the destination read", stamp: owner, wantOwner: owner},
		{name: "an ordinary document created after the destination read", stamp: ""},
	}

	for _, tt := range tests {
		for _, action := range []string{"doc_move", "doc_copy"} {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				store, writer := newLifecycleRaceStore(t)
				seedLifecycleDocument(t, store, source, "Notes", "")

				release, arrivalDone := startPausedStampingWrite(t, store, writer, destination, "Alice dossier", tt.stamp)
				transferred := make(chan error, 1)
				go func() {
					var err error
					switch action {
					case "doc_move":
						_, err = store.Move(ctx, MoveArgs{Ref: source, DestinationRef: destination, Overwrite: true})
					case "doc_copy":
						_, err = store.Copy(ctx, CopyArgs{Ref: source, DestinationRef: destination, Overwrite: true})
					}
					transferred <- err
				}()

				select {
				case err := <-transferred:
					t.Fatalf("%s finished while a write held the contacts root (error = %v); its destination check is not coupled to its write", action, err)
				case <-time.After(contendedMutationGrace):
				}
				close(release)
				if err := <-arrivalDone; err != nil {
					t.Fatalf("arriving write: %v", err)
				}
				err := <-transferred

				if tt.wantOwner == "" {
					if err != nil {
						t.Fatalf("%s error = %v, want it to overwrite the ordinary document", action, err)
					}
					assertLifecycleTitle(t, store, destination, "Notes")
					return
				}

				var refusal *ManagedDocumentLifecycleError
				if !errors.As(err, &refusal) || refusal.WriteTool != tt.wantOwner || refusal.Refusal != RefusedOverwrite {
					t.Fatalf("%s error = %v, want an overwrite refusal naming %s", action, err, tt.wantOwner)
				}
				assertLifecycleTitle(t, store, destination, "Alice dossier")
				assertLifecycleTitle(t, store, source, "Notes")
			})
		}
	}
}

// TestStoreSectionTransferGuardSeesADestinationThatArrivesAfterItsRead
// drives the same destination race through doc_move_section and
// doc_copy_section, which reach the destination by a different route: they
// render it from the bytes they read — a whole new document when it was
// absent — and then replace the file. An owner arriving in that window must
// be refused rather than silently rewritten into a section transfer's
// output, and an ordinary document arriving there must still receive the
// section.
func TestStoreSectionTransferGuardSeesADestinationThatArrivesAfterItsRead(t *testing.T) {
	const (
		source      = "kb:notes.md"
		destination = "contacts:alice.md"
		section     = "Garden"
		owner       = "contact_dossier_write"
	)
	tests := []struct {
		name string
		// stamp is the managed_by of the document that arrives at the
		// destination; empty makes it an ordinary document.
		stamp     string
		wantOwner string
	}{
		{name: "a dossier created after the destination read", stamp: owner, wantOwner: owner},
		{name: "an ordinary document created after the destination read", stamp: ""},
	}

	for _, tt := range tests {
		for _, action := range []string{"doc_move_section", "doc_copy_section"} {
			t.Run(tt.name+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				store, writer := newLifecycleRaceStore(t)
				if _, err := store.Write(ctx, WriteArgs{
					Ref:   source,
					Title: "Notes",
					Body:  stringPtr("## " + section + "\n\nBob keeps bees.\n"),
				}); err != nil {
					t.Fatalf("Write %s: %v", source, err)
				}

				release, arrivalDone := startPausedStampingWrite(t, store, writer, destination, "Alice dossier", tt.stamp)
				transferred := make(chan error, 1)
				go func() {
					args := SectionTransferArgs{Ref: source, Section: section, DestinationRef: destination}
					var err error
					switch action {
					case "doc_move_section":
						_, err = store.MoveSection(ctx, args)
					case "doc_copy_section":
						_, err = store.CopySection(ctx, args)
					}
					transferred <- err
				}()

				select {
				case err := <-transferred:
					t.Fatalf("%s finished while a write held the contacts root (error = %v); its destination check is not coupled to its write", action, err)
				case <-time.After(contendedMutationGrace):
				}
				close(release)
				if err := <-arrivalDone; err != nil {
					t.Fatalf("arriving write: %v", err)
				}
				err := <-transferred

				if tt.wantOwner == "" {
					if err != nil {
						t.Fatalf("%s error = %v, want it to add the section to the ordinary document", action, err)
					}
					// The transfer must carry the version that landed, not
					// the empty destination its read saw.
					assertLifecycleTitle(t, store, destination, "Alice dossier")
					assertLifecycleSection(t, store, destination, section, true)
					assertLifecycleSection(t, store, source, section, action == "doc_copy_section")
					return
				}

				var refusal *StructuredDocumentMutationError
				if !errors.As(err, &refusal) || refusal.WriteTool != tt.wantOwner {
					t.Fatalf("%s error = %v, want an ownership refusal naming %s", action, err, tt.wantOwner)
				}
				record, readErr := store.Read(ctx, destination)
				if readErr != nil {
					t.Fatalf("refused %s destroyed the dossier: %v", action, readErr)
				}
				if record.ManagedBy != tt.wantOwner {
					t.Errorf("dossier managed_by = %q, want %q", record.ManagedBy, tt.wantOwner)
				}
				assertLifecycleTitle(t, store, destination, "Alice dossier")
				// A refused transfer leaves the source whole: the section
				// is not cut out on the way to a destination it never
				// reached.
				assertLifecycleSection(t, store, source, section, true)
			})
		}
	}
}

// assertLifecycleSection reports whether ref's body carries a section
// heading, so a transfer can be checked from both ends.
func assertLifecycleSection(t *testing.T, store *Store, ref, heading string, want bool) {
	t.Helper()
	record, err := store.Read(context.Background(), ref)
	if err != nil {
		t.Fatalf("Read %s: %v", ref, err)
	}
	if got := strings.Contains(record.Body, "## "+heading); got != want {
		t.Errorf("%s carries section %q = %t, want %t", ref, heading, got, want)
	}
}
