package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

type contactDossierWriterRecorder struct {
	args documents.FacetedWriteArgs
}

type contactDossierReaderRecorder struct {
	args   documents.RefArgs
	result string
	err    error
}

func (r *contactDossierReaderRecorder) Read(_ context.Context, args documents.RefArgs) (string, error) {
	r.args = args
	return r.result, r.err
}

func (w *contactDossierWriterRecorder) Write(_ context.Context, args documents.FacetedWriteArgs) (string, error) {
	w.args = args
	return `{"action":"doc_write","applied":true}`, nil
}

func TestContactDossierReadToolOwnsRefAndMakesAbsenceActionable(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := store.Upsert(&contacts.Contact{FormattedName: "Dossier Person", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}

	reader := &contactDossierReaderRecorder{err: fmt.Errorf("document not found: absent")}
	writer := &contactDossierWriterRecorder{}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments(reader.Read, writer.Write)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	tool := registry.Get("contact_dossier_read")
	if tool == nil {
		t.Fatal("contact_dossier_read not registered for a managed dossier root")
	}
	if got, want := tool.Tags, []string{"contacts", "owner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool tags = %#v, want %#v", got, want)
	}
	ctx := WithConversationID(WithLoopID(context.Background(), "archivist-1"), "loop-archivist-1-123")
	result, err := tool.Handler(ctx, map[string]any{"contact_id": contact.ID.String()})
	if err != nil {
		t.Fatalf("absent contact_dossier_read returned an error: %v", err)
	}
	if got, want := reader.args.Ref, contacts.DossierRef(contact.ID); got != want {
		t.Errorf("read ref = %q, want %q", got, want)
	}
	if got, want := reader.args.ReceiptScope, "loop:archivist-1/conversation:loop-archivist-1-123"; got != want {
		t.Errorf("receipt scope = %q, want %q", got, want)
	}
	var decoded struct {
		ContactID   string `json:"contact_id"`
		ContactName string `json:"contact_name"`
		Dossier     struct {
			Exists   bool            `json:"exists"`
			Ref      string          `json:"ref"`
			Document json.RawMessage `json:"document"`
		} `json:"dossier"`
		NextAction struct {
			Tool        string `json:"tool"`
			ContactID   string `json:"contact_id"`
			Instruction string `json:"instruction"`
		} `json:"next_action"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode absence result: %v", err)
	}
	if decoded.ContactID != contact.ID.String() || decoded.Dossier.Exists || decoded.Dossier.Ref != contacts.DossierRef(contact.ID) {
		t.Errorf("absence result = %#v", decoded)
	}
	if decoded.ContactName != contact.FormattedName || string(decoded.Dossier.Document) != "null" {
		t.Errorf("absence envelope = %#v, want stable identity and null document", decoded)
	}
	if decoded.NextAction.Tool != "contact_dossier_write" || decoded.NextAction.ContactID != contact.ID.String() {
		t.Errorf("next action = %#v, want exact dossier write", decoded.NextAction)
	}
	if !strings.Contains(decoded.NextAction.Instruction, "do not retry") {
		t.Errorf("next action instruction = %q, want retry guard", decoded.NextAction.Instruction)
	}
}

func TestContactDossierReadToolRejectsMistypedContactBeforeDocumentRead(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := &contactDossierReaderRecorder{}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, false)
	contactTools.ConfigureDossierDocuments(reader.Read, nil)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	tool := registry.Get("contact_dossier_read")
	if tool == nil {
		t.Fatal("read-only contact root did not advertise contact_dossier_read")
	}
	_, err = tool.Handler(context.Background(), map[string]any{
		"contact_id": "019c76e4-2ff1-7918-8d6f-6c2488f5098d",
	})
	if err == nil {
		t.Fatal("missing contact unexpectedly read a dossier")
	}
	for _, want := range []string{"not an active structured contact", "call contact_lookup", "instead of retrying"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-contact error = %v, want %q", err, want)
		}
	}
	if reader.args.Ref != "" {
		t.Fatalf("missing contact reached document reader with %#v", reader.args)
	}
}

func TestContactDossierReadToolReturnsExistingDocumentPayload(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := store.Upsert(&contacts.Contact{FormattedName: "Existing Dossier", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &contactDossierReaderRecorder{result: `{"ref":"contacts:existing.md","body":"current","word_count":1}`}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, false)
	contactTools.ConfigureDossierDocuments(reader.Read, nil)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	result, err := registry.Get("contact_dossier_read").Handler(context.Background(), map[string]any{
		"contact_id": contact.ID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ContactID   string `json:"contact_id"`
		ContactName string `json:"contact_name"`
		Dossier     struct {
			Exists   bool            `json:"exists"`
			Ref      string          `json:"ref"`
			Document json.RawMessage `json:"document"`
		} `json:"dossier"`
		NextAction json.RawMessage `json:"next_action"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode existing result: %v", err)
	}
	if decoded.ContactID != contact.ID.String() || decoded.ContactName != contact.FormattedName || !decoded.Dossier.Exists || decoded.Dossier.Ref != contacts.DossierRef(contact.ID) {
		t.Fatalf("existing dossier envelope = %#v", decoded)
	}
	if string(decoded.Dossier.Document) != reader.result || string(decoded.NextAction) != "null" {
		t.Fatalf("existing dossier payload = %s next_action=%s", decoded.Dossier.Document, decoded.NextAction)
	}
}

func TestContactDossierWriteToolOwnsStructureAndRevisionScope(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := store.Upsert(&contacts.Contact{FormattedName: "Dossier Person", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}

	writer := &contactDossierWriterRecorder{}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments(nil, writer.Write)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	tool := registry.Get("contact_dossier_write")
	if tool == nil {
		t.Fatal("contact_dossier_write not registered for a managed dossier root")
	}
	if !tool.SkipContentResolve {
		t.Fatal("contact dossier projections must remain literal")
	}
	if got, want := tool.Tags, []string{"contacts", "owner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool tags = %#v, want %#v", got, want)
	}
	properties := tool.Parameters["properties"].(map[string]any)
	for field, budget := range map[string]string{
		"status_line": "Maximum 120 characters",
		"teaser":      "Maximum 500 characters",
		"digest":      "Maximum 2048 characters",
	} {
		description := properties[field].(map[string]any)["description"].(string)
		if !strings.Contains(description, budget) {
			t.Errorf("%s description = %q, want %q", field, description, budget)
		}
	}
	fullDescription := properties["full"].(map[string]any)["description"].(string)
	for _, want := range []string{"archive:session:<full-session-uuid>", "full canonical session UUID", "short prefixes can be ambiguous"} {
		if !strings.Contains(fullDescription, want) {
			t.Errorf("full description = %q, want %q", fullDescription, want)
		}
	}
	wantRequired := []string{"contact_id", "status_line", "teaser", "digest", "full"}
	if got := tool.Parameters["required"].([]string); !reflect.DeepEqual(got, wantRequired) {
		t.Fatalf("required fields = %#v, want %#v", got, wantRequired)
	}

	ctx := WithConversationID(WithLoopID(context.Background(), "signal-interactive"), "signal-operator")
	_, err = tool.Handler(ctx, map[string]any{
		"contact_id":  contact.ID.String(),
		"status_line": "Relationship is current and steady.",
		"teaser":      "Recent conversation sharpened the collaboration picture.",
		"digest":      "The contact prefers direct technical collaboration and explicit boundaries.",
		"full":        "### Working style\n\nCurrent synthesis with cited evidence.",
	})
	if err != nil {
		t.Fatalf("contact_dossier_write handler: %v", err)
	}
	if got, want := writer.args.Ref, contacts.DossierRef(contact.ID); got != want {
		t.Errorf("write ref = %q, want %q", got, want)
	}
	if got, want := writer.args.ReceiptScope, "loop:signal-interactive/conversation:signal-operator"; got != want {
		t.Errorf("receipt scope = %q, want %q", got, want)
	}
}

func TestContactDossierWriteToolUnavailableWithoutManagedDocuments(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, false)
	contactTools.ConfigureDossierDocuments(nil, (&contactDossierWriterRecorder{}).Write)

	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)
	if tool := registry.Get("contact_dossier_write"); tool != nil {
		t.Fatal("read-only dossier root advertised a mutation tool")
	}
}

func TestContactDossierWriteToolRejectsEveryUnknownArgument(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments(nil, (&contactDossierWriterRecorder{}).Write)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	_, err = registry.Get("contact_dossier_write").Handler(context.Background(), map[string]any{
		"title":         "Ignored title",
		"description":   "Ignored description",
		"journal_entry": "Ignored journal entry",
		"statuz_line":   "Misspelled facet",
		"unexpected":    "Arbitrary unknown key",
	})
	if err == nil {
		t.Fatal("unknown arguments were silently accepted")
	}
	for _, want := range []string{
		"accepts only contact_id, status_line, teaser, digest, and full",
		"description, journal_entry, statuz_line, title, unexpected",
		"Go derives document identity and structure",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown argument error = %v, want it to mention %q", err, want)
		}
	}
}
