package contacts

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// TestWriteDossierAsksForRefusalsAsErrors pins that contact_dossier_write
// asks the document store to report a refused replacement — no read on
// record, or a read the dossier has moved past — as an error. Returned
// inline as an applied:false result, the refusal would be a successful
// call, and the loop runtime counts a successful call as a landed dossier.
func TestWriteDossierAsksForRefusalsAsErrors(t *testing.T) {
	tools := newTestTools(t)
	if _, err := tools.SaveContact(`{"name":"Dossier Person","kind":"individual"}`); err != nil {
		t.Fatal(err)
	}
	contact, err := tools.store.FindByName("Dossier Person")
	if err != nil {
		t.Fatal(err)
	}
	writer := &recordingDossierWriter{}
	tools.ConfigureDossierRoot(true, true)
	tools.ConfigureDossierDocuments(nil, writer.Write)
	if _, err := tools.WriteDossier(context.Background(), DossierWriteArgs{
		ContactID:  contact.ID.String(),
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversations clarified the operator's preferred collaboration style.",
		Digest:     "The contact prefers direct technical collaboration and explicit source-of-truth boundaries.",
		Full:       "# Relationship context\n\nCurrent synthesis — evidence: archive:session:019c52f0-9ce8-7708-867f-35da2e6b4777.",
	}); err != nil {
		t.Fatalf("WriteDossier: %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
	if !writer.args.RejectionIsError {
		t.Error("dossier write leaves RejectionIsError unset, so a refused replacement reads as a landed write")
	}
}

// TestDossierRejectionSaysNothingWasWritten covers both dossier refusal
// sites — the contact_dossier_write tool and the contacts-root backstop —
// and pins that each reports a write that did not land, asks for every
// field in the next call, and sets no retry count.
func TestDossierRejectionSaysNothingWasWritten(t *testing.T) {
	overBudget := strings.Repeat("s", 121)
	tests := []struct {
		name   string
		reject func(t *testing.T) error
	}{
		{
			name: "contact_dossier_write",
			reject: func(t *testing.T) error {
				tools := newTestTools(t)
				if _, err := tools.SaveContact(`{"name":"Dossier Person","kind":"individual"}`); err != nil {
					t.Fatal(err)
				}
				contact, err := tools.store.FindByName("Dossier Person")
				if err != nil {
					t.Fatal(err)
				}
				writer := &recordingDossierWriter{}
				tools.ConfigureDossierRoot(true, true)
				tools.ConfigureDossierDocuments(nil, writer.Write)
				_, err = tools.WriteDossier(context.Background(), DossierWriteArgs{
					ContactID:  contact.ID.String(),
					StatusLine: overBudget,
					Teaser:     "Useful hook.",
					Digest:     "Enough context to act.",
					Full:       "Complete detail.",
				})
				if writer.calls != 0 {
					t.Fatalf("rejected dossier reached the writer %d times", writer.calls)
				}
				return err
			},
		},
		{
			name: "contacts root backstop",
			reject: func(t *testing.T) error {
				id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
				candidate := validDossierCandidate(id)
				candidate.Body = dossierOutputContract.Render(documentfacets.Payload{
					StatusLine: overBudget,
					Teaser:     "Useful hook.",
					Digest:     "Enough context to act.",
					Full:       "Complete detail.",
				})
				return dossierValidatorForName("Dossier Person")(candidate)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.reject(t)
			if err == nil {
				t.Fatal("over-budget dossier was accepted")
			}
			for _, want := range []string{
				"projections are invalid and nothing was written",
				"correct every listed field in your next call",
				"status_line is 121 characters and the limit is 120 (1 over)",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
			for _, absent := range []string{"retry once", "truncat"} {
				if strings.Contains(err.Error(), absent) {
					t.Errorf("error = %q, must not contain %q", err, absent)
				}
			}
		})
	}
}
