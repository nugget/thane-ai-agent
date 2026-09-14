package contacts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// TestDossierRejectionMarksRejectedArguments pins which
// contact_dossier_write arguments each refusal reports as refused: the
// facet contract's violations and every dossier rule mark the projections
// that broke them, and a failure that is not about the values — the
// document writer failing underneath — marks nothing.
func TestDossierRejectionMarksRejectedArguments(t *testing.T) {
	const name = "Dossier Person"
	valid := DossierWriteArgs{
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversations clarified the operator's preferred collaboration style.",
		Digest:     "The contact prefers direct technical collaboration and explicit source-of-truth boundaries.",
		Full:       "# Relationship context\n\nCurrent synthesis — evidence: archive:session:019c52f0-9ce8-7708-867f-35da2e6b4777.",
	}
	writerFailed := func(context.Context, documents.FacetedWriteArgs) (string, error) {
		return "", errors.New("write contacts:dossier.md through root policy: disk full while writing status_line")
	}
	tests := []struct {
		name   string
		mutate func(args *DossierWriteArgs, id uuid.UUID)
		write  func(context.Context, documents.FacetedWriteArgs) (string, error)
		want   []string
	}{
		{
			name:   "facet contract over budget",
			mutate: func(a *DossierWriteArgs, _ uuid.UUID) { a.StatusLine = strings.Repeat("s", 121) },
			want:   []string{"status_line"},
		},
		{
			name: "subject name in the compact projections",
			mutate: func(a *DossierWriteArgs, _ uuid.UUID) {
				a.StatusLine = name + " is steady."
				a.Teaser = "Why " + name + " matters right now."
			},
			want: []string{"status_line", "teaser"},
		},
		{
			name:   "subject UUID in the digest",
			mutate: func(a *DossierWriteArgs, id uuid.UUID) { a.Digest = "Bound to contact:" + id.String() + "." },
			want:   []string{"digest"},
		},
		{
			name:   "short archive citation in full",
			mutate: func(a *DossierWriteArgs, _ uuid.UUID) { a.Full = "Evidence: archive:session:019c52f0." },
			want:   []string{"full"},
		},
		{
			name: "contract and dossier rules together",
			mutate: func(a *DossierWriteArgs, id uuid.UUID) {
				a.StatusLine = strings.Repeat("s", 121)
				a.Digest = "Bound to contact:" + id.String() + "."
			},
			want: []string{"digest", "status_line"},
		},
		{
			name:   "the document writer failing underneath",
			mutate: func(*DossierWriteArgs, uuid.UUID) {},
			write:  writerFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := newTestTools(t)
			if _, err := tools.SaveContact(`{"name":"` + name + `","kind":"individual"}`); err != nil {
				t.Fatal(err)
			}
			contact, err := tools.store.FindByName(name)
			if err != nil {
				t.Fatal(err)
			}
			write := tt.write
			if write == nil {
				write = (&recordingDossierWriter{}).Write
			}
			tools.ConfigureDossierRoot(true, true)
			tools.ConfigureDossierDocuments(nil, write)

			args := valid
			args.ContactID = contact.ID.String()
			tt.mutate(&args, contact.ID)
			_, err = tools.WriteDossier(context.Background(), args)
			if err == nil {
				t.Fatal("WriteDossier() succeeded")
			}
			if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments() = %#v, want %#v (error %q)", got, tt.want, err)
			}
		})
	}
}

// TestDossierBackstopMarksRejectedArguments covers the contacts-root
// validator, which contact_dossier_write's own writes pass through: its
// projection rules mark the projection at fault, while a violation of what
// Go derives (the title) names no argument the model sent.
func TestDossierBackstopMarksRejectedArguments(t *testing.T) {
	id := uuid.MustParse("019c76e4-2ff1-7918-8d6f-6c2488f5098d")
	validPayload := documentfacets.Payload{
		StatusLine: "Relationship is current and steady.",
		Teaser:     "Recent conversations clarified the operator's preferred collaboration style.",
		Digest:     "The contact prefers direct technical collaboration and explicit source-of-truth boundaries.",
		Full:       "Complete detail.",
	}
	tests := []struct {
		name   string
		mutate func(*documents.DocumentWriteCandidate)
		want   []string
	}{
		{
			name: "facet contract over budget",
			mutate: func(c *documents.DocumentWriteCandidate) {
				payload := validPayload
				payload.StatusLine = strings.Repeat("s", 121)
				c.Body = dossierOutputContract.Render(payload)
			},
			want: []string{"status_line"},
		},
		{
			name: "subject name in the teaser",
			mutate: func(c *documents.DocumentWriteCandidate) {
				payload := validPayload
				payload.Teaser = "Dossier Person clarified the collaboration style."
				c.Body = dossierOutputContract.Render(payload)
			},
			want: []string{"teaser"},
		},
		{
			name:   "a title that does not match the contact",
			mutate: func(c *documents.DocumentWriteCandidate) { c.Frontmatter["title"] = []string{"Someone Else"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validDossierCandidate(id)
			tt.mutate(&candidate)
			err := dossierValidatorForName("Dossier Person")(candidate)
			if err == nil {
				t.Fatal("backstop accepted the candidate")
			}
			if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments() = %#v, want %#v (error %q)", got, tt.want, err)
			}
		})
	}
}
