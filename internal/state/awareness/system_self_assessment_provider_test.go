package awareness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// TestSystemSelfAssessmentProvider pins the provider's contract: the
// status_line facet renders with a delta-formatted age, and every
// degraded state — unfaceted document, empty verdict, read failure —
// is quiet rather than a placeholder or a surfaced error.
func TestSystemSelfAssessmentProvider(t *testing.T) {
	t.Parallel()

	faceted := "## Status Line\n\npanel clean, baselines steady\n\n## Digest\n\nNo open concerns.\n\n## Details\n\nworking memory here\n"
	writtenAt := time.Now().Add(-2 * time.Hour)

	cases := []struct {
		name     string
		body     string
		at       time.Time
		err      error
		contains []string
		empty    bool
	}{
		{
			name:     "verdict renders with age",
			body:     faceted,
			at:       writtenAt,
			contains: []string{"System Self-Assessment", "panel clean, baselines steady", "age_delta=-"},
		},
		{
			name:  "unfaceted document is quiet",
			body:  "# Metacognitive State\n\nbaselines and concerns, pre-facet shape\n",
			at:    writtenAt,
			empty: true,
		},
		{
			name:  "empty status line is quiet",
			body:  "## Status Line\n\n\n## Digest\n\nsomething\n\n## Details\n\nbody\n",
			at:    writtenAt,
			empty: true,
		},
		{
			name:  "read failure is quiet, not an error",
			err:   errors.New("smb mount gone"),
			empty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSystemSelfAssessmentProvider(func(context.Context) (string, time.Time, error) {
				return tc.body, tc.at, tc.err
			}, nil)
			out, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
			if err != nil {
				t.Fatalf("TagContext: %v", err)
			}
			if tc.empty {
				if out != "" {
					t.Fatalf("want quiet, got: %q", out)
				}
				return
			}
			for _, want := range tc.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q: %q", want, out)
				}
			}
		})
	}
}
