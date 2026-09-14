package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// TestPublishToolMarksRejectedArguments drives the generated
// publish_output_* handler, which takes every projection as a top-level
// argument, and pins that a refusal reaches the runtime marked with
// exactly the arguments at fault.
func TestPublishToolMarksRejectedArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "over budget",
			args: map[string]any{"status_line": strings.Repeat("s", 121), "teaser": "Open for the hook.", "full": "Complete detail."},
			want: []string{"status_line"},
		},
		{
			name: "a projection left out",
			args: map[string]any{"status_line": "Current.", "full": "Complete detail."},
			want: []string{"teaser"},
		},
		{
			name: "a projection that is not a string",
			args: map[string]any{"status_line": "Current.", "teaser": 5.0, "full": "Complete detail."},
			want: []string{"teaser"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newLoopOutputDocumentStore(t)
			app := &App{documentStore: store, documentTools: documents.NewTools(store)}
			hydrated, err := app.hydrateLoopOutputs(facetedSpec())
			if err != nil {
				t.Fatalf("hydrateLoopOutputs: %v", err)
			}
			publish := findRuntimeTool(t, hydrated, "publish_output_office_status")

			_, err = publish.Handler(context.Background(), tt.args)
			if err == nil {
				t.Fatal("invalid publish succeeded")
			}
			if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments() = %#v, want %#v (error %q)", got, tt.want, err)
			}
		})
	}
}
