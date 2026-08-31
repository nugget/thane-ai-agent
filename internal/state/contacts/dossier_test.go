package contacts

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

func validDossierCandidate(id uuid.UUID) documents.DocumentWriteCandidate {
	payload := looppkg.FacetPayload{
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversations clarified the operator's preferred collaboration style.",
		Digest:     "The contact prefers direct technical collaboration and explicit source-of-truth boundaries.",
		Full:       "# Relationship context\n\nCurrent synthesis with cited evidence.",
	}
	return documents.DocumentWriteCandidate{
		Path: id.String() + ".md",
		Tags: []string{DossierSubject(id), "household"},
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
			wantErr: "must carry frontmatter tag",
		},
		{
			name: "mismatched subject",
			mutate: func(candidate *documents.DocumentWriteCandidate) {
				candidate.Tags = []string{"contact:019c76e4-2ff1-7918-8d6f-6c2488f5098e"}
			},
			wantErr: "mismatched contact tag",
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
