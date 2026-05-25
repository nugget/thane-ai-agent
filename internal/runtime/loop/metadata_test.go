package loop

import "testing"

// TestSpecScopeTag covers the read-side rename + legacy fallback so a
// future contributor can't accidentally regress either side: the
// canonical "scope_tag" key has priority, the pre-rename "focus_tag"
// key still works for specs persisted before the rename, and absence
// returns an empty string rather than something stringly-truthy.
func TestSpecScopeTag(t *testing.T) {
	t.Parallel()

	t.Run("scope_tag wins", func(t *testing.T) {
		spec := Spec{Metadata: map[string]string{
			MetadataScopeTag:       "loop:new",
			metadataLegacyFocusTag: "loop:old",
		}}
		if got := SpecScopeTag(spec); got != "loop:new" {
			t.Fatalf("got %q, want loop:new (scope_tag must win over focus_tag)", got)
		}
	})

	t.Run("legacy focus_tag fallback", func(t *testing.T) {
		spec := Spec{Metadata: map[string]string{
			metadataLegacyFocusTag: "loop:legacy",
		}}
		if got := SpecScopeTag(spec); got != "loop:legacy" {
			t.Fatalf("got %q, want loop:legacy (focus_tag fallback)", got)
		}
	})

	t.Run("neither set", func(t *testing.T) {
		if got := SpecScopeTag(Spec{}); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		spec := Spec{Metadata: map[string]string{
			MetadataScopeTag: "  loop:trim  ",
		}}
		if got := SpecScopeTag(spec); got != "loop:trim" {
			t.Fatalf("got %q, want trimmed loop:trim", got)
		}
	})

	t.Run("empty scope_tag falls through to legacy", func(t *testing.T) {
		// Edge case: a future migration that clears scope_tag to ""
		// should still read the legacy key, not silently lose the tag.
		spec := Spec{Metadata: map[string]string{
			MetadataScopeTag:       "",
			metadataLegacyFocusTag: "loop:legacy",
		}}
		if got := SpecScopeTag(spec); got != "loop:legacy" {
			t.Fatalf("got %q, want loop:legacy (empty scope_tag falls through)", got)
		}
	})
}
