package documents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// TestWriteFacetedRejectionSaysNothingWasWritten covers both violation
// sources the shared writer folds into one error — the facet contract and an
// owner's domain validator — and pins that the frame reports a write that
// did not land and sets no retry count.
func TestWriteFacetedRejectionSaysNothingWasWritten(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		file  string
		write func(*Tools) error
		want  string
	}{
		{
			name: "contract violation through doc_write",
			file: "status.md",
			write: func(tools *Tools) error {
				_, err := tools.Publish(context.Background(), PublishArgs{
					Ref:        "kb:status.md",
					StatusLine: strings.Repeat("s", 200),
					Full:       "Complete detail.",
				})
				return err
			},
			want: "status_line is 200 characters",
		},
		{
			name: "domain violation through an owning writer",
			file: "owned.md",
			write: func(tools *Tools) error {
				_, err := tools.WriteFaceted(context.Background(), FacetedWriteArgs{
					Ref:       "kb:owned.md",
					Contract:  documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine}}},
					Payload:   documentfacets.Payload{StatusLine: "Current.", Full: "Complete detail."},
					WriteTool: "publish_output_owned",
					Validate: func(documentfacets.Payload) error {
						return errors.New("full must cite its source")
					},
				})
				return err
			},
			want: "full must cite its source",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, kbDir := newMutationStore(t)
			err := tt.write(NewTools(store))
			if err == nil {
				t.Fatal("invalid faceted write succeeded")
			}
			for _, want := range []string{"projections are invalid and nothing was written", "correct every listed field", tt.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "retry once") {
				t.Errorf("error = %q, must not cap retries", err)
			}
			if _, statErr := os.Stat(filepath.Join(kbDir, tt.file)); !os.IsNotExist(statErr) {
				t.Fatalf("rejected write left %s on disk (stat err %v)", tt.file, statErr)
			}
		})
	}
}
