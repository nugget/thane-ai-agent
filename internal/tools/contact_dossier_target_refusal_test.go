package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// TestContactDossierWriteToolMarksTargetRefusals: the handler returns
// ErrTargetRefused, carrying the tool's own text unchanged, only for a
// refusal of the contact the call named. Every other outcome keeps its
// plain shape, so a loop charges only that refusal to no target and
// keeps waiting for a contact whose dossier was rejected.
func TestContactDossierWriteToolMarksTargetRefusals(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := contacts.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Upsert(&contacts.Contact{FormattedName: "Bob Example", Kind: "individual"})
	if err != nil {
		t.Fatal(err)
	}
	contactTools := contacts.NewTools(store, nil)
	contactTools.ConfigureDossierRoot(true, true)
	contactTools.ConfigureDossierDocuments(nil, (&contactDossierWriterRecorder{}).Write)
	registry := NewEmptyRegistry()
	registry.SetContactTools(contactTools)
	handler := registry.Get("contact_dossier_write").Handler

	args := func(contactID, statusLine string) map[string]any {
		return map[string]any{
			"contact_id":  contactID,
			"status_line": statusLine,
			"teaser":      "Recent conversation sharpened the collaboration picture.",
			"digest":      "Prefers direct technical collaboration and explicit boundaries.",
			"full":        "### Working style\n\nCurrent synthesis with cited evidence.",
		}
	}
	cases := []struct {
		name          string
		args          map[string]any
		wantErr       string
		targetRefused bool
	}{
		{name: "a contact_id that names no active contact", args: args("019c76e4-2ff1-7918-8d6f-6c2488f5098d", "Steady."),
			wantErr: "not an active structured contact", targetRefused: true},
		{name: "a non-canonical spelling of a real contact", args: args(strings.ToUpper(bob.ID.String()), "Steady."),
			wantErr: "canonical non-zero UUID"},
		{name: "a content rejection", args: args(bob.ID.String(), strings.Repeat("x", 200)), wantErr: "status_line"},
		{name: "a landed write", args: args(bob.ID.String(), "Steady.")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handler(context.Background(), tc.args)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("handler: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("handler error = %v, want it to contain %q", err, tc.wantErr)
			}
			var refused *ErrTargetRefused
			if got := errors.As(err, &refused); got != tc.targetRefused {
				t.Fatalf("errors.As(err, *ErrTargetRefused) = %v, want %v: %v", got, tc.targetRefused, err)
			}
			if tc.targetRefused && err.Error() != errors.Unwrap(err).Error() {
				t.Errorf("the refusal's text changed: %q, tool said %q", err.Error(), errors.Unwrap(err).Error())
			}
		})
	}
}
