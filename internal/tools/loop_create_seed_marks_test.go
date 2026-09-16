package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// TestGuidedCreateSeedRefusalMarksNoArgument pins that a seed refused by
// the facet contract carries no argument marks out of loop_create. The
// projections sit nested under output.initial, so the keys the contract
// marks are not arguments this call sent, and a mark naming one would
// point the runtime at a value the model never passed at the top level.
func TestGuidedCreateSeedRefusalMarksNoArgument(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]any
		wantErr string
	}{
		{
			name:    "over budget status line",
			initial: map[string]any{"status_line": strings.Repeat("x", 121), "digest": "Fine.", "full": "Fine."},
			wantErr: "limit is 120",
		},
		{
			name:    "projection that is not a string",
			initial: map[string]any{"status_line": 5.0, "digest": "Fine.", "full": "Fine."},
			wantErr: "status_line must be a string",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newCurateTestRig(t)
			_, err := rig.tool.Handler(context.Background(), seededArgs(tt.initial))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
			if got := toolargs.RejectedArguments(err); got != nil {
				t.Errorf("RejectedArguments() = %#v, want nil: the projections are nested under output.initial", got)
			}
		})
	}
}

// TestGuidedCreateMigrationRefusalMarksNoArgument is the same pin for a
// contract migration. The migrated projections sit nested under
// output.migration, and a retained one comes from the document rather
// than the call, so neither is an argument loop_create was sent.
func TestGuidedCreateMigrationRefusalMarksNoArgument(t *testing.T) {
	rig := newCurateTestRig(t)
	ctx := context.Background()
	if _, err := rig.tool.Handler(ctx, curateArgs(nil)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	body := "Accumulated closet belief with no projection envelope."
	if _, err := rig.docTools.Write(ctx, documents.WriteArgs{
		Ref:            "kb:dashboards/closet.md",
		Body:           &body,
		StructuredTool: "replace_output_closet_guardian",
	}); err != nil {
		t.Fatalf("simulate loop write: %v", err)
	}

	args := curateArgs(map[string]any{
		"facets":    []any{"status_line"},
		"migration": map[string]any{"status_line": strings.Repeat("x", 121)},
	})
	args["replace"] = true
	_, err := rig.tool.Handler(ctx, args)
	if err == nil || !strings.Contains(err.Error(), "output.migration") || !strings.Contains(err.Error(), "limit is 120") {
		t.Fatalf("error = %v, want an output.migration refusal naming the 120-character limit", err)
	}
	if got := toolargs.RejectedArguments(err); got != nil {
		t.Errorf("RejectedArguments() = %#v, want nil: the projections are nested under output.migration", got)
	}
}
