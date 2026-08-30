package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

type testContextAdvertiser struct {
	advertisements []agentctx.ContextAdvertisement
	content        map[string]string
	legacyCalls    int
	materialized   []string
}

func (p *testContextAdvertiser) TagContext(context.Context, agentctx.ContextRequest) (string, error) {
	p.legacyCalls++
	return "LEGACY_ADVERTISER_PATH", nil
}

func (p *testContextAdvertiser) ContextAdvertisements(context.Context, agentctx.ContextRequest) ([]agentctx.ContextAdvertisement, error) {
	return append([]agentctx.ContextAdvertisement(nil), p.advertisements...), nil
}

func (p *testContextAdvertiser) MaterializeContextAdvertisement(_ context.Context, _ agentctx.ContextRequest, selection agentctx.ContextSelection) (string, error) {
	key := selection.Advertisement.Source + "/" + selection.Advertisement.ID + "/" + selection.Projection.Name
	p.materialized = append(p.materialized, key)
	return p.content[key], nil
}

func testAdvertisement(source, id string, match agentctx.ContextMatchKind, strength float64, projections ...agentctx.ContextProjection) agentctx.ContextAdvertisement {
	return agentctx.ContextAdvertisement{
		ID:          id,
		Source:      source,
		Kind:        "test",
		Bucket:      agentctx.ContextBucketRelated,
		Summary:     "test advertisement " + id,
		Matches:     []agentctx.ContextMatchSignal{{Kind: match, Strength: strength}},
		Projections: projections,
	}
}

func testProjection(name string, role agentctx.ContextProjectionRole, bytes int) agentctx.ContextProjection {
	return agentctx.ContextProjection{Name: name, Role: role, Format: "text/markdown", EstimatedBytes: bytes}
}

func TestSelectContextAdvertisementsRanksEvidenceDeterministically(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	ads := []agentctx.ContextAdvertisement{
		testAdvertisement("memory", "ambient", agentctx.ContextMatchAmbient, 1, testProjection("signal", agentctx.ContextRoleSignal, 100)),
		testAdvertisement("memory", "semantic", agentctx.ContextMatchSemantic, 1, testProjection("digest", agentctx.ContextRoleContext, 100)),
		testAdvertisement("memory", "exact", agentctx.ContextMatchExactSubject, 0.1, testProjection("digest", agentctx.ContextRoleContext, 100)),
	}

	orders := [][]agentctx.ContextAdvertisement{ads, {ads[2], ads[0], ads[1]}}
	for i, order := range orders {
		candidates := make([]contextAdvertisementCandidate, 0, len(order))
		for _, ad := range order {
			candidates = append(candidates, contextAdvertisementCandidate{advertiser: provider, advertisement: ad})
		}
		selected, _ := selectContextAdvertisements(candidates)
		if len(selected) != 3 {
			t.Fatalf("order %d selected %d advertisements, want 3", i, len(selected))
		}
		got := []string{
			selected[0].selection.Advertisement.ID,
			selected[1].selection.Advertisement.ID,
			selected[2].selection.Advertisement.ID,
		}
		if strings.Join(got, ",") != "exact,semantic,ambient" {
			t.Fatalf("order %d selection = %v, want [exact semantic ambient]", i, got)
		}
	}
}

func TestSelectContextAdvertisementsChoosesProjectionForMatchAndBudget(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	tests := []struct {
		name  string
		ad    agentctx.ContextAdvertisement
		want  string
		empty bool
	}{
		{
			name: "request match gets actionable context",
			ad: testAdvertisement("docs", "one", agentctx.ContextMatchSemantic, 0.8,
				testProjection("signal", agentctx.ContextRoleSignal, 100),
				testProjection("digest", agentctx.ContextRoleContext, 1000),
				testProjection("full", agentctx.ContextRoleDetail, 2000)),
			want: "digest",
		},
		{
			name: "ambient match gets smallest signal",
			ad: testAdvertisement("docs", "one", agentctx.ContextMatchAmbient, 1,
				testProjection("teaser", agentctx.ContextRoleSignal, 500),
				testProjection("status_line", agentctx.ContextRoleSignal, 120),
				testProjection("digest", agentctx.ContextRoleContext, 1000)),
			want: "status_line",
		},
		{
			name: "request match gets roomiest signal when no digest exists",
			ad: testAdvertisement("docs", "one", agentctx.ContextMatchLexical, 1,
				testProjection("status_line", agentctx.ContextRoleSignal, 120),
				testProjection("teaser", agentctx.ContextRoleSignal, 500)),
			want: "teaser",
		},
		{
			name: "oversized context falls back to signal",
			ad: testAdvertisement("docs", "one", agentctx.ContextMatchExactSubject, 1,
				testProjection("signal", agentctx.ContextRoleSignal, 200),
				testProjection("digest", agentctx.ContextRoleContext, maxAdvertisedContextBytes+1)),
			want: "signal",
		},
		{
			name:  "detail is never automatic",
			ad:    testAdvertisement("docs", "one", agentctx.ContextMatchExactSubject, 1, testProjection("full", agentctx.ContextRoleDetail, 100)),
			empty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected, _ := selectContextAdvertisements([]contextAdvertisementCandidate{{advertiser: provider, advertisement: tt.ad}})
			if tt.empty {
				if len(selected) != 0 {
					t.Fatalf("selected = %#v, want none", selected)
				}
				return
			}
			if len(selected) != 1 || selected[0].selection.Projection.Name != tt.want {
				t.Fatalf("selected = %#v, want projection %q", selected, tt.want)
			}
		})
	}
}

func TestSelectContextAdvertisementsDeduplicatesAndLimits(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	duplicate := testAdvertisement("archive", "same", agentctx.ContextMatchExactSubject, 1, testProjection("signal", agentctx.ContextRoleSignal, 100))
	candidates := []contextAdvertisementCandidate{
		{advertiser: provider, advertisement: duplicate},
		{advertiser: provider, advertisement: duplicate},
	}
	for i := 0; i < maxSelectedContextAdvertisements+3; i++ {
		ad := testAdvertisement("archive", "subject-"+string(rune('a'+i)), agentctx.ContextMatchLexical, 0.5, testProjection("signal", agentctx.ContextRoleSignal, 100))
		candidates = append(candidates, contextAdvertisementCandidate{advertiser: provider, advertisement: ad})
	}

	selected, _ := selectContextAdvertisements(candidates)
	if len(selected) != maxSelectedContextAdvertisements {
		t.Fatalf("selected %d advertisements, want cap %d", len(selected), maxSelectedContextAdvertisements)
	}
	seenSame := 0
	for _, item := range selected {
		if item.selection.Advertisement.ID == "same" {
			seenSame++
		}
	}
	if seenSame != 1 {
		t.Fatalf("duplicate selected %d times, want once", seenSame)
	}
}

func TestTagContextAssemblerUsesAdvertisementPathAndPrependsSelection(t *testing.T) {
	provider := &testContextAdvertiser{}
	provider.advertisements = []agentctx.ContextAdvertisement{
		testAdvertisement("metacognition", "self", agentctx.ContextMatchAmbient, 1, testProjection("status_line", agentctx.ContextRoleSignal, 256)),
	}
	provider.advertisements[0].Bucket = agentctx.ContextBucketLiveState
	provider.content = map[string]string{"metacognition/self/status_line": "SELECTED_SELF_SIGNAL"}

	assembler := NewTagContextAssembler(TagContextAssemblerConfig{})
	assembler.RegisterAlwaysProvider(&mockTagProvider{
		content: "LEGACY_LIVE_STATE" + strings.Repeat("L", maxTagContextBytes),
		bucket:  agentctx.ContextBucketLiveState,
	})
	assembler.RegisterAlwaysProvider(provider)
	sections := assembler.BuildSections(context.Background(), agentctx.ContextRequest{IncludeAlways: true})

	var live string
	for _, section := range sections {
		if section.Bucket == agentctx.ContextBucketLiveState {
			live = section.Content
			break
		}
	}
	if live == "" {
		t.Fatal("Live State section missing")
	}
	if signal, legacy := strings.Index(live, "SELECTED_SELF_SIGNAL"), strings.Index(live, "LEGACY_LIVE_STATE"); signal < 0 || legacy < 0 || signal > legacy {
		t.Fatalf("advertised signal should precede eager context:\n%s", live)
	}
	if !strings.Contains(live, "Live State truncated: exceeded 64 KB bucket limit") {
		t.Fatal("selected signal should survive while oversized eager context remains honestly truncated")
	}
	if provider.legacyCalls != 0 {
		t.Fatalf("legacy TagContext called %d times, want 0", provider.legacyCalls)
	}
	if len(provider.materialized) != 1 || provider.materialized[0] != "metacognition/self/status_line" {
		t.Fatalf("materialized = %v, want selected status_line only", provider.materialized)
	}
}

func TestSelectContextAdvertisementsDuplicateWithoutProjectionDoesNotSuppressLaterClaimant(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	// Two claimants of one identity. The higher-ranked one offers only
	// detail, which automatic selection never chooses; the lower-ranked one
	// carries a selectable signal. Marking the identity seen on the empty
	// claimant would suppress the usable one and select nothing at all.
	empty := testAdvertisement("memory", "same", agentctx.ContextMatchExactSubject, 1,
		testProjection("full", agentctx.ContextRoleDetail, 100))
	usable := testAdvertisement("memory", "same", agentctx.ContextMatchAmbient, 0.5,
		testProjection("signal", agentctx.ContextRoleSignal, 100))

	selected, _ := selectContextAdvertisements([]contextAdvertisementCandidate{
		{advertiser: provider, advertisement: empty},
		{advertiser: provider, advertisement: usable},
	})

	if len(selected) != 1 {
		t.Fatalf("selected %d advertisements, want 1", len(selected))
	}
	if got := selected[0].selection.Projection.Name; got != "signal" {
		t.Fatalf("selected projection %q, want the later claimant's signal", got)
	}
}

func TestMaterializeDropsPayloadExceedingItsOwnEstimate(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	over := testAdvertisement("memory", "overrun", agentctx.ContextMatchExactSubject, 1,
		testProjection("digest", agentctx.ContextRoleContext, 64))
	honest := testAdvertisement("memory", "honest", agentctx.ContextMatchSemantic, 1,
		testProjection("digest", agentctx.ContextRoleContext, 64))
	provider.advertisements = []agentctx.ContextAdvertisement{over, honest}
	provider.content = map[string]string{
		// Selection reserved 64 bytes for each; the overrun delivers far
		// more. Admitting it would spend capacity promised to later
		// winners and make their fate order-dependent.
		"memory/overrun/digest": strings.Repeat("x", 4096),
		"memory/honest/digest":  "fits fine",
	}

	assembler := NewTagContextAssembler(TagContextAssemblerConfig{})
	buckets := assembler.materializeContextAdvertisements(context.Background(), agentctx.ContextRequest{}, []contextAdvertisementCandidate{
		{advertiser: provider, advertisement: over},
		{advertiser: provider, advertisement: honest},
	})

	rendered := buckets[agentctx.ContextBucketRelated]
	if strings.Contains(rendered, "xxxx") {
		t.Fatalf("over-estimate payload must be dropped, got: %.80s", rendered)
	}
	if !strings.Contains(rendered, "fits fine") {
		t.Fatalf("honest payload should survive, got: %q", rendered)
	}
}

func TestSelectContextAdvertisementsCountsWithheldOffers(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	// Twelve genuinely selectable offers against a cap of eight: four are
	// withheld and the count must say so. A thirteenth offers only detail —
	// never selectable, so it is a non-offer, not a withheld one.
	var candidates []contextAdvertisementCandidate
	for i := 0; i < 12; i++ {
		ad := testAdvertisement("memory", fmt.Sprintf("doc-%02d", i), agentctx.ContextMatchSemantic, 1,
			testProjection("digest", agentctx.ContextRoleContext, 64))
		candidates = append(candidates, contextAdvertisementCandidate{advertiser: provider, advertisement: ad})
	}
	detailOnly := testAdvertisement("memory", "detail-only", agentctx.ContextMatchSemantic, 0.9,
		testProjection("full", agentctx.ContextRoleDetail, 64))
	candidates = append(candidates, contextAdvertisementCandidate{advertiser: provider, advertisement: detailOnly})

	selected, withheld := selectContextAdvertisements(candidates)

	if len(selected) != maxSelectedContextAdvertisements {
		t.Fatalf("selected %d, want the cap %d", len(selected), maxSelectedContextAdvertisements)
	}
	if withheld != 4 {
		t.Fatalf("withheld = %d, want 4 (selectable losers only, never the detail-only non-offer)", withheld)
	}
}

func TestMaterializeRendersTheWithheldLine(t *testing.T) {
	t.Parallel()

	provider := &testContextAdvertiser{}
	provider.content = map[string]string{}
	var candidates []contextAdvertisementCandidate
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("doc-%02d", i)
		ad := testAdvertisement("memory", id, agentctx.ContextMatchSemantic, 1,
			testProjection("digest", agentctx.ContextRoleContext, 64))
		provider.content["memory/"+id+"/digest"] = "content for " + id
		candidates = append(candidates, contextAdvertisementCandidate{advertiser: provider, advertisement: ad})
	}

	assembler := NewTagContextAssembler(TagContextAssemblerConfig{})
	buckets := assembler.materializeContextAdvertisements(context.Background(), agentctx.ContextRequest{}, candidates)

	related := buckets[agentctx.ContextBucketRelated]
	if !strings.Contains(related, "2 context offer(s) withheld") {
		t.Fatalf("expected a withheld line naming 2 offers, got: %q", related)
	}
	if !strings.Contains(related, "doc_search") {
		t.Fatalf("the withheld line must name the pull door, got: %q", related)
	}
}

// ctxHonoringAdvertiser materializes like a real provider: a dead context
// is an error, not something to ignore. The plain test advertiser never
// looks at ctx, which would let a detach regression pass unnoticed.
type ctxHonoringAdvertiser struct{ testContextAdvertiser }

func (p *ctxHonoringAdvertiser) MaterializeContextAdvertisement(ctx context.Context, req agentctx.ContextRequest, selection agentctx.ContextSelection) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.testContextAdvertiser.MaterializeContextAdvertisement(ctx, req, selection)
}

func TestMaterializeDetachesFromADepletedWalkBudget(t *testing.T) {
	t.Parallel()

	provider := &ctxHonoringAdvertiser{}
	provider.advertisements = []agentctx.ContextAdvertisement{
		testAdvertisement("memory", "survivor", agentctx.ContextMatchSemantic, 1,
			testProjection("digest", agentctx.ContextRoleContext, 64)),
	}
	provider.content = map[string]string{"memory/survivor/digest": "made it"}

	// The walk's context is already dead — the exact state at the tail of
	// a slow turn. The winners must not inherit it.
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	assembler := NewTagContextAssembler(TagContextAssemblerConfig{})
	buckets := assembler.materializeContextAdvertisements(dead, agentctx.ContextRequest{}, []contextAdvertisementCandidate{
		{advertiser: provider, advertisement: provider.advertisements[0]},
	})

	if !strings.Contains(buckets[agentctx.ContextBucketRelated], "made it") {
		t.Fatalf("materialization must survive a dead walk context, got: %#v", buckets)
	}
}
