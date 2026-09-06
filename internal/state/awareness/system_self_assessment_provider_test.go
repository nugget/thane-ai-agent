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
			}, nil, nil)
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
		return "## Status Line\n\npanel clean\n\n## Details\n\nbody\n", time.Now(), nil
	}, nil, nil)
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
	if projection.Name != string(looppkg.OutputFacetStatusLine) || projection.Role != agentctx.ContextRoleSignal {
		t.Fatalf("projection = %#v, want status_line signal", projection)
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

// TestSystemSelfAssessmentProviderExpandsTemporalTemplates pins this
// provider as a reader surface. metacog is told to write
// "{{delta:2026-09-18}}" rather than a literal delta, so without
// expansion the one line in the system that is a judgment about the
// whole system reaches the model as braces — while the same facet
// served over GET /v1/loops/{name}/outputs/{output} renders correctly.
func TestSystemSelfAssessmentProviderExpandsTemporalTemplates(t *testing.T) {
	t.Parallel()

	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	body := "## Status Line\n\nfreeze window opens {{delta:2026-09-08}}\n\n## Details\n\nbody\n"
	p := NewSystemSelfAssessmentProvider(func(context.Context) (string, time.Time, error) {
		return body, time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC), nil
	}, chicago, nil)
	p.now = func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }

	out, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Contains(out, "{{delta:") {
		t.Errorf("verdict reached the model unexpanded: %q", out)
	}
	if !strings.Contains(out, "freeze window opens +2d") {
		t.Errorf("want the expanded verdict, got: %q", out)
	}
}

// TestSystemSelfAssessmentProviderExpandsTheCachedVerdictFreshly pins
// where expansion happens. The cache exists so a failed read still
// serves the last good verdict wearing its honest age; expanding at
// cache time instead of render time would freeze the delta at the
// moment of the last successful read, so the block would claim
// freshness in the template while the age stamp beside it said
// otherwise.
func TestSystemSelfAssessmentProviderExpandsTheCachedVerdictFreshly(t *testing.T) {
	t.Parallel()

	fail := false
	updatedAt := time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC)
	p := NewSystemSelfAssessmentProvider(func(context.Context) (string, time.Time, error) {
		if fail {
			return "", time.Time{}, errors.New("substrate down")
		}
		return "## Status Line\n\nfreeze window opens {{delta:2026-09-08}}\n\n## Details\n\nbody\n", updatedAt, nil
	}, time.UTC, nil)

	p.now = func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }
	first, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext (live): %v", err)
	}
	if !strings.Contains(first, "opens +2d") {
		t.Fatalf("live read: want +2d, got %q", first)
	}

	// Two days later the read fails, so the cached verdict is served.
	// Its template must render against now, not against the instant it
	// was cached at.
	fail = true
	p.now = func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) }
	cached, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext (cached): %v", err)
	}
	if !strings.Contains(cached, "opens today") {
		t.Errorf("cached verdict served a stale delta: %q", cached)
	}
	if !strings.Contains(cached, "cached, live read failed") {
		t.Errorf("cached verdict lost its marker: %q", cached)
	}
}

// TestSystemSelfAssessmentProviderExpandsInTheHouseholdZone pins which
// day the verdict means. The provider's clock is the process clock, so
// without the household zone a verdict renders the household's today
// as yesterday for the last hours of every local evening.
func TestSystemSelfAssessmentProviderExpandsInTheHouseholdZone(t *testing.T) {
	t.Parallel()

	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	// 03:00 UTC on the 7th is 22:00 on the 6th in Chicago.
	now := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)
	body := "## Status Line\n\nbackup verified {{delta:2026-09-06}}\n\n## Details\n\nbody\n"

	render := func(zone *time.Location) string {
		t.Helper()
		p := NewSystemSelfAssessmentProvider(func(context.Context) (string, time.Time, error) {
			return body, now, nil
		}, zone, nil)
		p.now = func() time.Time { return now }
		out, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		return out
	}

	if got := render(chicago); !strings.Contains(got, "verified today") {
		t.Errorf("household zone should render today: %q", got)
	}
	if got := render(time.UTC); !strings.Contains(got, "verified yesterday") {
		t.Errorf("UTC should render yesterday — the fixture is not proving anything otherwise: %q", got)
	}
}
