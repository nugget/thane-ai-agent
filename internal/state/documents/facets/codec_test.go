package facets

import "testing"

func TestParseLegacyRequiresCompleteEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		faceted bool
	}{
		{
			name:    "ordinary details section",
			body:    "# Runbook\n\nOverview.\n\n## Details\n\nOperator-authored detail.",
			faceted: false,
		},
		{
			name:    "ordinary digest section",
			body:    "# Notes\n\nOverview.\n\n## Digest\n\nA legitimate digest heading.",
			faceted: false,
		},
		{
			name:    "historical envelope",
			body:    "## Status Line\n\nNominal.\n\n## Details\n\nFull state.",
			faceted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, faceted := ParseLegacy(tt.body)
			if faceted != tt.faceted {
				t.Fatalf("ParseLegacy faceted = %v, want %v", faceted, tt.faceted)
			}
			if !tt.faceted && payload.Full != tt.body {
				t.Fatalf("ordinary document full = %q, want original body %q", payload.Full, tt.body)
			}
		})
	}
}
