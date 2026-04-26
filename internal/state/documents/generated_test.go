package documents

import (
	"strings"
	"testing"
	"time"
)

func TestGeneratedMetadataFrontmatter(t *testing.T) {
	t.Parallel()

	generatedAt := time.Date(2026, 4, 26, 13, 14, 15, 0, time.FixedZone("test", -5*60*60))
	meta := GeneratedMetadata{
		GeneratedBy:     "media_save_analysis",
		GeneratedAt:     generatedAt,
		SourceRefs:      []string{" url:https://example.test/a ", "", "feed:alpha", "feed:alpha"},
		DocumentKind:    DocumentKindMediaAnalysis,
		RefreshStrategy: RefreshStrategyImmutable,
		ManagedRoot:     "generated",
	}.Frontmatter()

	if got := firstValue(meta, GeneratedFieldBy); got != "media_save_analysis" {
		t.Fatalf("%s = %q, want media_save_analysis", GeneratedFieldBy, got)
	}
	if got := firstValue(meta, GeneratedFieldAt); got != "2026-04-26T18:14:15Z" {
		t.Fatalf("%s = %q, want UTC RFC3339 timestamp", GeneratedFieldAt, got)
	}
	if got := meta[GeneratedFieldSourceRefs]; len(got) != 2 || got[0] != "feed:alpha" || got[1] != "url:https://example.test/a" {
		t.Fatalf("%s = %#v, want normalized unique source refs", GeneratedFieldSourceRefs, got)
	}
	if got := firstValue(meta, GeneratedFieldDocumentKind); got != DocumentKindMediaAnalysis {
		t.Fatalf("%s = %q, want %q", GeneratedFieldDocumentKind, got, DocumentKindMediaAnalysis)
	}
	if got := firstValue(meta, GeneratedFieldRefreshStrategy); got != RefreshStrategyImmutable {
		t.Fatalf("%s = %q, want %q", GeneratedFieldRefreshStrategy, got, RefreshStrategyImmutable)
	}
	if got := firstValue(meta, GeneratedFieldManagedRoot); got != "generated" {
		t.Fatalf("%s = %q, want generated", GeneratedFieldManagedRoot, got)
	}
}

func TestRenderGeneratedFrontmatter(t *testing.T) {
	t.Parallel()

	raw, err := RenderGeneratedFrontmatter(GeneratedMetadata{
		GeneratedBy:     "perception_loop",
		GeneratedAt:     time.Date(2026, 4, 26, 18, 14, 15, 0, time.UTC),
		SourceRefs:      []string{"ha:camera.front_door"},
		DocumentKind:    "rolling_summary",
		RefreshStrategy: RefreshStrategyRollingWindow,
	})
	if err != nil {
		t.Fatalf("RenderGeneratedFrontmatter: %v", err)
	}

	for _, want := range []string{
		`generated_by: "perception_loop"`,
		`generated_at: "2026-04-26T18:14:15Z"`,
		`document_kind: "rolling_summary"`,
		`refresh_strategy: "rolling-window"`,
		"source_refs:\n  - \"ha:camera.front_door\"",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("rendered frontmatter = %q, want %q", raw, want)
		}
	}

	parsed := parseFrontmatterMap(raw)
	if got := firstValue(parsed, GeneratedFieldDocumentKind); got != "rolling_summary" {
		t.Fatalf("parsed %s = %q, want rolling_summary", GeneratedFieldDocumentKind, got)
	}
	if got := parsed[GeneratedFieldSourceRefs]; len(got) != 1 || got[0] != "ha:camera.front_door" {
		t.Fatalf("parsed %s = %#v, want source ref", GeneratedFieldSourceRefs, got)
	}
}

func TestRenderGeneratedFrontmatterRequiresCoreFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta GeneratedMetadata
		want string
	}{
		{
			name: "missing generator",
			meta: GeneratedMetadata{
				GeneratedAt:     time.Now(),
				DocumentKind:    DocumentKindMediaAnalysis,
				RefreshStrategy: RefreshStrategyImmutable,
			},
			want: GeneratedFieldBy,
		},
		{
			name: "missing timestamp",
			meta: GeneratedMetadata{
				GeneratedBy:     "media_save_analysis",
				DocumentKind:    DocumentKindMediaAnalysis,
				RefreshStrategy: RefreshStrategyImmutable,
			},
			want: GeneratedFieldAt,
		},
		{
			name: "missing kind",
			meta: GeneratedMetadata{
				GeneratedBy:     "media_save_analysis",
				GeneratedAt:     time.Now(),
				RefreshStrategy: RefreshStrategyImmutable,
			},
			want: GeneratedFieldDocumentKind,
		},
		{
			name: "missing refresh strategy",
			meta: GeneratedMetadata{
				GeneratedBy:  "media_save_analysis",
				GeneratedAt:  time.Now(),
				DocumentKind: DocumentKindMediaAnalysis,
			},
			want: GeneratedFieldRefreshStrategy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := RenderGeneratedFrontmatter(tt.meta)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RenderGeneratedFrontmatter error = %v, want field %q", err, tt.want)
			}
		})
	}
}
