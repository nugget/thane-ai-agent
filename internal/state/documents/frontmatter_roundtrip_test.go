package documents

import (
	"strings"
	"testing"
)

// TestFrontmatterValueRoundTripIsStable guards against the escaping-
// amplification bug that grew a loop-managed document's loop_intent
// frontmatter value into a 32 MiB run of backslashes on prod
// (knowledge/temporal/ranch-conditions.md, 2026-06). renderFrontmatter
// quotes values with strconv.Quote, so any embedded double-quote,
// backslash, or control character is escaped on write; the parser must
// reverse that escaping on read. When it does not, every read→render
// cycle re-escapes the already-escaped characters and the value doubles
// in size each iteration of the owning service loop.
//
// The invariant under test: a value survives render→parse unchanged, and
// re-rendering a parsed value is a fixed point (no growth across cycles).
func TestFrontmatterValueRoundTripIsStable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string
	}{
		{"plain", "just some prose with no special characters"},
		{"embedded_double_quote", `a one-line "still settling toward evening, quiet" is correct`},
		{"backslash", `a path C:\Users\aimee and an escape \n literal`},
		{"newline", "first paragraph\n\nsecond paragraph"},
		{"tab", "col1\tcol2"},
		{"unicode", "Aimée — felt sense, who's home"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			meta := map[string][]string{"loop_intent": {tc.value}}

			rendered := renderFrontmatter(meta)
			parsed := parseFrontmatterMap(rendered)
			if got := firstValue(parsed, "loop_intent"); got != tc.value {
				t.Fatalf("value not preserved across render→parse:\n  want %q\n   got %q", tc.value, got)
			}

			// A second cycle must be a fixed point: re-rendering the parsed
			// value yields byte-identical frontmatter. Any growth here is the
			// amplification bug.
			reRendered := renderFrontmatter(parsed)
			if reRendered != rendered {
				t.Fatalf("render is not a fixed point — value is amplifying across cycles:\n  cycle 1 (%d bytes): %q\n  cycle 2 (%d bytes): %q",
					len(rendered), rendered, len(reRendered), reRendered)
			}

			// Belt and suspenders: simulate several owning-loop cycles and
			// assert the rendered size never grows.
			cur := rendered
			for i := 0; i < 5; i++ {
				next := renderFrontmatter(parseFrontmatterMap(cur))
				if len(next) > len(cur) {
					t.Fatalf("frontmatter grew on cycle %d: %d → %d bytes", i+1, len(cur), len(next))
				}
				cur = next
			}
		})
	}
}

// TestFrontmatterInlineListRoundTripIsStable covers the inline multi-value
// list path — `key: ["a", "b"]` — which renderFrontmatter uses for every
// multi-value key except source_refs. Each element is quoted with the same
// strconv.Quote escaping and must parse back losslessly.
func TestFrontmatterInlineListRoundTripIsStable(t *testing.T) {
	t.Parallel()

	meta := map[string][]string{
		"tags": {`tag-with-"quote"`, `tag\with\backslash`},
	}
	rendered := renderFrontmatter(meta)
	if !strings.Contains(rendered, "[") {
		t.Fatalf("expected inline list rendering, got %q", rendered)
	}
	assertListRoundTripStable(t, meta, "tags", rendered)
}

// TestFrontmatterBlockListRoundTripIsStable covers the block-list path —
//
//	source_refs:
//	  - "a"
//	  - "b"
//
// which renderFrontmatter uses *only* for source_refs (GeneratedFieldSourceRefs),
// and which parseFrontmatterMap parses through its separate `- item`
// branch. That branch does its own quote-stripping, so it is independently
// vulnerable to the escaping-amplification bug and needs its own coverage.
func TestFrontmatterBlockListRoundTripIsStable(t *testing.T) {
	t.Parallel()

	meta := map[string][]string{
		GeneratedFieldSourceRefs: {`kb:notes/with "quotes".md`, `core:path\with\backslash`, "plain-ref"},
	}
	rendered := renderFrontmatter(meta)
	if !strings.Contains(rendered, GeneratedFieldSourceRefs+":\n") || !strings.Contains(rendered, "\n  - ") {
		t.Fatalf("expected block-list rendering for %s, got %q", GeneratedFieldSourceRefs, rendered)
	}
	assertListRoundTripStable(t, meta, GeneratedFieldSourceRefs, rendered)
}

// assertListRoundTripStable verifies a multi-value frontmatter key both
// preserves its values across render→parse and stops growing across
// repeated cycles. Value order is normalized by the parser (dedupe-sorted),
// so the fixed-point check is taken from the second cycle onward; the
// no-growth check spans every cycle and is what actually catches the
// amplification bug regardless of ordering.
func assertListRoundTripStable(t *testing.T, meta map[string][]string, key, rendered string) {
	t.Helper()

	parsed := parseFrontmatterMap(rendered)
	got := strings.Join(parsed[key], "\x00")
	for _, want := range meta[key] {
		if !strings.Contains("\x00"+got+"\x00", "\x00"+want+"\x00") {
			t.Fatalf("value %q for key %q lost across round-trip; got %q", want, key, parsed[key])
		}
	}

	// Run several owning-loop cycles. Past the first normalization the
	// rendered bytes must be a fixed point, and the size must never grow.
	prev := renderFrontmatter(parseFrontmatterMap(rendered))
	for i := 0; i < 5; i++ {
		cur := renderFrontmatter(parseFrontmatterMap(prev))
		if len(cur) > len(prev) {
			t.Fatalf("%s frontmatter grew on cycle %d: %d → %d bytes", key, i+1, len(prev), len(cur))
		}
		if cur != prev {
			t.Fatalf("%s render is not a fixed point on cycle %d:\n  prev: %q\n  cur:  %q", key, i+1, prev, cur)
		}
		prev = cur
	}
}
