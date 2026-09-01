package documents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

func TestToolsPublishCreatesCanonicalFacetedDocument(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	tools := NewTools(store)
	teaser := "Open this for the current migration state."
	digest := "The migration is complete and the old write path is retired."

	result, err := tools.Publish(context.Background(), PublishArgs{
		Ref:         "kb:roadmap.md",
		Title:       "Roadmap",
		Description: "Current roadmap state",
		Tags:        []string{"roadmap", "status"},
		StatusLine:  "Migration complete; monitoring the new path.",
		Teaser:      &teaser,
		Digest:      &digest,
		Full:        "The migration completed cleanly.\n\n### Follow-up\n\nWatch production for one week.",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !strings.Contains(result, `"action": "doc_write"`) {
		t.Fatalf("Publish result = %s, want doc_write action", result)
	}

	raw, err := os.ReadFile(filepath.Join(kbDir, "roadmap.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range []string{
		`managed_by: "doc_write"`,
		`thane_document: "faceted/v1"`,
		"## Status Line\n\nMigration complete; monitoring the new path.",
		"## Teaser\n\n" + teaser,
		"## Digest\n\n" + digest,
		"## Details\n\nThe migration completed cleanly.",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("published document =\n%s\nwant %q", raw, want)
		}
	}

	read, err := tools.Read(context.Background(), RefArgs{Ref: "kb:roadmap.md"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var payload struct {
		Faceted   bool     `json:"faceted"`
		Facets    []string `json:"facets"`
		Levels    []string `json:"levels_available"`
		WriteTool string   `json:"write_tool"`
	}
	if err := json.Unmarshal([]byte(read), &payload); err != nil {
		t.Fatalf("Unmarshal read result: %v", err)
	}
	if !payload.Faceted {
		t.Fatalf("read result = %s, want faceted=true", read)
	}
	if want := []string{"status_line", "teaser", "digest"}; !reflect.DeepEqual(payload.Facets, want) {
		t.Fatalf("facets = %v, want %v", payload.Facets, want)
	}
	if want := []string{"status_line", "teaser", "digest", "full"}; !reflect.DeepEqual(payload.Levels, want) {
		t.Fatalf("levels_available = %v, want %v", payload.Levels, want)
	}
	if payload.WriteTool != DocumentWriteToolName {
		t.Fatalf("write_tool = %q, want %q", payload.WriteTool, DocumentWriteToolName)
	}
}

func TestToolsPublishEquivalentLogicalStateIsByteStable(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	tools := NewTools(store)
	contract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}}
	body := contract.Render(documentfacets.Payload{
		StatusLine: "Production is healthy.",
		Full:       "All monitored services are operating normally.",
	})
	frontmatter := (documentfacets.Manifest{
		Schema:    documentfacets.SchemaV1,
		Contract:  contract,
		ManagedBy: DocumentWriteToolName,
	}).Frontmatter()
	frontmatter["title"] = []string{"Service status"}
	frontmatter["tags"] = []string{"status", "operations"}
	frontmatter["created"] = []string{"2026-01-02T03:04:05Z"}
	frontmatter["updated"] = []string{"2026-02-03T04:05:06Z"}

	path := filepath.Join(kbDir, "service-status.md")
	before := []byte(renderDocument(frontmatter, body))
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, err := tools.Publish(context.Background(), PublishArgs{
		Ref:        "kb:service-status.md",
		Title:      "Service status",
		Tags:       []string{"operations", "status", "operations"},
		StatusLine: "Production is healthy.",
		Full:       "All monitored services are operating normally.",
	}); err != nil {
		t.Fatalf("Publish equivalent state: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("equivalent publish changed canonical bytes:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestWriteFacetedUsesOwningToolInRootCommitMessage(t *testing.T) {
	t.Parallel()

	writer := &recordingRootWriter{}
	store, kbDir := newPolicyStore(t, map[string]RootPolicy{
		"kb": {Indexing: true, Authoring: AuthoringManaged},
	}, map[string]RootWriter{"kb": writer})
	writer.root = kbDir
	contract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}, {Name: documentfacets.Digest}}}

	if _, err := NewTools(store).WriteFaceted(context.Background(), FacetedWriteArgs{
		Ref:       "kb:owned.md",
		Contract:  contract,
		Payload:   documentfacets.Payload{StatusLine: "Current state.", Digest: "Current summary.", Full: "Current detail."},
		WriteTool: "publish_output_owned",
	}); err != nil {
		t.Fatalf("WriteFaceted: %v", err)
	}
	if len(writer.writes) != 1 || !strings.HasPrefix(writer.writes[0], "owned.md|publish_output_owned kb:owned.md — Current state.") {
		t.Fatalf("writer.writes = %#v, want owning tool in commit subject", writer.writes)
	}
}

func TestToolsPublishRequiresEveryExistingProjectionWithoutChangingDocument(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	tools := NewTools(store)
	teaser := "Open this for detail."
	digest := "The current standalone summary."
	_, err := tools.Publish(context.Background(), PublishArgs{
		Ref:        "kb:status.md",
		StatusLine: "Initial status.",
		Teaser:     &teaser,
		Digest:     &digest,
		Full:       "Initial detail.",
	})
	if err != nil {
		t.Fatalf("initial Publish: %v", err)
	}
	path := filepath.Join(kbDir, "status.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	_, err = tools.Publish(context.Background(), PublishArgs{
		Ref:        "kb:status.md",
		StatusLine: "New status.",
		Full:       "New detail.",
	})
	if err == nil || !strings.Contains(err.Error(), "teaser is required") || !strings.Contains(err.Error(), "digest is required") {
		t.Fatalf("Publish omission error = %v, want both existing projections named", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected publish changed document:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestToolsPublishAdoptsOrdinaryDocument(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()
	_, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:notes.md",
		Title: "Notes",
		Body:  stringPtr("Old unstructured body."),
	})
	if err != nil {
		t.Fatalf("initial Write: %v", err)
	}
	tools := NewTools(store)
	full := "Replacement detail with a nested section.\n\n### Evidence\n\nThe exact supplied body."
	_, err = tools.Publish(ctx, PublishArgs{
		Ref:        "kb:notes.md",
		StatusLine: "Notes have been structured.",
		Full:       full,
	})
	if err != nil {
		t.Fatalf("Publish adoption: %v", err)
	}
	record, err := store.Read(ctx, "kb:notes.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	payload, faceted := looppkg.ParseFacetSections(record.Body)
	if !faceted {
		t.Fatalf("adopted body = %q, want faceted document", record.Body)
	}
	if payload.Full != full {
		t.Fatalf("full = %q, want exact supplied body %q", payload.Full, full)
	}
	if record.Title != "Notes" {
		t.Fatalf("title = %q, want existing title preserved", record.Title)
	}
	if record.ManagedBy != DocumentWriteToolName {
		t.Fatalf("managed_by = %q, want %q", record.ManagedBy, DocumentWriteToolName)
	}
}

func TestGenericMutationsRedirectFacetedDocuments(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()
	tools := NewTools(store)
	_, err := tools.Publish(ctx, PublishArgs{
		Ref:        "kb:status.md",
		StatusLine: "Current state.",
		Full:       "Current detail.",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, err = store.Write(ctx, WriteArgs{
		Ref:  "kb:ordinary.md",
		Body: stringPtr("## Source\n\nOrdinary section."),
	})
	if err != nil {
		t.Fatalf("write ordinary source: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "write",
			run: func() error {
				_, err := tools.Write(ctx, WriteArgs{Ref: "kb:status.md", Body: stringPtr("replacement")})
				return err
			},
		},
		{
			name: "edit",
			run: func() error {
				_, err := tools.Edit(ctx, EditArgs{Ref: "kb:status.md", Mode: "replace_body", Body: "replacement"})
				return err
			},
		},
		{
			name: "journal",
			run: func() error {
				_, err := tools.JournalUpdate(ctx, JournalUpdateArgs{Ref: "kb:status.md", Entry: "note"})
				return err
			},
		},
		{
			name: "copy section into",
			run: func() error {
				_, err := tools.CopySection(ctx, SectionTransferArgs{Ref: "kb:ordinary.md", Section: "Source", DestinationRef: "kb:status.md"})
				return err
			},
		},
		{
			name: "move section out",
			run: func() error {
				_, err := tools.MoveSection(ctx, SectionTransferArgs{Ref: "kb:status.md", Section: "Details", DestinationRef: "kb:moved.md"})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil || !strings.Contains(err.Error(), DocumentWriteToolName) || !strings.Contains(err.Error(), "no change was made") {
				t.Fatalf("mutation error = %v, want doc_write redirect", err)
			}
		})
	}

	record, err := store.Read(ctx, "kb:status.md")
	if err != nil {
		t.Fatalf("Read status: %v", err)
	}
	payload, _ := looppkg.ParseFacetSections(record.Body)
	if payload.StatusLine != "Current state." || payload.Full != "Current detail." {
		t.Fatalf("rejected mutations changed status document: %#v", payload)
	}
	if _, err := store.Read(ctx, "kb:moved.md"); !IsNotFound(err) {
		t.Fatalf("move created destination despite rejection: %v", err)
	}
}

func TestStoreRejectsInvalidFacetedBodyBeforeMutation(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	contract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}}
	frontmatter := (documentfacets.Manifest{Contract: contract, ManagedBy: DocumentWriteToolName}).Frontmatter()
	_, err := store.Write(context.Background(), WriteArgs{
		Ref:            "kb:invalid.md",
		Frontmatter:    frontmatter,
		Body:           stringPtr("## Status Line\n\n" + strings.Repeat("x", 121) + "\n\n## Details\n\nDetail."),
		StructuredTool: DocumentWriteToolName,
	})
	if err == nil || !strings.Contains(err.Error(), "status_line is 121 characters") {
		t.Fatalf("Write error = %v, want shared facet budget failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(kbDir, "invalid.md")); !os.IsNotExist(statErr) {
		t.Fatalf("rejected write changed filesystem; stat error = %v", statErr)
	}
}

func TestStoreAcceptsGeneratedJSONFacetEnvelope(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	output := looppkg.OutputSpec{
		Name: "machine_status",
		Type: looppkg.OutputTypeMaintainedDocument,
		Facets: []looppkg.FacetSpec{{
			Name:   looppkg.OutputFacetStatusLine,
			Format: looppkg.FacetFormatJSON,
		}},
	}
	body := output.RenderFacetDocument(looppkg.FacetPayload{
		StatusLine: `{"healthy":true,"workers":3}`,
		Full:       "All workers are healthy.",
	})
	contract := documentfacets.Contract{Facets: append([]documentfacets.Spec(nil), output.Facets...)}
	_, err := store.Write(context.Background(), WriteArgs{
		Ref:            "kb:machine-status.md",
		Frontmatter:    (documentfacets.Manifest{Contract: contract, ManagedBy: output.ToolName()}).Frontmatter(),
		Body:           &body,
		StructuredTool: output.ToolName(),
	})
	if err != nil {
		t.Fatalf("Write JSON-faceted output: %v", err)
	}
	record, err := store.Read(context.Background(), "kb:machine-status.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(record.Body, "```json\n") {
		t.Fatalf("body = %q, want generated JSON fence preserved", record.Body)
	}
}

func TestToolsPublishHonorsNarrowerOwner(t *testing.T) {
	t.Parallel()

	store, _ := newMutationStore(t)
	ctx := context.Background()
	contract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}}
	body := contract.Render(documentfacets.Payload{StatusLine: "Owned state.", Full: "Owned detail."})
	_, err := store.Write(ctx, WriteArgs{
		Ref:            "kb:owned.md",
		Frontmatter:    (documentfacets.Manifest{Contract: contract, ManagedBy: "publish_output_owned"}).Frontmatter(),
		Body:           &body,
		StructuredTool: "publish_output_owned",
	})
	if err != nil {
		t.Fatalf("seed owner document: %v", err)
	}
	_, err = NewTools(store).Publish(ctx, PublishArgs{
		Ref:        "kb:owned.md",
		StatusLine: "Replacement state.",
		Full:       "Replacement detail.",
	})
	if err == nil || !strings.Contains(err.Error(), "publish_output_owned owns that document") {
		t.Fatalf("Publish error = %v, want owning-tool redirect", err)
	}
}

func TestMalformedCanonicalManifestNeverExposesStorageEnvelope(t *testing.T) {
	t.Parallel()

	store, kbDir := newMutationStore(t)
	path := filepath.Join(kbDir, "malformed.md")
	raw := `---
facets: ["digest"]
managed_by: "doc_write"
thane_document: "faceted/v1"
---

## Digest

PRIVATE_DIGEST_MARKER

## Details

PRIVATE_FULL_MARKER
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	tools := NewTools(store)

	_, err := tools.Read(context.Background(), RefArgs{Ref: "kb:malformed.md"})
	if err == nil {
		t.Fatal("Read returned malformed private storage envelope")
	}
	for _, want := range []string{"invalid faceted manifest", DocumentWriteToolName, "no private storage envelope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Read error = %q, want %q", err, want)
		}
	}
	for _, secret := range []string{"PRIVATE_DIGEST_MARKER", "PRIVATE_FULL_MARKER", "## Digest", "## Details"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("Read error exposed %q: %v", secret, err)
		}
	}
	results, err := store.Search(context.Background(), SearchQuery{Query: "PRIVATE_FULL_MARKER", Limit: 10})
	if err != nil {
		t.Fatalf("Search malformed document: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("search indexed malformed private storage content: %#v", results)
	}

	if _, err := tools.Write(context.Background(), WriteArgs{
		Ref:  "kb:malformed.md",
		Body: stringPtr("body-only overwrite"),
	}); err == nil || !strings.Contains(err.Error(), DocumentWriteToolName) {
		t.Fatalf("body write error = %v, want structured repair redirect", err)
	}

	digest := "Repaired digest."
	if _, err := tools.Publish(context.Background(), PublishArgs{
		Ref:        "kb:malformed.md",
		StatusLine: "Manifest repaired.",
		Digest:     &digest,
		Full:       "Repaired complete state.",
	}); err != nil {
		t.Fatalf("Publish repair: %v", err)
	}
	read, err := tools.Read(context.Background(), RefArgs{Ref: "kb:malformed.md"})
	if err != nil {
		t.Fatalf("Read repaired document: %v", err)
	}
	if !strings.Contains(read, "Repaired complete state.") || strings.Contains(read, "PRIVATE_FULL_MARKER") {
		t.Fatalf("repaired read = %s", read)
	}
}
