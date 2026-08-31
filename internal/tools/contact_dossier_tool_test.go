package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

type contactDossierWriterRecorder struct {
	args documents.WriteArgs
}

func (w *contactDossierWriterRecorder) Write(_ context.Context, args documents.WriteArgs) (string, error) {
	w.args = args
	return `{"action":"doc_write","applied":true}`, nil
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
	contactTools := contacts.NewTools(store)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments(writer.Write)
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
	contactTools := contacts.NewTools(store)
	contactTools.ConfigureDossierRoot(true, false)
	contactTools.ConfigureDossierDocuments((&contactDossierWriterRecorder{}).Write)

	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)
	if tool := registry.Get("contact_dossier_write"); tool != nil {
		t.Fatal("read-only dossier root advertised a mutation tool")
	}
}

func TestContactDossierWriteToolRejectsGenericDocumentArguments(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	contactTools := contacts.NewTools(store)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments((&contactDossierWriterRecorder{}).Write)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)

	_, err = registry.Get("contact_dossier_write").Handler(context.Background(), map[string]any{"body": "wrong door"})
	if err == nil || !strings.Contains(err.Error(), "pass contact_id plus status_line, teaser, digest, and full") {
		t.Fatalf("generic argument error = %v, want one-step redirect", err)
	}
}
