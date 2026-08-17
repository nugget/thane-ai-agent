package awareness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestSystemSelfAssessmentProvider pins the provider's contract: the
// signal facet renders with a delta-formatted age, and every
// degraded state — unfaceted document, empty verdict, read failure —
// is quiet rather than a placeholder or a surfaced error.
func TestSystemSelfAssessmentProvider(t *testing.T) {
	t.Parallel()

	faceted := "## Signal\n\npanel clean, baselines steady\n\n## Digest\n\nNo open concerns.\n\n## Details\n\nworking memory here\n"
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
			name:  "empty signal is quiet",
			body:  "## Signal\n\n\n## Digest\n\nsomething\n\n## Details\n\nbody\n",
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

func TestSystemSelfAssessmentProviderAdvertisesSignalBeforeReading(t *testing.T) {
	t.Parallel()

	reads := 0
	p := NewSystemSelfAssessmentProvider(func(context.Context) (string, time.Time, error) {
		reads++
		return "## Signal\n\npanel clean\n\n## Details\n\nbody\n", time.Now(), nil
	}, nil)
	ads, err := p.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("ContextAdvertisements: %v", err)
	}
	if reads != 0 {
		t.Fatalf("advertising performed %d document reads, want 0", reads)
	}
	if len(ads) != 1 {
		t.Fatalf("advertisements = %#v, want one", ads)
	}
	if err := ads[0].Validate(); err != nil {
		t.Fatalf("advertisement invalid: %v", err)
	}
	projection := ads[0].Projections[0]
	if projection.Name != string(looppkg.OutputFacetSignal) || projection.Role != agentctx.ContextRoleSignal {
		t.Fatalf("projection = %#v, want signal role", projection)
	}
	out, err := p.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
		Advertisement: ads[0],
		Projection:    projection,
	})
	if err != nil {
		t.Fatalf("MaterializeContextAdvertisement: %v", err)
	}
	if reads != 1 || !strings.Contains(out, "panel clean") {
		t.Fatalf("materialized output = %q after %d reads", out, reads)
	}
}
