package loop

import "strings"

// MetadataScopeTag is the [Spec.Metadata] key under which curate-style
// service loops record their per-loop scope tag — the synthetic
// capability tag (e.g. "loop:1f3793") that namespaces entity-watchlist
// subscriptions to a single loop. Two service loops watching the same
// entity with different histories/forecasts/TTLs do not interfere
// because their watch rows live under different scope tags.
const MetadataScopeTag = "scope_tag"

// metadataLegacyFocusTag is the pre-rename key name. Reads fall back
// to it through [SpecScopeTag] so existing persisted Specs keep
// working until they are next saved. New code never writes this key —
// remove after a deprecation window once production storage no longer
// carries any focus_tag entries.
const metadataLegacyFocusTag = "focus_tag"

// SpecScopeTag returns the loop's scope tag from spec.Metadata. Reads
// [MetadataScopeTag] first, then falls back to the legacy
// "focus_tag" key for definitions persisted before the rename.
// Returns "" when neither is present.
func SpecScopeTag(spec Spec) string {
	if tag := strings.TrimSpace(spec.Metadata[MetadataScopeTag]); tag != "" {
		return tag
	}
	return strings.TrimSpace(spec.Metadata[metadataLegacyFocusTag])
}
