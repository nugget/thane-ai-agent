package documents

import "testing"

// TestRestrictedAudienceCoversEveryNarrowerReach pins the advertisement
// and search gate against the reach ladder. Only agent-wide documents —
// and documents declaring no audience at all, which is most of the
// corpus — may be offered ambiently.
func TestRestrictedAudienceCoversEveryNarrowerReach(t *testing.T) {
	tests := []struct {
		name       string
		audience   string
		restricted bool
	}{
		{"private is restricted", "private", true},
		{"subscribers is restricted", "subscribers", true},
		{"agent is not", "agent", false},
		{"absent is agent-wide", "", false},
		// Frontmatter written before the rename is on disk and must keep
		// gating, or a working-notes document starts being advertised on
		// the turn this ships.
		{"legacy internal still restricted", "internal", true},
		{"legacy published still open", "published", false},
		{"case and padding ignored", "  SubScribers  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRestrictedAudience(tt.audience); got != tt.restricted {
				t.Errorf("IsRestrictedAudience(%q) = %v, want %v", tt.audience, got, tt.restricted)
			}
			fm := map[string][]string{audienceFrontmatterKey: {tt.audience}}
			if got := isRestrictedAudienceDocument(fm); got != tt.restricted {
				t.Errorf("isRestrictedAudienceDocument(%q) = %v, want %v", tt.audience, got, tt.restricted)
			}
		})
	}
}

// TestRestrictedAudienceArgsMatchTheSQLPlaceholders guards a silent
// failure mode: AdvertisableDocuments names one placeholder per value,
// so adding a reach without widening the IN clause would quietly stop
// excluding it.
func TestRestrictedAudienceArgsMatchTheSQLPlaceholders(t *testing.T) {
	if got, want := len(restrictedAudienceArgs()), len(restrictedAudienceValues); got != want {
		t.Fatalf("restrictedAudienceArgs() length = %d, want %d", got, want)
	}
	if len(restrictedAudienceValues) != 3 {
		t.Fatalf("restrictedAudienceValues has %d entries; the AdvertisableDocuments IN clause names 3 placeholders and must be widened to match", len(restrictedAudienceValues))
	}
}
