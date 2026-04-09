package documents

import "testing"

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
