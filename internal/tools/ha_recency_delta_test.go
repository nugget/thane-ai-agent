package tools

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// TestHARecencyDelta covers the since/updated consistency guaranteed by
// haRecencyDelta, including the #1131 edge case where a zero LastChanged must
// not let `updated` populate off a nonsense delta against the zero time.
func TestHARecencyDelta(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	changed := now.Add(-10 * time.Minute)

	cases := []struct {
		name        string
		state       homeassistant.State
		wantSince   bool
		wantUpdated bool
	}{
		{
			name:        "both set, updated equals changed",
			state:       homeassistant.State{LastChanged: changed, LastUpdated: changed},
			wantSince:   true,
			wantUpdated: false,
		},
		{
			name:        "updated meaningfully after changed",
			state:       homeassistant.State{LastChanged: changed, LastUpdated: now.Add(-time.Minute)},
			wantSince:   true,
			wantUpdated: true,
		},
		{
			name:        "last_changed zero but last_updated set yields neither",
			state:       homeassistant.State{LastUpdated: now.Add(-time.Minute)},
			wantSince:   false,
			wantUpdated: false,
		},
		{
			name:        "both zero yields neither",
			state:       homeassistant.State{},
			wantSince:   false,
			wantUpdated: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			since, updated := haRecencyDelta(tc.state, now)
			if (since != "") != tc.wantSince {
				t.Errorf("since = %q, want non-empty = %v", since, tc.wantSince)
			}
			if (updated != "") != tc.wantUpdated {
				t.Errorf("updated = %q, want non-empty = %v", updated, tc.wantUpdated)
			}
		})
	}
}
