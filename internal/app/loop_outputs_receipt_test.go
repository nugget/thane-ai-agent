package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// receiptRootBackend is a per-file revision store standing in for the
// git-backed provenance writer: enough of RootWriter and RootReviser for
// the hidden-receipt/CAS path to run for real without a repository.
type receiptRootBackend struct {
	root      string
	next      int
	revisions map[string]string // filename → current revision; "" once deleted
	contents  map[string]string // revision → content
}

func newReceiptRootBackend(root string) *receiptRootBackend {
	return &receiptRootBackend{root: root, revisions: map[string]string{}, contents: map[string]string{}}
}

func (b *receiptRootBackend) current(filename string) string {
	if rev := b.revisions[filename]; rev != "" {
		return rev
	}
	return "absent"
}

func (b *receiptRootBackend) Write(_ context.Context, filename, content, _ string) error {
	return b.write(filename, content)
}

func (b *receiptRootBackend) WriteIfRevision(_ context.Context, filename, content, _ string, expectedRevision string) (string, error) {
	if actual := b.current(filename); expectedRevision != actual {
		return "", &documents.RootRevisionConflictError{Expected: expectedRevision, Actual: actual}
	}
	if err := b.write(filename, content); err != nil {
		return "", err
	}
	return b.revisions[filename], nil
}

func (b *receiptRootBackend) Delete(_ context.Context, filename, _ string) error {
	if err := os.Remove(filepath.Join(b.root, filename)); err != nil {
		return err
	}
	b.revisions[filename] = ""
	return nil
}

func (b *receiptRootBackend) Snapshot(_ context.Context, filename string) (documents.RevisionContent, error) {
	content, err := os.ReadFile(filepath.Join(b.root, filename))
	if err != nil {
		return documents.RevisionContent{}, err
	}
	rev := b.revisions[filename]
	return documents.RevisionContent{Revision: documents.RevisionRef{Commit: rev, Short: rev}, Content: string(content)}, nil
}

func (b *receiptRootBackend) Resolve(_ context.Context, filename, _ string) (documents.RevisionRef, error) {
	rev := b.revisions[filename]
	if rev == "" {
		return documents.RevisionRef{}, fmt.Errorf("%s has no revision", filename)
	}
	return documents.RevisionRef{Commit: rev, Short: rev}, nil
}

func (b *receiptRootBackend) History(context.Context, string, documents.RevisionQuery) (documents.RevisionListing, error) {
	return documents.RevisionListing{}, nil
}

func (b *receiptRootBackend) Diff(_ context.Context, _ string, from, to, _ string) (documents.RevisionDiff, error) {
	before, okBefore := b.contents[from]
	after, okAfter := b.contents[to]
	if !okBefore || !okAfter {
		return documents.RevisionDiff{}, fmt.Errorf("unknown diff endpoint %q..%q", from, to)
	}
	return documents.RevisionDiff{Added: 1, Removed: 1, Body: "-" + before + "\n+" + after + "\n"}, nil
}

func (b *receiptRootBackend) Content(context.Context, string, string) (documents.RevisionContent, error) {
	return documents.RevisionContent{}, nil
}

func (b *receiptRootBackend) write(filename, content string) error {
	path := filepath.Join(b.root, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	b.next++
	rev := fmt.Sprintf("rev-%d", b.next)
	b.revisions[filename] = rev
	b.contents[rev] = content
	return nil
}

// newReceiptLoopOutputStore builds a document store whose "core" root is
// revision-backed, so reads record receipts and whole-document writes
// are conditional — the shape prod's projects and self roots have.
func newReceiptLoopOutputStore(t *testing.T) (*documents.Store, *receiptRootBackend) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	backend := newReceiptRootBackend(dir)
	store, err := documents.NewStoreWithOptions(db, map[string]string{"core": dir}, nil, documents.StoreOptions{
		RootWriters:  map[string]documents.RootWriter{"core": backend},
		RootRevisers: map[string]documents.RootReviser{"core": backend},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return store, backend
}

// toolCallContext is the context a generated output tool or re-exposed
// doc_read runs under: the agent loop stamps the tools-package loop and
// conversation keys per tool call.
func toolCallContext(loopID, conversationID string) context.Context {
	return tools.WithConversationID(tools.WithLoopID(context.Background(), loopID), conversationID)
}

// turnBuilderContext is the context the output-context builder runs
// under: the loop runtime's own keys, stamped on the wake before the
// agent turn exists.
func turnBuilderContext(loopID, conversationID string) context.Context {
	return looppkg.WithConversationIDForTest(looppkg.WithLoopIDForTest(context.Background(), loopID), conversationID)
}

func seedDocument(t *testing.T, store *documents.Store, ref, body string) {
	t.Helper()
	if _, err := store.Write(context.Background(), documents.WriteArgs{Ref: ref, Body: &body}); err != nil {
		t.Fatalf("seed %s: %v", ref, err)
	}
}

func readBody(t *testing.T, store *documents.Store, ref string) string {
	t.Helper()
	doc, err := store.Read(context.Background(), ref)
	if err != nil {
		t.Fatalf("read %s: %v", ref, err)
	}
	return doc.Body
}

func officeOutputs() []looppkg.OutputSpec {
	return []looppkg.OutputSpec{{
		Name: "office",
		Type: looppkg.OutputTypeMaintainedDocument,
		Ref:  "core:office.md",
	}}
}

func ownDocRead(docTools *documents.Tools, outputs []looppkg.OutputSpec) looppkg.RuntimeTool {
	native := []looppkg.RuntimeTool{{
		Name: "doc_read",
		Handler: func(context.Context, map[string]any) (string, error) {
			return `{"native": true}`, nil
		},
	}}
	return wrapOwnOutputDocRead(native, docTools, outputs)[0]
}

// TestOwnOutputDocReadSatisfiesReplacePrecondition is the production
// failure end to end: a loop's replace tool refused for want of a read,
// the loop reading its own output through the privileged doc_read, and
// the retry. Before the fix the privileged read recorded no receipt, so
// the retry was refused identically — four projects-root loops sat in
// that loop for ten hours — and the refusal came back as a successful
// tool result. Now the refusal is an error that names the missing read,
// and the read satisfies it.
func TestOwnOutputDocReadSatisfiesReplacePrecondition(t *testing.T) {
	t.Parallel()
	store, _ := newReceiptLoopOutputStore(t)
	seedDocument(t, store, "core:office.md", "Bay 1 open.")
	docTools := documents.NewTools(store)
	outputs := officeOutputs()
	replace := buildLoopOutputTools(docTools, outputs)[0]
	read := ownDocRead(docTools, outputs)
	ctx := toolCallContext("loop-office", "loop-office-1-1")

	_, err := replace.Handler(ctx, map[string]any{"body": "Bays 1 and 2 open."})
	var rejected *documents.MutationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("blind replace err = %v, want *documents.MutationRejectedError", err)
	}
	if !strings.Contains(err.Error(), "no record of this loop reading core:office.md") || !strings.Contains(err.Error(), "Read core:office.md with doc_read") {
		t.Fatalf("refusal does not name the missing read: %v", err)
	}
	if strings.Contains(err.Error(), "revision parameter") {
		t.Fatalf("refusal advises a parameter the tool does not have: %v", err)
	}
	if got := readBody(t, store, "core:office.md"); !strings.Contains(got, "Bay 1 open.") {
		t.Fatalf("refused replace changed the document: %q", got)
	}

	if _, err := read.Handler(ctx, map[string]any{"ref": "core:office.md"}); err != nil {
		t.Fatalf("own-output doc_read: %v", err)
	}
	result, err := replace.Handler(ctx, map[string]any{"body": "Bays 1 and 2 open."})
	if err != nil {
		t.Fatalf("replace after read: %v", err)
	}
	if !strings.Contains(result, `"applied": true`) {
		t.Fatalf("replace after read did not apply: %s", result)
	}
	if got := readBody(t, store, "core:office.md"); !strings.Contains(got, "Bays 1 and 2 open.") {
		t.Fatalf("body after replace = %q", got)
	}
}

// TestReplaceOutputStaleReceiptIsErrorCarryingDiff: an intervening edit
// still refuses the replace, now as an error whose text carries the
// intervening diff; the receipt advances with the refusal so the
// reconciled retry lands without another read.
func TestReplaceOutputStaleReceiptIsErrorCarryingDiff(t *testing.T) {
	t.Parallel()
	store, backend := newReceiptLoopOutputStore(t)
	seedDocument(t, store, "core:office.md", "Bay 1 open.")
	docTools := documents.NewTools(store)
	outputs := officeOutputs()
	replace := buildLoopOutputTools(docTools, outputs)[0]
	read := ownDocRead(docTools, outputs)
	ctx := toolCallContext("loop-office", "loop-office-1-1")

	if _, err := read.Handler(ctx, map[string]any{"ref": "core:office.md"}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := backend.Write(ctx, "office.md", "Operator note: bay 2 is being repainted.", "operator edit"); err != nil {
		t.Fatalf("operator edit: %v", err)
	}
	_, err := replace.Handler(ctx, map[string]any{"body": "Bays 1 and 2 open."})
	var rejected *documents.MutationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("stale replace err = %v, want *documents.MutationRejectedError", err)
	}
	if !strings.Contains(err.Error(), "changed after this loop last read it") || !strings.Contains(err.Error(), "bay 2 is being repainted") {
		t.Fatalf("stale refusal lacks the intervening diff: %v", err)
	}
	result, err := replace.Handler(ctx, map[string]any{"body": "Bay 1 open; bay 2 being repainted."})
	if err != nil {
		t.Fatalf("reconciled retry: %v", err)
	}
	if !strings.Contains(result, `"applied": true`) {
		t.Fatalf("reconciled retry did not apply: %s", result)
	}
}

// TestOutputContextRenderIsTheWakeRead pins the receipt a wake gets for
// free: the Declared Durable Outputs context shows the loop its own
// document whole, and that rendering counts as the read the replace tool
// requires — under the same scope, even though the render runs on the
// loop runtime's context and the tool on the agent's. A document too
// large to render whole records nothing, so the read-first refusal still
// protects the tail the loop has not seen.
func TestOutputContextRenderIsTheWakeRead(t *testing.T) {
	t.Parallel()
	store, _ := newReceiptLoopOutputStore(t)
	seedDocument(t, store, "core:office.md", "Bay 1 open.")
	docTools := documents.NewTools(store)
	outputs := officeOutputs()
	replace := buildLoopOutputTools(docTools, outputs)[0]

	builderCtx := turnBuilderContext("loop-office", "loop-office-1-1")
	toolCtx := toolCallContext("loop-office", "loop-office-1-1")
	if got, want := tools.DocumentRevisionScope(builderCtx), tools.DocumentRevisionScope(toolCtx); got != want || got == "" {
		t.Fatalf("builder scope %q != tool scope %q", got, want)
	}

	rendered, err := renderLoopOutputContextWithNow(builderCtx, store, docTools, outputs, time.Now())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "Bay 1 open.") || strings.Contains(rendered, `"truncated": true`) {
		t.Fatalf("render should show the whole small document:\n%s", rendered)
	}
	result, err := replace.Handler(toolCtx, map[string]any{"body": "Bays 1 and 2 open."})
	if err != nil {
		t.Fatalf("replace after render, no doc_read: %v", err)
	}
	if !strings.Contains(result, `"applied": true`) {
		t.Fatalf("replace after render did not apply: %s", result)
	}

	// A new wake, and a document that no longer fits the context budget.
	big := strings.Repeat("The office stays warm and the belief accumulates. ", 500)
	if _, err := replace.Handler(toolCtx, map[string]any{"body": big}); err != nil {
		t.Fatalf("grow document: %v", err)
	}
	wake2Builder := turnBuilderContext("loop-office", "loop-office-2-2")
	wake2Tool := toolCallContext("loop-office", "loop-office-2-2")
	rendered, err = renderLoopOutputContextWithNow(wake2Builder, store, docTools, outputs, time.Now())
	if err != nil {
		t.Fatalf("render large: %v", err)
	}
	if !strings.Contains(rendered, `"truncated": true`) {
		t.Fatalf("large document should render truncated; the test proves nothing otherwise:\n%.300s", rendered)
	}
	_, err = replace.Handler(wake2Tool, map[string]any{"body": "Shrunk without reading."})
	var rejected *documents.MutationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("replace after truncated render err = %v, want read-first refusal", err)
	}
	if got := readBody(t, store, "core:office.md"); !strings.Contains(got, "belief accumulates") {
		t.Fatalf("refused replace changed the large document")
	}
	if _, err := ownDocRead(docTools, outputs).Handler(wake2Tool, map[string]any{"ref": "core:office.md"}); err != nil {
		t.Fatalf("whole read: %v", err)
	}
	if _, err := replace.Handler(wake2Tool, map[string]any{"body": "Shrunk after reading."}); err != nil {
		t.Fatalf("replace after whole read: %v", err)
	}
}

// TestPublishOutputRefusalSkipsWorkingNotes: a refused publish is an
// error, and the notes companion does not write — notes describing a
// publish that never landed would be the one half of the pair that
// moved. Once the wake's output context has shown both documents, the
// publish and its notes land together with no doc_read at all.
func TestPublishOutputRefusalSkipsWorkingNotes(t *testing.T) {
	t.Parallel()
	store, _ := newReceiptLoopOutputStore(t)
	docTools := documents.NewTools(store)
	outputs := []looppkg.OutputSpec{
		{
			Name:   "office status",
			Type:   looppkg.OutputTypeMaintainedDocument,
			Ref:    "core:office.md",
			Facets: []looppkg.FacetSpec{{Name: looppkg.OutputFacetStatusLine}},
		},
		{
			Name: "office notes",
			Type: looppkg.OutputTypeWorkingNotes,
			Ref:  "core:office-notes.md",
		},
	}
	var publish looppkg.RuntimeTool
	for _, tool := range buildLoopOutputTools(docTools, outputs) {
		if tool.Name == "publish_output_office_status" {
			publish = tool
		}
	}
	if publish.Handler == nil {
		t.Fatal("publish tool not generated")
	}

	// Wake 1: both documents are created, which needs no prior read.
	wake1 := toolCallContext("loop-office", "loop-office-1-1")
	first, err := publish.Handler(wake1, map[string]any{
		"status_line": "Printer online.",
		"full":        "# Office\n\nAll quiet.",
		"notes":       "Theory A: the printer jams on Mondays.",
	})
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if !strings.Contains(first, `"notes_written"`) {
		t.Fatalf("first publish should write notes: %s", first)
	}

	// Wake 2, before its output context has rendered: the publish is
	// refused as an error and the notes stay at Theory A.
	wake2 := toolCallContext("loop-office", "loop-office-2-2")
	_, err = publish.Handler(wake2, map[string]any{
		"status_line": "Printer jammed.",
		"full":        "# Office\n\nPaper jam in tray 2.",
		"notes":       "Theory B: it is the humidity.",
	})
	var rejected *documents.MutationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("unread publish err = %v, want *documents.MutationRejectedError", err)
	}
	if got := readBody(t, store, "core:office-notes.md"); !strings.Contains(got, "Theory A") || strings.Contains(got, "Theory B") {
		t.Fatalf("refused publish moved the notes: %q", got)
	}
	if got := readBody(t, store, "core:office.md"); strings.Contains(got, "Paper jam") {
		t.Fatalf("refused publish changed the document: %q", got)
	}

	// The wake's output context renders both documents whole; that is
	// the read. Publish and notes land together.
	if _, err := renderLoopOutputContextWithNow(turnBuilderContext("loop-office", "loop-office-2-2"), store, docTools, outputs, time.Now()); err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := publish.Handler(wake2, map[string]any{
		"status_line": "Printer jammed.",
		"full":        "# Office\n\nPaper jam in tray 2.",
		"notes":       "Theory B: it is the humidity.",
	})
	if err != nil {
		t.Fatalf("publish after render: %v", err)
	}
	var result struct {
		Published    json.RawMessage `json:"published"`
		NotesWritten json.RawMessage `json:"notes_written"`
		NotesError   string          `json:"notes_error"`
	}
	if err := json.Unmarshal([]byte(second), &result); err != nil {
		t.Fatalf("unmarshal publish result: %v\n%s", err, second)
	}
	if result.NotesError != "" || !strings.Contains(string(result.NotesWritten), `"applied": true`) || !strings.Contains(string(result.Published), `"applied": true`) {
		t.Fatalf("publish after render = %s", second)
	}
	if got := readBody(t, store, "core:office-notes.md"); !strings.Contains(got, "Theory B") {
		t.Fatalf("notes after publish = %q", got)
	}
}
