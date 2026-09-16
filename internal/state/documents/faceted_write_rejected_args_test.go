package documents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// TestWriteFacetedMarksRejectedArguments pins which arguments a refused
// structured write reports as refused. doc_write and every owning writer
// take the projection keys as top-level arguments, so the contract's marks
// and a domain validator's marks both reach the caller through the shared
// frame; a domain error that marks nothing adds nothing, and a failure that
// is not about the values marks nothing at all.
func TestWriteFacetedMarksRejectedArguments(t *testing.T) {
	t.Parallel()

	statusOnly := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}}
	tests := []struct {
		name  string
		write func(*Tools) error
		want  []string
	}{
		{
			name: "doc_write over budget",
			write: func(tools *Tools) error {
				_, err := tools.Publish(context.Background(), PublishArgs{
					Ref:        "kb:status.md",
					StatusLine: strings.Repeat("s", 200),
					Full:       "Complete detail.",
				})
				return err
			},
			want: []string{"status_line"},
		},
		{
			name: "doc_write missing full",
			write: func(tools *Tools) error {
				_, err := tools.Publish(context.Background(), PublishArgs{Ref: "kb:status.md", StatusLine: "Current."})
				return err
			},
			want: []string{"full"},
		},
		{
			name: "contract and domain violations together",
			write: func(tools *Tools) error {
				_, err := tools.WriteFaceted(context.Background(), FacetedWriteArgs{
					Ref:       "kb:owned.md",
					Contract:  statusOnly,
					Payload:   documentfacets.Payload{StatusLine: strings.Repeat("s", 200), Full: "Complete detail."},
					WriteTool: "publish_output_owned",
					Validate: func(documentfacets.Payload) error {
						return toolargs.Rejected(errors.New("full must cite its source"), "full")
					},
				})
				return err
			},
			want: []string{"full", "status_line"},
		},
		{
			name: "an unmarked domain violation adds nothing",
			write: func(tools *Tools) error {
				_, err := tools.WriteFaceted(context.Background(), FacetedWriteArgs{
					Ref:       "kb:owned.md",
					Contract:  statusOnly,
					Payload:   documentfacets.Payload{StatusLine: "Current.", Full: "Complete detail."},
					WriteTool: "publish_output_owned",
					Validate: func(documentfacets.Payload) error {
						return errors.New("full must cite its source")
					},
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newMutationStore(t)
			err := tt.write(NewTools(store))
			if err == nil {
				t.Fatal("invalid faceted write succeeded")
			}
			if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments() = %#v, want %#v (error %q)", got, tt.want, err)
			}
		})
	}
}

// TestFacetedWriteFailuresThatAreNotAboutValuesMarkNothing covers the two
// refusals whose text names projections but whose call carried none of them
// as arguments, or carried valid ones: an unconfigured store, and a whole
// body write, where every projection arrived inside the body argument.
func TestFacetedWriteFailuresThatAreNotAboutValuesMarkNothing(t *testing.T) {
	t.Parallel()

	t.Run("unconfigured store", func(t *testing.T) {
		t.Parallel()
		var tools *Tools
		_, err := tools.Publish(context.Background(), PublishArgs{Ref: "kb:status.md", StatusLine: "Current.", Full: "Complete detail."})
		if err == nil {
			t.Fatal("Publish() on a nil Tools succeeded")
		}
		if got := toolargs.RejectedArguments(err); got != nil {
			t.Errorf("RejectedArguments() = %#v, want nil for %q", got, err)
		}
	})
	t.Run("whole body write", func(t *testing.T) {
		t.Parallel()
		store, _ := newMutationStore(t)
		contract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}}
		_, err := store.Write(context.Background(), WriteArgs{
			Ref:            "kb:invalid.md",
			Frontmatter:    (documentfacets.Manifest{Contract: contract, ManagedBy: DocumentWriteToolName}).Frontmatter(),
			Body:           stringPtr("## Status Line\n\n" + strings.Repeat("x", 121) + "\n\n## Details\n\nDetail."),
			StructuredTool: DocumentWriteToolName,
		})
		if err == nil || !strings.Contains(err.Error(), "status_line is 121 characters") {
			t.Fatalf("Write() error = %v, want the facet budget failure", err)
		}
		if got := toolargs.RejectedArguments(err); got != nil {
			t.Errorf("RejectedArguments() = %#v, want nil: status_line is inside the body, not an argument", got)
		}
	})
}
