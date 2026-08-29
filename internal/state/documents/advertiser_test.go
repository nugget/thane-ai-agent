package documents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
)

// advertiserUnderTest builds a DocumentAdvertiser over the shared
// advertise-store fixture: root "beta" holds the faceted curator dossier
// (plus an internal notes doc), root "alpha" holds plain documents.
func advertiserUnderTest(t *testing.T, policies map[string]DocumentRootAdvertisePolicy, sleepMax func(string) (time.Duration, bool)) *DocumentAdvertiser {
	t.Helper()
	store, _ := newAdvertiseStore(t)
	return NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store: store,
		RootPolicy: func(root string) DocumentRootAdvertisePolicy {
			return policies[root]
		},
		SleepMax: sleepMax,
		HomeZone: time.UTC,
		Now:      func() time.Time { return time.Now() },
	})
}

func TestDocumentAdvertiserHonorsRootPolicy(t *testing.T) {
	tests := []struct {
		name     string
		policies map[string]DocumentRootAdvertisePolicy
		req      agentctx.ContextRequest
		wantRefs []string
	}{
		{
			name:     "no policy offers nothing",
			policies: map[string]DocumentRootAdvertisePolicy{},
			wantRefs: nil,
		},
		{
			name: "always offers the faceted document",
			policies: map[string]DocumentRootAdvertisePolicy{
				"beta": {Mode: "always"},
			},
			wantRefs: []string{"beta:dossier.md"},
		},
		{
			name: "tagged stays quiet without its tag",
			policies: map[string]DocumentRootAdvertisePolicy{
				"beta": {Mode: "tagged", RequiresTag: "travel"},
			},
			wantRefs: nil,
		},
		{
			name: "tagged offers while its tag is active",
			policies: map[string]DocumentRootAdvertisePolicy{
				"beta": {Mode: "tagged", RequiresTag: "travel"},
			},
			req:      agentctx.ContextRequest{ActiveTags: map[string]bool{"travel": true}},
			wantRefs: []string{"beta:dossier.md"},
		},
		{
			// alpha's documents are unfaceted: detail is all they could
			// offer, and automatic selection never takes detail — they
			// stay reachable through search instead of burning offers.
			name: "an always root with only unfaceted documents offers nothing",
			policies: map[string]DocumentRootAdvertisePolicy{
				"alpha": {Mode: "always"},
			},
			wantRefs: nil,
		},
		{
			name: "a pinned ref is not offered twice",
			policies: map[string]DocumentRootAdvertisePolicy{
				"beta": {Mode: "always"},
			},
			req:      agentctx.ContextRequest{PinnedRefs: []string{"beta:dossier.md"}},
			wantRefs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adv := advertiserUnderTest(t, tc.policies, nil)
			ads, err := adv.ContextAdvertisements(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("ContextAdvertisements: %v", err)
			}
			var refs []string
			for _, ad := range ads {
				refs = append(refs, ad.Ref)
			}
			if len(refs) != len(tc.wantRefs) {
				t.Fatalf("offered %v, want %v", refs, tc.wantRefs)
			}
			for i := range refs {
				if refs[i] != tc.wantRefs[i] {
					t.Fatalf("offered %v, want %v", refs, tc.wantRefs)
				}
			}
		})
	}
}

func TestDocumentAdvertiserProjectionsCarryIndexedCosts(t *testing.T) {
	adv := advertiserUnderTest(t, map[string]DocumentRootAdvertisePolicy{"beta": {Mode: "always"}}, nil)

	ads, err := adv.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements: %v (%d ads)", err, len(ads))
	}
	if err := ads[0].Validate(); err != nil {
		t.Fatalf("advertisement does not satisfy the contract: %v", err)
	}

	roles := map[string]agentctx.ContextProjectionRole{}
	for _, p := range ads[0].Projections {
		roles[p.Name] = p.Role
		if p.EstimatedBytes <= advertiseEnvelopeOverheadBytes {
			t.Fatalf("projection %s estimate %d does not cover content plus envelope", p.Name, p.EstimatedBytes)
		}
	}
	// The dossier declares status_line + teaser (both role signal) and a
	// full body (detail). Roles come from the facet contract's table.
	if roles["status_line"] != agentctx.ContextRoleSignal || roles["teaser"] != agentctx.ContextRoleSignal {
		t.Fatalf("compact facets should carry role signal, got %v", roles)
	}
	if roles["full"] != agentctx.ContextRoleDetail {
		t.Fatalf("full should carry role detail, got %v", roles)
	}
}

func TestDocumentAdvertiserFreshnessBandsAgainstTheLiveSleepCeiling(t *testing.T) {
	base := time.Now()
	tests := []struct {
		name         string
		age          time.Duration
		sleepMax     time.Duration
		wantStrength float64
	}{
		{"within one wake is fresh", 4 * time.Hour, 6 * time.Hour, advertiseFreshStrength},
		{"a few quiet wakes is aging", 20 * time.Hour, 6 * time.Hour, advertiseAgingStrength},
		{"beyond four wakes is stale", 30 * time.Hour, 6 * time.Hour, advertiseStaleStrength},
		{"no maintaining loop uses the daily default", 20 * time.Hour, 0, advertiseFreshStrength},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sleepMax := func(string) (time.Duration, bool) { return tc.sleepMax, tc.sleepMax > 0 }
			adv := advertiserUnderTest(t, map[string]DocumentRootAdvertisePolicy{"beta": {Mode: "always"}}, sleepMax)
			adv.cfg.Now = func() time.Time { return base }

			row := AdvertisableDocument{ModifiedAt: base.Add(-tc.age), LoopDefinitionName: "trip-curator"}
			if got := adv.freshnessStrength(row, base); got != tc.wantStrength {
				t.Fatalf("strength = %v, want %v", got, tc.wantStrength)
			}
		})
	}
}

func TestDocumentAdvertiserMatchEvidence(t *testing.T) {
	adv := advertiserUnderTest(t, map[string]DocumentRootAdvertisePolicy{"beta": {Mode: "always"}}, nil)

	// A user message naming a doc tag earns lexical evidence.
	ads, err := adv.ContextAdvertisements(context.Background(),
		agentctx.ContextRequest{UserMessage: "what's the plan for the utah drive?"})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements: %v (%d)", err, len(ads))
	}
	kinds := map[agentctx.ContextMatchKind]bool{}
	for _, m := range ads[0].Matches {
		kinds[m.Kind] = true
	}
	if !kinds[agentctx.ContextMatchAmbient] || !kinds[agentctx.ContextMatchLexical] {
		t.Fatalf("want ambient + lexical evidence, got %v", ads[0].Matches)
	}

	// A subject handle on ctx matching a doc tag is the strongest evidence.
	ctx := knowledge.WithSubjects(context.Background(), []string{"utah"})
	ads, err = adv.ContextAdvertisements(ctx, agentctx.ContextRequest{})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements with subjects: %v (%d)", err, len(ads))
	}
	found := false
	for _, m := range ads[0].Matches {
		if m.Kind == agentctx.ContextMatchExactSubject && m.Strength == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("want exact_subject evidence, got %v", ads[0].Matches)
	}
}

func TestDocumentAdvertiserMaterializesWithEnvelopeAndTemplates(t *testing.T) {
	store, dirs := newAdvertiseStore(t)
	_ = dirs
	fixedNow := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adv := NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store:      store,
		RootPolicy: func(string) DocumentRootAdvertisePolicy { return DocumentRootAdvertisePolicy{Mode: "always"} },
		HomeZone:   time.UTC,
		Now:        func() time.Time { return fixedNow },
	})

	// Rewrite the dossier teaser to carry a temporal template, then
	// re-index so enumeration sees it.
	ads, err := adv.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil || len(ads) == 0 {
		t.Fatalf("ContextAdvertisements: %v (%d)", err, len(ads))
	}
	var dossier agentctx.ContextAdvertisement
	for _, ad := range ads {
		if ad.Ref == "beta:dossier.md" {
			dossier = ad
		}
	}
	var teaser agentctx.ContextProjection
	for _, p := range dossier.Projections {
		if p.Name == "teaser" {
			teaser = p
		}
	}
	if teaser.Name == "" {
		t.Fatalf("dossier offered no teaser projection: %v", dossier.Projections)
	}

	out, err := adv.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
		Advertisement: dossier,
		Projection:    teaser,
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	lines := strings.SplitN(out, "\n", 2)
	if len(lines) != 2 {
		t.Fatalf("fragment should open with an envelope line, got: %q", out)
	}
	for _, want := range []string{`"ref":"beta:dossier.md"`, `"source_loop":"trip-curator"`, `"match":"ambient"`, `"updated_delta":"`, `"more":"doc_read(beta:dossier.md, level=full)"`} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("envelope missing %s: %s", want, lines[0])
		}
	}
	if !strings.Contains(lines[1], "Why the dossier matters") {
		t.Fatalf("fragment should carry the teaser content, got: %q", lines[1])
	}

	// A selection for a facet the document no longer carries errors
	// loudly instead of rendering an empty fragment.
	_, err = adv.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
		Advertisement: dossier,
		Projection:    agentctx.ContextProjection{Name: "digest", Role: agentctx.ContextRoleContext, Format: "text/markdown", EstimatedBytes: 64},
	})
	if err == nil || !strings.Contains(err.Error(), "no longer carries facet") {
		t.Fatalf("want missing-facet error, got %v", err)
	}
}

func TestDocumentAdvertiserExpandsTemporalTemplatesAtMaterialization(t *testing.T) {
	store, dirs := newAdvertiseStore(t)
	writeFile(t, dirs["beta"]+"/dossier.md", `---
title: Utah Trip Dossier
tags: [travel, utah]
managed_by: loop_output_trip
loop_definition_name: trip-curator
---

## Status Line

Utah trip Sep 18 ({{delta:2026-09-18}})

## Details

Body.
`)
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	fixedNow := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adv := NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store:      store,
		RootPolicy: func(string) DocumentRootAdvertisePolicy { return DocumentRootAdvertisePolicy{Mode: "always"} },
		HomeZone:   time.UTC,
		Now:        func() time.Time { return fixedNow },
	})

	ads, err := adv.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil || len(ads) == 0 {
		t.Fatalf("ContextAdvertisements: %v", err)
	}
	out, err := adv.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
		Advertisement: ads[0],
		Projection:    agentctx.ContextProjection{Name: "status_line", Role: agentctx.ContextRoleSignal, Format: "text/markdown", EstimatedBytes: 512},
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if !strings.Contains(out, "Utah trip Sep 18 (+20d)") {
		t.Fatalf("temporal template should expand at materialization, got: %q", out)
	}
	if strings.Contains(out, "{{delta:") {
		t.Fatalf("raw template leaked to the reader surface: %q", out)
	}
}
