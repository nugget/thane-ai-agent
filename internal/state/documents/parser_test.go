package documents

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitFrontmatterSupportsCRLF(t *testing.T) {
	t.Parallel()

	raw := "---\r\ntitle: Windows Note\r\ntags: [alpha, beta]\r\n---\r\n\r\n# Heading\r\n\r\nBody.\r\n"
	meta, body := splitFrontmatter(raw)

	if got := meta["title"]; len(got) != 1 || got[0] != "Windows Note" {
		t.Fatalf("title = %#v, want Windows Note", got)
	}
	if got := meta["tags"]; len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("tags = %#v, want [alpha beta]", got)
	}
	if body == raw || body == "" {
		t.Fatalf("body = %q, want frontmatter stripped", body)
	}
}

func TestSplitFrontmatterSupportsBlockListValues(t *testing.T) {
	t.Parallel()

	raw := "---\n" +
		"title: Block List Note\n" +
		"tags:\n" +
		"  - alpha\n" +
		"  - beta\n" +
		"description: Example\n" +
		"---\n\n" +
		"# Heading\n\nBody.\n"
	meta, _ := splitFrontmatter(raw)

	if got := meta["title"]; len(got) != 1 || got[0] != "Block List Note" {
		t.Fatalf("title = %#v, want Block List Note", got)
	}
	if got := meta["tags"]; len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("tags = %#v, want [alpha beta]", got)
	}
}

// TestParseMarkdownDocumentCapturesFacetBytes pins the index-time byte
// measurement the context advertiser costs offers with (#1431). The
// expectations are BYTE lengths, not rune counts, on purpose: facet
// budgets elsewhere in the contract are runes (display budgets), but
// these entries price a projection against a byte-denominated context
// budget, and a rune count under-estimates multi-byte UTF-8 content.
func TestParseMarkdownDocumentCapturesFacetBytes(t *testing.T) {
	t.Parallel()

	const (
		asciiStatus  = "all systems nominal"
		asciiTeaser  = "why a reader would open this now"
		asciiDigest  = "a standalone summary with enough substance to act on"
		asciiDetails = "the full working body\n\nwith a second paragraph"
		utf8Status   = "café ☕ prêt — übersicht"
		utf8Details  = "détails complets: 状況は安定しています"
		plainBody    = "# Plain\n\nJust a first paragraph.\n"
	)
	if len(utf8Status) == utf8.RuneCountInString(utf8Status) {
		t.Fatalf("utf8Status must contain multi-byte runes for this test to prove bytes-not-runes")
	}

	tests := []struct {
		name string
		body string
		want map[string]int
	}{
		{
			name: "faceted ascii carries every present level plus full",
			body: "## Status Line\n\n" + asciiStatus + "\n\n## Teaser\n\n" + asciiTeaser + "\n\n## Digest\n\n" + asciiDigest + "\n\n## Details\n\n" + asciiDetails + "\n",
			want: map[string]int{
				"status_line": len(asciiStatus),
				"teaser":      len(asciiTeaser),
				"digest":      len(asciiDigest),
				"full":        len(asciiDetails),
			},
		},
		{
			name: "multi-byte content is measured in bytes",
			body: "## Status Line\n\n" + utf8Status + "\n\n## Details\n\n" + utf8Details + "\n",
			want: map[string]int{
				"status_line": len(utf8Status),
				"full":        len(utf8Details),
			},
		},
		{
			name: "absent facets get no entries",
			body: "## Status Line\n\n" + asciiStatus + "\n\n## Details\n\n" + asciiDetails + "\n",
			want: map[string]int{
				"status_line": len(asciiStatus),
				"full":        len(asciiDetails),
			},
		},
		{
			name: "unfaceted document still measures full",
			body: plainBody,
			want: map[string]int{
				"full": len(strings.TrimSpace(plainBody)),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := parseMarkdownDocumentParts("doc.md", map[string][]string{}, tc.body)
			if len(doc.FacetBytes) != len(tc.want) {
				t.Fatalf("FacetBytes = %v, want %v", doc.FacetBytes, tc.want)
			}
			for key, want := range tc.want {
				if got := doc.FacetBytes[key]; got != want {
					t.Errorf("FacetBytes[%q] = %d, want %d", key, got, want)
				}
			}
		})
	}
}

// TestParseMarkdownDocumentPromotesAudience pins the promotion of the
// single audience frontmatter value to a first-class parsed field, which
// is what lets the index store it as a column and gate internal-audience
// documents in SQL (#1250, #1431).
func TestParseMarkdownDocumentPromotesAudience(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "absent audience promotes empty",
			raw:  "---\ntitle: Plain\n---\n\n# Plain\n\nBody.\n",
			want: "",
		},
		{
			name: "internal audience",
			raw:  "---\naudience: internal\n---\n\n# Notes\n\nBody.\n",
			want: "internal",
		},
		{
			name: "published audience",
			raw:  "---\naudience: published\n---\n\n# Doc\n\nBody.\n",
			want: "published",
		},
		{
			name: "rendered quoting is unquoted",
			raw:  "---\naudience: \"internal\"\n---\n\n# Notes\n\nBody.\n",
			want: "internal",
		},
		{
			name: "no frontmatter at all",
			raw:  "# Doc\n\nBody.\n",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := parseMarkdownDocument("doc.md", tc.raw)
			if doc.Audience != tc.want {
				t.Errorf("Audience = %q, want %q", doc.Audience, tc.want)
			}
		})
	}
}
