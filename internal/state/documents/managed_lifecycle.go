package documents

import (
	"fmt"
	"os"
	"strings"
)

// ManagedLifecycleRefusal says how a refused doc_delete, doc_move, or
// doc_copy would have gone around a document's owning tool.
type ManagedLifecycleRefusal int

const (
	// RefusedRetire refuses deleting the document, or moving it out of
	// its root.
	RefusedRetire ManagedLifecycleRefusal = iota
	// RefusedOverwrite refuses replacing the document, which its owner
	// keeps, with another document's content.
	RefusedOverwrite
	// RefusedArrival refuses writing a document whose managed_by names
	// the owner into a root that accepts such a document only through
	// that owner.
	RefusedArrival
)

// ManagedDocumentLifecycleError refuses doc_delete, doc_move, or doc_copy
// where the call would go around the narrower tool that owns a document
// in a root that accepts it only through that tool, such as a contact
// dossier in the contacts root. A generic delete would retire the
// document without its owner, a move would carry it out from under the
// root's domain contract, and a move or copy onto it, or of an owned
// document into the root, would replace what the owner wrote.
type ManagedDocumentLifecycleError struct {
	// Ref is the document the refused call would have deleted, moved
	// away, or written.
	Ref       string
	Root      string
	Attempted string
	WriteTool string
	Refusal   ManagedLifecycleRefusal
	// Source is the document a refused move or copy would have read
	// from. It is empty for [RefusedRetire].
	Source string
}

// Error names the owning tool and says who can retire, relocate, or
// restore the document, since the owning tool only replaces content.
func (e *ManagedDocumentLifecycleError) Error() string {
	switch e.Refusal {
	case RefusedOverwrite:
		return fmt.Sprintf("%s cannot overwrite %s with %s because %s owns that document and the %s root accepts it only through that tool; no change was made. "+
			"To change what the document says, use %s. Replacing it wholesale with another document's content is not a document-tool operation: "+
			"if an earlier version should come back, report that to the operator, who can restore it from the %s root's repository",
			e.Attempted, e.Ref, e.Source, e.WriteTool, e.Root, e.WriteTool, e.Root)
	case RefusedArrival:
		return fmt.Sprintf("%s cannot write %s into %s because its managed_by names %s and the %s root accepts such a document only through that tool; no change was made. "+
			"To write a document there, use %s",
			e.Attempted, e.Source, e.Ref, e.WriteTool, e.Root, e.WriteTool)
	}
	return fmt.Sprintf("%s cannot %s %s because %s owns that document and the %s root accepts it only through that tool; no change was made. "+
		"To change what the document says, use %s. Retiring or relocating it is not a document-tool operation: "+
		"if it should go, report that to the operator, who can remove or move it in the %s root's repository. "+
		"That includes a document %s no longer reaches because its subject is gone, such as the dossier of a forgotten contact",
		e.Attempted, lifecycleVerb(e.Attempted), e.Ref, e.WriteTool, e.Root, e.WriteTool, e.Root, e.WriteTool)
}

func lifecycleVerb(action string) string {
	switch action {
	case "doc_delete":
		return "delete"
	case "doc_move":
		return "move"
	default:
		return "change"
	}
}

// narrowerOwner returns the record's managed_by when it names an owner
// narrower than doc_write, and "" for an unmanaged or doc_write document.
func narrowerOwner(record *DocumentRecord) string {
	if record == nil {
		return ""
	}
	owner := strings.TrimSpace(record.ManagedBy)
	if owner == DocumentWriteToolName {
		return ""
	}
	return owner
}

// refuseManagedLifecycle returns a [ManagedDocumentLifecycleError] when
// record names an owner narrower than doc_write and its root has a write
// validator, the mark of a root whose documents belong to a domain
// contract. Every other document stays deletable and movable: an
// unmanaged one, a doc_write one (doc_write has no delete of its own), and
// a managed one in an ordinary root, such as the outputs a deleted loop
// leaves behind, whose owning tool no longer exists.
func (s *Store) refuseManagedLifecycle(action, ref, root string, record *DocumentRecord) error {
	owner := narrowerOwner(record)
	if owner == "" || s.rootValidator(root) == nil {
		return nil
	}
	return &ManagedDocumentLifecycleError{Ref: ref, Root: root, Attempted: action, WriteTool: owner, Refusal: RefusedRetire}
}

// transferDestination is where a doc_move or doc_copy would write, as
// the caller named it and as it resolved.
type transferDestination struct {
	ref, root, relPath, absPath string
}

// refuseManagedTransfer guards the destination of doc_move and doc_copy
// in a root with a write validator. It refuses overwriting a document a
// narrower owner keeps there, whatever the source, and refuses carrying
// in a source whose managed_by names such an owner, since that content
// arrives legitimately only through the owner. The validator checks
// content alone, so a stale copy of an owned document would pass it and
// roll the document back. An ordinary destination root is left alone,
// as [Store.refuseManagedLifecycle] leaves an ordinary source.
func (s *Store) refuseManagedTransfer(action, sourceRef string, source *DocumentRecord, dst transferDestination) error {
	if s.rootValidator(dst.root) == nil {
		return nil
	}
	refusal := &ManagedDocumentLifecycleError{Ref: dst.ref, Root: dst.root, Attempted: action, Source: sourceRef}
	destination, _, _, err := s.readDocumentFile(dst.absPath, dst.root, dst.relPath)
	switch {
	case err == nil:
		if owner := narrowerOwner(destination); owner != "" {
			refusal.WriteTool, refusal.Refusal = owner, RefusedOverwrite
			return refusal
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read destination document: %w", err)
	}
	if owner := narrowerOwner(source); owner != "" {
		refusal.WriteTool, refusal.Refusal = owner, RefusedArrival
		return refusal
	}
	return nil
}
