package documents

import (
	"context"
	"os"
	"path/filepath"
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
		if p.EstimatedBytes <= advertiseEnvelopeSlackBytes {
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

func TestDocumentAdvertiserExactSubjectHasNoAmbientOrLexicalFallback(t *testing.T) {
	store, dirs := newAdvertiseStore(t)
	writeFile(t, filepath.Join(dirs["beta"], "other-dossier.md"), `---
title: California Dossier
tags: [california]
---

## Status Line

California plans are quiet

## Teaser

The unrelated dossier has its own compact signal.

## Details

Unrelated detail.
`)
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	adv := NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store: store,
		RootPolicy: func(root string) DocumentRootAdvertisePolicy {
			if root == "beta" {
				return DocumentRootAdvertisePolicy{Mode: "exact_subject"}
			}
			return DocumentRootAdvertisePolicy{}
		},
		HomeZone: time.UTC,
	})

	for _, tc := range []struct {
		name string
		ctx  context.Context
		req  agentctx.ContextRequest
	}{
		{name: "no subject", ctx: context.Background()},
		{name: "lexical mention only", ctx: context.Background(), req: agentctx.ContextRequest{UserMessage: "tell me about the Utah trip"}},
		{name: "unrelated subject", ctx: knowledge.WithSubjects(context.Background(), []string{"contact:someone-else"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ads, err := adv.ContextAdvertisements(tc.ctx, tc.req)
			if err != nil {
				t.Fatalf("ContextAdvertisements: %v", err)
			}
			if len(ads) != 0 {
				t.Fatalf("exact-subject policy offered %v without an exact subject", ads)
			}
		})
	}

	ctx := knowledge.WithSubjects(context.Background(), []string{"utah"})
	ads, err := adv.ContextAdvertisements(ctx, agentctx.ContextRequest{UserMessage: "unrelated words"})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements exact subject: %v (%d ads)", err, len(ads))
	}
	if ads[0].Ref != "beta:dossier.md" {
		t.Fatalf("exact-subject refs = %v, want only beta:dossier.md", ads)
	}
	if len(ads[0].Matches) != 1 || ads[0].Matches[0].Kind != agentctx.ContextMatchExactSubject {
		t.Fatalf("exact-subject matches = %v, want only exact_subject evidence", ads[0].Matches)
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
	// The fixture's first Refresh ran moments ago; without disabling the
	// throttle this second one would no-op, the index would still hold
	// the pre-rewrite row, and the materializer's changed-since-offered
	// check would (correctly) refuse to render.
	store.refreshInterval = 0
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

func TestDocumentAdvertiserRefusesAFileChangedSinceTheOffer(t *testing.T) {
	store, dirs := newAdvertiseStore(t)
	adv := NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store:      store,
		RootPolicy: func(string) DocumentRootAdvertisePolicy { return DocumentRootAdvertisePolicy{Mode: "always"} },
		HomeZone:   time.UTC,
	})
	ads, err := adv.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements: %v (%d)", err, len(ads))
	}

	// The file changes after the offer and before selection — the shape
	// enumeration's deliberate staleness makes possible. The stale index
	// row must not lend its provenance to whatever is on disk now.
	writeFile(t, filepath.Join(dirs["beta"], "dossier.md"), "---\ntitle: Swapped\n---\n\n## Status Line\n\nnot what was offered\n")

	_, err = adv.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
		Advertisement: ads[0],
		Projection:    agentctx.ContextProjection{Name: "status_line", Role: agentctx.ContextRoleSignal, Format: "text/markdown", EstimatedBytes: 512},
	})
	if err == nil || !strings.Contains(err.Error(), "changed since it was offered") {
		t.Fatalf("want changed-since-offered refusal, got %v", err)
	}
}

func TestDocumentAdvertiserRefusesAFreshlyInternalDocument(t *testing.T) {
	store, dirs := newAdvertiseStore(t)
	adv := NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store:      store,
		RootPolicy: func(string) DocumentRootAdvertisePolicy { return DocumentRootAdvertisePolicy{Mode: "always"} },
		HomeZone:   time.UTC,
	})
	ads, err := adv.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements: %v (%d)", err, len(ads))
	}

	// Republished as internal after the offer. The mtime/size check
	// would already refuse this rewrite; the fresh audience check is the
	// second lock on the same door, pinned here by defeating the first:
	// remember the row, rewrite the file, then restore the remembered
	// stat identity is impossible — so instead assert through the
	// internal seam that the audience gate alone refuses.
	d := adv
	d.mu.RLock()
	entry := d.lastRows[ads[0].Ref]
	d.mu.RUnlock()

	internalBody := "---\naudience: internal\n---\n\n## Status Line\n\nsecret\n"
	writeFile(t, filepath.Join(dirs["beta"], "dossier.md"), internalBody)
	info, err := os.Stat(filepath.Join(dirs["beta"], "dossier.md"))
	if err != nil {
		t.Fatal(err)
	}
	entry.row.ModifiedAt = info.ModTime()
	entry.row.SizeBytes = info.Size()
	d.mu.Lock()
	d.lastRows[ads[0].Ref] = entry
	d.mu.Unlock()

	_, err = adv.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
		Advertisement: ads[0],
		Projection:    agentctx.ContextProjection{Name: "status_line", Role: agentctx.ContextRoleSignal, Format: "text/markdown", EstimatedBytes: 512},
	})
	if err == nil || !strings.Contains(err.Error(), "audience-internal") {
		t.Fatalf("want audience-internal refusal, got %v", err)
	}
}

func TestDocumentAdvertiserEstimatesCoverTheMeasuredEnvelope(t *testing.T) {
	store, _ := newAdvertiseStore(t)
	fixed := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	adv := NewDocumentAdvertiser(DocumentAdvertiserConfig{
		Store:      store,
		RootPolicy: func(string) DocumentRootAdvertisePolicy { return DocumentRootAdvertisePolicy{Mode: "always"} },
		HomeZone:   time.UTC,
		Now:        func() time.Time { return fixed },
	})
	ads, err := adv.ContextAdvertisements(context.Background(), agentctx.ContextRequest{})
	if err != nil || len(ads) != 1 {
		t.Fatalf("ContextAdvertisements: %v (%d)", err, len(ads))
	}

	// The commitment the discriminator enforces: rendered bytes never
	// exceed the declared estimate. Estimates are built from the exact
	// stored envelope plus the indexed facet bytes, so this must hold
	// for every offered projection — including one with a long ref that
	// a fixed overhead constant would have under-counted.
	for _, p := range ads[0].Projections {
		if p.Role == agentctx.ContextRoleDetail {
			continue
		}
		out, err := adv.MaterializeContextAdvertisement(context.Background(), agentctx.ContextRequest{}, agentctx.ContextSelection{
			Advertisement: ads[0],
			Projection:    p,
		})
		if err != nil {
			t.Fatalf("Materialize %s: %v", p.Name, err)
		}
		if len(out) > p.EstimatedBytes {
			t.Fatalf("projection %s rendered %d bytes over its own estimate %d", p.Name, len(out), p.EstimatedBytes)
		}
	}
}
