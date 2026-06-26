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

// TestFrontmatterBlockListRoundTripIsStable covers the multi-value /
// block-list rendering path (e.g. source_refs, tags) which is quoted with
// the same strconv.Quote escaping and must parse back losslessly.
func TestFrontmatterBlockListRoundTripIsStable(t *testing.T) {
	t.Parallel()

	meta := map[string][]string{
		"tags": {`tag-with-"quote"`, `tag\with\backslash`},
	}
	rendered := renderFrontmatter(meta)
	parsed := parseFrontmatterMap(rendered)
	if reRendered := renderFrontmatter(parsed); reRendered != rendered {
		t.Fatalf("block-list render is not a fixed point:\n  cycle 1: %q\n  cycle 2: %q", rendered, reRendered)
	}
	// Values must survive intact (order-independent: parse dedupe-sorts).
	got := strings.Join(parsed["tags"], "|")
	for _, want := range meta["tags"] {
		if !strings.Contains(got, want) {
			t.Fatalf("tag %q lost across round-trip; got %q", want, got)
		}
	}
}
