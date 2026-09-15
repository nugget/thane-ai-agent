package documents

import (
	"fmt"
	"strings"
)

// ManagedDocumentLifecycleError refuses doc_delete or doc_move on a
// document its root accepts only through a narrower owning tool, such as
// a contact dossier in the contacts root. A generic delete would retire
// the document without its owner, and a move would carry it out from
// under the root's domain contract.
type ManagedDocumentLifecycleError struct {
	Ref       string
	Root      string
	Attempted string
	WriteTool string
}

// Error names the owning tool and says who can retire or relocate the
// document, since the owning tool only replaces content.
func (e *ManagedDocumentLifecycleError) Error() string {
	return fmt.Sprintf("%s cannot %s %s because %s owns that document and the %s root accepts it only through that tool; no change was made. "+
		"To change what the document says, use %s. Retiring or relocating it is not a document-tool operation: "+
		"if it should go, report that to the operator, who can remove or move it in the %s root's repository",
		e.Attempted, lifecycleVerb(e.Attempted), e.Ref, e.WriteTool, e.Root, e.WriteTool, e.Root)
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

// refuseManagedLifecycle returns a [ManagedDocumentLifecycleError] when
// record names an owner narrower than doc_write and its root has a write
// validator, the mark of a root whose documents belong to a domain
// contract. Every other document stays deletable and movable: an
// unmanaged one, a doc_write one (doc_write has no delete of its own), and
// a managed one in an ordinary root, such as the outputs a deleted loop
// leaves behind, whose owning tool no longer exists.
func (s *Store) refuseManagedLifecycle(action, ref, root string, record *DocumentRecord) error {
	if record == nil {
		return nil
	}
	owner := strings.TrimSpace(record.ManagedBy)
	if owner == "" || owner == DocumentWriteToolName || s.rootValidator(root) == nil {
		return nil
	}
	return &ManagedDocumentLifecycleError{Ref: ref, Root: root, Attempted: action, WriteTool: owner}
}
