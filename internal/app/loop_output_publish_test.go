package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func tieredSpec() looppkg.Spec {
	return looppkg.Spec{
		Name:       "ranch_office",
		Enabled:    true,
		Task:       "Curate the office domain.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{
			{
				Name:  "office status",
				Type:  looppkg.OutputTypeMaintainedDocument,
				Ref:   "core:office.md",
				Tiers: []looppkg.OutputTier{looppkg.OutputTierStatusLine, looppkg.OutputTierTeaser},
			},
			{
				Name:          "office notes",
				Type:          looppkg.OutputTypeWorkingNotes,
				Ref:           "core:office-notes.md",
				JournalWindow: "month",
				MaxWindows:    6,
			},
		},
	}
}

func findRuntimeTool(t *testing.T, spec looppkg.Spec, name string) looppkg.RuntimeTool {
	t.Helper()
	for _, tool := range spec.RuntimeTools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("runtime tool %q not generated; got %d tools", name, len(spec.RuntimeTools))
	return looppkg.RuntimeTool{}
}

func TestTieredOutputGeneratesPublishToolWithTypedProjections(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}

	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}

	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")
	props, _ := publish.Parameters["properties"].(map[string]any)
	for _, key := range []string{"status_line", "teaser", "full", "note"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("publish tool schema missing %q argument; got %v", key, props)
		}
	}
	if _, ok := props["digest"]; ok {
		t.Fatal("publish tool advertises digest, which this output does not declare")
	}

	required, _ := publish.Parameters["required"].([]string)
	if strings.Join(required, ",") != "status_line,teaser,full" {
		t.Fatalf("required = %v, want the declared projections and full but not note", required)
	}
	if !strings.Contains(props["status_line"].(map[string]any)["description"].(string), "120 characters") {
		t.Fatalf("status_line description should state its budget: %v", props["status_line"])
	}
}

func TestPublishToolWithoutWorkingNotesOmitsNoteArgument(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	spec := tieredSpec()
	spec.Outputs = spec.Outputs[:1] // drop the working-notes declaration

	hydrated, err := app.hydrateLoopOutputs(spec)
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")
	props, _ := publish.Parameters["properties"].(map[string]any)
	if _, ok := props["note"]; ok {
		t.Fatal("note argument advertised with no working-notes output to receive it")
	}
}

func TestPublishToolRendersDocumentAndStampsFrontmatter(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	if _, err := publish.Handler(context.Background(), map[string]any{
		"status_line": "Printer online; 2 packages waiting.",
		"teaser":      "The desk is clear but two deliveries need signing.",
		"full":        "# Office\n\n### Deliveries\n\nTwo packages.",
	}); err != nil {
		t.Fatalf("publish handler: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(coreDir, "office.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	written := string(raw)
	for _, want := range []string{
		"## Status Line",
		"Printer online; 2 packages waiting.",
		"## Teaser",
		"## Details",
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("published document missing %q:\n%s", want, written)
		}
	}

	// The document is the canonical store: parsing it back must yield
	// exactly what was published.
	doc, err := store.Read(context.Background(), "core:office.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := firstFrontmatterValue(doc, "audience"); got != "published" {
		t.Fatalf("audience frontmatter = %q, want published", got)
	}
	if got := firstFrontmatterValue(doc, "managed_by"); got != "publish_output_office_status" {
		t.Fatalf("managed_by frontmatter = %q, want the publish tool name", got)
	}
	payload := looppkg.ParseTierDocument(doc.Body)
	if payload.StatusLine != "Printer online; 2 packages waiting." {
		t.Fatalf("re-parsed status line = %q", payload.StatusLine)
	}
	if payload.Full != "# Office\n\n### Deliveries\n\nTwo packages." {
		t.Fatalf("re-parsed full = %q", payload.Full)
	}
}

func TestPublishToolRejectsOverBudgetWithoutWriting(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	_, err = publish.Handler(context.Background(), map[string]any{
		"status_line": strings.Repeat("x", 200),
		"teaser":      "Fine.",
		"full":        "Fine.",
	})
	if err == nil {
		t.Fatal("publish handler error = nil for an over-budget status line")
	}
	if !strings.Contains(err.Error(), "the limit is 120") {
		t.Fatalf("error should teach the budget: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(coreDir, "office.md")); !os.IsNotExist(statErr) {
		t.Fatal("a rejected publish must not write the document")
	}
}

func TestPublishToolNoteAppendsInternalWorkingNotes(t *testing.T) {
	t.Parallel()

	store, coreDir := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	result, err := publish.Handler(context.Background(), map[string]any{
		"status_line": "Printer online.",
		"teaser":      "Nothing needs attention.",
		"full":        "# Office\n\nAll quiet.",
		"note":        "Dropped the delivery warning — both packages were signed for at 14:20.",
	})
	if err != nil {
		t.Fatalf("publish handler: %v", err)
	}
	if !strings.Contains(result, "note_recorded") {
		t.Fatalf("result should report the recorded note: %s", result)
	}

	raw, err := os.ReadFile(filepath.Join(coreDir, "office-notes.md"))
	if err != nil {
		t.Fatalf("ReadFile notes: %v", err)
	}
	notes := string(raw)
	if !strings.Contains(notes, "both packages were signed for") {
		t.Fatalf("working notes missing the entry:\n%s", notes)
	}
	// The audience stamp is what makes the notes document invisible to
	// search and tagged-guidance injection.
	notesDoc, err := store.Read(context.Background(), "core:office-notes.md")
	if err != nil {
		t.Fatalf("Read notes: %v", err)
	}
	if got := firstFrontmatterValue(notesDoc, "audience"); got != "internal" {
		t.Fatalf("working notes audience = %q, want internal:\n%s", got, notes)
	}

	// The note must not leak into the published document.
	published, err := os.ReadFile(filepath.Join(coreDir, "office.md"))
	if err != nil {
		t.Fatalf("ReadFile published: %v", err)
	}
	if strings.Contains(string(published), "signed for at 14:20") {
		t.Fatalf("note content leaked into the published document:\n%s", string(published))
	}
}

func TestWorkingNotesAppendToolStampsInternalAudience(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	appendNotes := findRuntimeTool(t, hydrated, "append_output_office_notes")
	if !strings.Contains(appendNotes.Description, "private process log") {
		t.Fatalf("working-notes tool should frame itself as private: %q", appendNotes.Description)
	}

	if _, err := appendNotes.Handler(context.Background(), map[string]any{
		"entry": "Reconsidered the trough threshold.",
	}); err != nil {
		t.Fatalf("append notes handler: %v", err)
	}
	doc, err := store.Read(context.Background(), "core:office-notes.md")
	if err != nil {
		t.Fatalf("Read notes: %v", err)
	}
	if got := firstFrontmatterValue(doc, "audience"); got != "internal" {
		t.Fatalf("direct working-notes append missing the internal stamp: audience = %q", got)
	}
}

// firstFrontmatterValue reads one indexed frontmatter value — the same
// shape the document layer's audience exclusion reads, rather than the
// rendered YAML text.
func firstFrontmatterValue(doc *documents.DocumentRecord, key string) string {
	if doc == nil {
		return ""
	}
	if values := doc.Frontmatter[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func TestJournalOutputDoesNotClaimInternalAudience(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	spec := tieredSpec()
	spec.Outputs = []looppkg.OutputSpec{{
		Name:          "office journal",
		Type:          looppkg.OutputTypeJournalDocument,
		Ref:           "core:office-journal.md",
		JournalWindow: "day",
	}}
	hydrated, err := app.hydrateLoopOutputs(spec)
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	appendJournal := findRuntimeTool(t, hydrated, "append_output_office_journal")
	if _, err := appendJournal.Handler(context.Background(), map[string]any{"entry": "Ordinary entry."}); err != nil {
		t.Fatalf("append journal handler: %v", err)
	}
	doc, err := store.Read(context.Background(), "core:office-journal.md")
	if err != nil {
		t.Fatalf("Read journal: %v", err)
	}
	if got := firstFrontmatterValue(doc, "audience"); got != "" {
		t.Fatalf("a published journal must carry no audience stamp, got %q", got)
	}
}

func TestTieredOutputContextAdvertisesProjections(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}

	block, err := hydrated.OutputContextBuilder(context.Background(), hydrated.Outputs)
	if err != nil {
		t.Fatalf("OutputContextBuilder: %v", err)
	}
	for _, want := range []string{
		"publish_output_office_status",
		`"status_line"`,
		`"audience": "published"`,
		`"audience": "internal"`,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("output context missing %q:\n%s", want, block)
		}
	}
}

func TestPublishToolRejectsNonStringProjection(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

	_, err = publish.Handler(context.Background(), map[string]any{
		"status_line": 42,
		"teaser":      "Fine.",
		"full":        "Fine.",
	})
	if err == nil || !strings.Contains(err.Error(), "status_line must be a string") {
		t.Fatalf("error = %v, want a typed argument error naming status_line", err)
	}
}

func TestTieredOutputContextReportsPublishMode(t *testing.T) {
	t.Parallel()

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store}
	hydrated, err := app.hydrateLoopOutputs(tieredSpec())
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	block, err := hydrated.OutputContextBuilder(context.Background(), hydrated.Outputs)
	if err != nil {
		t.Fatalf("OutputContextBuilder: %v", err)
	}
	// A tiered output advertises publish_output_*, so pairing it with
	// the spec-level replace mode would describe a call that does not
	// exist for this output.
	if !strings.Contains(block, `"mode": "publish"`) {
		t.Fatalf("tiered output context should report publish mode:\n%s", block)
	}
	if strings.Contains(block, `"mode": "replace"`) {
		t.Fatalf("tiered output context still reports replace mode:\n%s", block)
	}
	if !strings.Contains(block, `"mode": "append"`) {
		t.Fatalf("working-notes output should still report append mode:\n%s", block)
	}
}
