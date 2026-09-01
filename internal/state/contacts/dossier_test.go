package contacts

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

type recordingDossierWriter struct {
	args  documents.FacetedWriteArgs
	calls int
}

func (w *recordingDossierWriter) Write(_ context.Context, args documents.FacetedWriteArgs) (string, error) {
	w.args = args
	w.calls++
	return `{"action":"doc_write","applied":true}`, nil
}

func validDossierCandidate(id uuid.UUID) documents.DocumentWriteCandidate {
	payload := documentfacets.Payload{
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversations clarified the operator's preferred collaboration style.",
		Digest:     "The contact prefers direct technical collaboration and explicit source-of-truth boundaries.",
		Full:       "# Relationship context\n\nCurrent synthesis — evidence: archive:session:019c52f0-9ce8-7708-867f-35da2e6b4777.",
	}
	candidate := documents.DocumentWriteCandidate{
		Path: id.String() + ".md",
		Tags: []string{DossierSubject(id)},
		Frontmatter: map[string][]string{
			"title": {"Dossier Person"},
		},
		Body: dossierOutputContract.Render(payload),
	}
	for key, values := range (documentfacets.Manifest{Contract: dossierOutputContract, ManagedBy: DossierWriteToolName}).Frontmatter() {
		candidate.Frontmatter[key] = values
	}
	return candidate
}

func dossierValidatorForName(name string) documents.RootWriteValidator {
	return NewDossierWriteValidator(func(uuid.UUID) (string, error) {
		return name, nil
	})
}

func TestValidateDossierWrite(t *testing.T) {
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	validator := dossierValidatorForName("Dossier Person")
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
			name: "subject uuid repeated in prose",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "Relationship is current and steady.", "Contact UUID "+id.String()+" is current.", 1)
			},
			wantErr: "Go already binds the document path and frontmatter",
		},
		{
			name: "uppercase subject uuid repeated in prose",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "explicit source-of-truth boundaries.", "contact "+strings.ToUpper(id.String())+".", 1)
			},
			wantErr: "Go already binds the document path and frontmatter",
		},
		{
			name: "related contact uuid remains valid",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "Current synthesis", "Related contact:01a055fc-6b4a-7c67-8c9b-f121b9814c45. Current synthesis", 1)
			},
		},
		{
			name: "subject name in status line",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "Relationship is current and steady.", "Dossier Person has a current and steady relationship.", 1)
			},
			wantErr: "projection(s) [status_line] repeat the subject contact name",
		},
		{
			name: "spoofed title cannot hide subject name",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Frontmatter["title"] = []string{"Someone Else"}
				candidate.Body = strings.Replace(candidate.Body, "Relationship is current and steady.", "Dossier Person has a current and steady relationship.", 1)
			},
			wantErr: "projection(s) [status_line] repeat the subject contact name",
		},
		{
			name: "missing canonical title",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				delete(candidate.Frontmatter, "title")
			},
			wantErr: `must carry exactly one title "Dossier Person"`,
		},
		{
			name: "overridden canonical title",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Frontmatter["title"] = []string{"Someone Else"}
			},
			wantErr: `must carry exactly one title "Dossier Person"`,
		},
		{
			name: "case-insensitive subject name in teaser",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "Recent conversations", "Recent conversations with dossier person", 1)
			},
			wantErr: "projection(s) [teaser] repeat the subject contact name",
		},
		{
			name: "subject name remains valid in standalone prose",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "The contact prefers", "Dossier Person prefers", 1)
				candidate.Body = strings.Replace(candidate.Body, "Current synthesis", "Dossier Person's current synthesis", 1)
			},
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
		{
			name: "short archive session citation",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "archive:session:019c52f0-9ce8-7708-867f-35da2e6b4777", "archive:session:019c52f0", 1)
			},
			wantErr: "full canonical UUID",
		},
		{
			name: "legacy archive session separator",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "archive:session:", "archive:session-", 1)
			},
			wantErr: "archive:session:<full-session-uuid>",
		},
		{
			name: "noncanonical archive session uuid",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Body = strings.Replace(candidate.Body, "019c52f0-9ce8-7708-867f-35da2e6b4777", "019C52F0-9CE8-7708-867F-35DA2E6B4777", 1)
			},
			wantErr: "full canonical UUID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validDossierCandidate(id)
			if tt.mutate != nil {
				tt.mutate(&candidate)
			}
			err := validator(candidate)
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

func TestWriteDossierRejectsSubjectUUIDInEveryProjection(t *testing.T) {
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

	id := contact.ID.String()
	_, err = tools.WriteDossier(context.Background(), DossierWriteArgs{
		ContactID:  id,
		StatusLine: "Current relationship for contact " + id + ".",
		Teaser:     "Open the canonical dossier contacts:" + id + ".md.",
		Digest:     "Structured identity is contact:" + id + ".",
		Full:       "### Subject\n\nContact UUID `" + id + "`.",
	})
	if err == nil {
		t.Fatal("WriteDossier() accepted its subject UUID in authored projections")
	}
	for _, want := range []string{
		"correct every listed field",
		"projection(s) [status_line, teaser, digest, full]",
		id,
		"Go already binds the document path and frontmatter",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("WriteDossier() error = %v, want it to mention %q", err, want)
		}
	}
	if writer.calls != 0 {
		t.Fatalf("redundant subject identity reached writer %d times", writer.calls)
	}
}

func TestWriteDossierRejectsSubjectNameInCompactProjections(t *testing.T) {
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
		StatusLine: "Dossier Person has a current and steady relationship.",
		Teaser:     "Open for the latest context about dossier person.",
		Digest:     "Dossier Person prefers direct technical collaboration.",
		Full:       "### Relationship context\n\nDossier Person's current synthesis.",
	})
	if err == nil {
		t.Fatal("WriteDossier() accepted the subject name in compact projections")
	}
	for _, want := range []string{
		"correct every listed field",
		"projection(s) [status_line, teaser]",
		`subject contact name "Dossier Person"`,
		"structured contact and dossier title already identify the subject",
		"digest and full may use it",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("WriteDossier() error = %v, want it to mention %q", err, want)
		}
	}
	if writer.calls != 0 {
		t.Fatalf("redundant subject name reached writer %d times", writer.calls)
	}
}

func TestWriteDossierAllowsSubjectNameInStandaloneProjections(t *testing.T) {
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
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Open for the latest collaboration context.",
		Digest:     "Dossier Person prefers direct technical collaboration.",
		Full:       "### Relationship context\n\nDossier Person's current synthesis.",
	})
	if err != nil {
		t.Fatalf("WriteDossier() rejected subject name in digest/full: %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
}

func TestContainsFoldedPhraseUsesNameBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		text   string
		phrase string
		want   bool
	}{
		{name: "exact", text: "Ed is a collaborator.", phrase: "Ed", want: true},
		{name: "case insensitive", text: "Working with ED's team.", phrase: "Ed", want: true},
		{name: "not inside word", text: "Shared context is current.", phrase: "Ed", want: false},
		{name: "unicode name", text: "ÉLODIE's preferences are current.", phrase: "Élodie", want: true},
		{name: "unicode simple fold", text: "Working with ος.", phrase: "ΟΣ", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsFoldedPhrase(tt.text, tt.phrase); got != tt.want {
				t.Fatalf("containsFoldedPhrase(%q, %q) = %v, want %v", tt.text, tt.phrase, got, tt.want)
			}
		})
	}
}

func TestWriteDossierRejectsEveryAmbiguousArchiveSessionCitation(t *testing.T) {
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
		StatusLine: "Current. — evidence: archive:session:019c1111",
		Teaser:     "Useful hook. — evidence: archive:session:019c2222",
		Digest:     "Enough context to act. — evidence: archive:session:019c3333",
		Full: "Two claims. — evidence: archive:session:019c52f0\n" +
			"Another claim. — evidence: archive:session:01a056ae\n" +
			"Repeated first claim. — evidence: archive:session:019c52f0",
	})
	if err == nil {
		t.Fatal("WriteDossier() accepted ambiguous archive session citations")
	}
	for _, want := range []string{
		"correct every listed field",
		"status_line=archive:session:019c1111",
		"teaser=archive:session:019c2222",
		"digest=archive:session:019c3333",
		"full=archive:session:019c52f0",
		"full=archive:session:01a056ae",
		"archive:session:<full-session-uuid>",
		"short prefixes can be ambiguous",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("WriteDossier() error = %v, want it to mention %q", err, want)
		}
	}
	if strings.Count(err.Error(), "archive:session:019c52f0") != 1 {
		t.Errorf("WriteDossier() error = %v, want duplicate invalid citation reported once", err)
	}
	if writer.calls != 0 {
		t.Fatalf("ambiguous citations reached writer %d times", writer.calls)
	}
}

func TestValidateDossierWriteReportsFacetAndEvidenceViolationsTogether(t *testing.T) {
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	candidate := validDossierCandidate(id)
	candidate.Body = dossierOutputContract.Render(documentfacets.Payload{
		StatusLine: strings.Repeat("s", 121),
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       "Complete detail. — evidence: archive:session:019c52f0",
	})

	err := dossierValidatorForName("Dossier Person")(candidate)
	if err == nil {
		t.Fatal("ValidateDossierWrite() accepted invalid projections")
	}
	for _, want := range []string{
		"correct every listed field",
		"facet contract",
		"status_line is 121 characters",
		"evidence contract",
		"full=archive:session:019c52f0",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateDossierWrite() error = %v, want it to mention %q", err, want)
		}
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
	tools.ConfigureDossierDocuments(nil, writer.Write)

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
	if got, want := writer.args.ReceiptScope, args.ReceiptScope; got != want {
		t.Errorf("receipt scope = %q, want %q", got, want)
	}
	if got, want := writer.args.WriteTool, DossierWriteToolName; got != want {
		t.Errorf("write tool = %q, want %q", got, want)
	}
	frontmatter := map[string][]string{"title": {writer.args.Title}}
	for key, values := range writer.args.Frontmatter {
		frontmatter[key] = append([]string(nil), values...)
	}
	for key, values := range (documentfacets.Manifest{Contract: writer.args.Contract, ManagedBy: writer.args.WriteTool}).Frontmatter() {
		frontmatter[key] = values
	}
	if err := dossierValidatorForName(contact.FormattedName)(documents.DocumentWriteCandidate{
		Path:        strings.TrimPrefix(writer.args.Ref, DossierRootName+":"),
		Tags:        writer.args.Tags,
		Frontmatter: frontmatter,
		Body:        writer.args.Contract.Render(writer.args.Payload),
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
	tools.ConfigureDossierDocuments(nil, writer.Write)

	_, err = tools.WriteDossier(context.Background(), DossierWriteArgs{
		ContactID:  contact.ID.String(),
		StatusLine: strings.Repeat("s", 121),
		Teaser:     "Useful hook.",
		Digest:     strings.Repeat("d", 2049),
		Full:       "Complete detail. — evidence: archive:session:019c52f0",
	})
	if err == nil {
		t.Fatal("WriteDossier() accepted invalid projections")
	}
	for _, want := range []string{
		"correct every listed field",
		"status_line is 121 characters",
		"digest is 2049 characters",
		"archive:session:019c52f0",
		"full canonical UUID",
	} {
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
	tools.ConfigureDossierDocuments(nil, writer.Write)
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
