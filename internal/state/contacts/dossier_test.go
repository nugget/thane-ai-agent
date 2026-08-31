package contacts

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

type recordingDossierWriter struct {
	args  documents.WriteArgs
	calls int
}

func (w *recordingDossierWriter) Write(_ context.Context, args documents.WriteArgs) (string, error) {
	w.args = args
	w.calls++
	return `{"action":"doc_write","applied":true}`, nil
}

func validDossierCandidate(id uuid.UUID) documents.DocumentWriteCandidate {
	payload := looppkg.FacetPayload{
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversations clarified the operator's preferred collaboration style.",
		Digest:     "The contact prefers direct technical collaboration and explicit source-of-truth boundaries.",
		Full:       "# Relationship context\n\nCurrent synthesis with cited evidence.",
	}
	return documents.DocumentWriteCandidate{
		Path: id.String() + ".md",
		Tags: []string{DossierSubject(id)},
		Body: dossierOutputContract.RenderFacetDocument(payload),
	}
}

func TestValidateDossierWrite(t *testing.T) {
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	tests := []struct {
		name    string
		mutate  func(*documents.DocumentWriteCandidate)
		wantErr string
	}{
		{name: "canonical dossier"},
		{
			name: "nested path",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Path = "people/" + candidate.Path
			},
			wantErr: "top-level",
		},
		{
			name: "noncanonical uuid",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Path = strings.ToUpper(strings.TrimSuffix(candidate.Path, ".md")) + ".md"
			},
			wantErr: "canonical non-zero",
		},
		{
			name: "missing subject",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Tags = []string{"household"}
			},
			wantErr: "must carry exactly one frontmatter tag",
		},
		{
			name: "mismatched subject",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Tags = []string{"contact:019c76e4-2ff1-7918-8d6f-6c2488f5098e"}
			},
			wantErr: "must carry exactly one frontmatter tag",
		},
		{
			name: "additional broad subject",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Tags = append(candidate.Tags, "household")
			},
			wantErr: "no broader subject",
		},
		{
			name: "missing facet",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "## Digest\n\nThe contact prefers direct technical collaboration and explicit source-of-truth boundaries.\n\n", "", 1)
			},
			wantErr: "digest is required",
		},
		{
			name: "noncanonical preamble",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = "stray preamble\n\n" + candidate.Body
			},
			wantErr: "canonical facet section order",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validDossierCandidate(id)
			if tt.mutate != nil {
				tt.mutate(&candidate)
			}
			err := ValidateDossierWrite(candidate)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateDossierWrite() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateDossierWrite() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestDossierIdentityHelpers(t *testing.T) {
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	if got, want := DossierRef(id), "contacts:019c76e4-2ff1-7918-8d6f-6c2488f5098d.md"; got != want {
		t.Fatalf("DossierRef() = %q, want %q", got, want)
	}
	if got, want := DossierSubject(id), "contact:019c76e4-2ff1-7918-8d6f-6c2488f5098d"; got != want {
		t.Fatalf("DossierSubject() = %q, want %q", got, want)
	}
}

func TestWriteDossierOwnsDocumentIdentityAndStructure(t *testing.T) {
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
	tools.ConfigureDossierDocuments(writer.Write)

	args := DossierWriteArgs{
		ContactID:    contact.ID.String(),
		StatusLine:   "Relationship is current and steady.",
		Teaser:       "Recent conversation sharpened the collaboration picture.",
		Digest:       "The contact prefers direct technical collaboration and explicit boundaries.",
		Full:         "### Working style\n\nCurrent synthesis with cited evidence.",
		ReceiptScope: "loop:signal-operator",
	}
	result, err := tools.WriteDossier(context.Background(), args)
	if err != nil {
		t.Fatalf("WriteDossier() error = %v", err)
	}
	if result == "" || writer.calls != 1 {
		t.Fatalf("WriteDossier() result = %q, calls = %d; want one managed write", result, writer.calls)
	}
	if got, want := writer.args.Ref, DossierRef(contact.ID); got != want {
		t.Errorf("write ref = %q, want %q", got, want)
	}
	if got, want := writer.args.Title, contact.FormattedName; got != want {
		t.Errorf("write title = %q, want %q", got, want)
	}
	if len(writer.args.Tags) != 1 || writer.args.Tags[0] != DossierSubject(contact.ID) {
		t.Errorf("write tags = %#v, want canonical private subject", writer.args.Tags)
	}
	if writer.args.Body == nil {
		t.Fatal("write body is nil")
	}
	if got, want := writer.args.ReceiptScope, args.ReceiptScope; got != want {
		t.Errorf("receipt scope = %q, want %q", got, want)
	}
	if err := ValidateDossierWrite(documents.DocumentWriteCandidate{
		Path: strings.TrimPrefix(writer.args.Ref, DossierRootName+":"),
		Tags: writer.args.Tags,
		Body: *writer.args.Body,
	}); err != nil {
		t.Fatalf("Go-authored dossier failed root validator: %v", err)
	}
}

func TestWriteDossierReportsAllProjectionViolationsBeforeWriting(t *testing.T) {
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
	tools.ConfigureDossierDocuments(writer.Write)

	_, err = tools.WriteDossier(context.Background(), DossierWriteArgs{
		ContactID:  contact.ID.String(),
		StatusLine: strings.Repeat("s", 121),
		Teaser:     "Useful hook.",
		Digest:     strings.Repeat("d", 2049),
		Full:       "Complete detail.",
	})
	if err == nil {
		t.Fatal("WriteDossier() accepted invalid projections")
	}
	for _, want := range []string{"correct every listed field", "status_line is 121 characters", "digest is 2049 characters"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("WriteDossier() error = %v, want it to mention %q", err, want)
		}
	}
	if writer.calls != 0 {
		t.Fatalf("invalid dossier reached writer %d times", writer.calls)
	}
}

func TestWriteDossierRequiresCanonicalActiveContact(t *testing.T) {
	tools := newTestTools(t)
	writer := &recordingDossierWriter{}
	tools.ConfigureDossierRoot(true, true)
	tools.ConfigureDossierDocuments(writer.Write)
	validPayload := DossierWriteArgs{
		StatusLine: "Current.",
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       "Complete detail.",
	}

	for _, tt := range []struct {
		name      string
		contactID string
		want      string
	}{
		{name: "noncanonical", contactID: "019C76E4-2FF1-7918-8D6F-6C2488F5098D", want: "canonical non-zero UUID"},
		{name: "missing", contactID: "019c76e4-2ff1-7918-8d6f-6c2488f5098d", want: "not an active structured contact"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := validPayload
			args.ContactID = tt.contactID
			_, err := tools.WriteDossier(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("WriteDossier() error = %v, want %q", err, tt.want)
			}
		})
	}
	if writer.calls != 0 {
		t.Fatalf("invalid contact reached writer %d times", writer.calls)
	}
}
