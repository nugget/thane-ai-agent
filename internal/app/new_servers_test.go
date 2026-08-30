package app

import (
	"slices"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

func TestCompanionAccountsByContactRequiresConfiguredSource(t *testing.T) {
	const contactID = "8A1F50A7-91C1-4DE5-90D3-B239719D29A8"
	tests := []struct {
		name string
		cfg  config.CompanionConfig
		want map[string][]string
	}{
		{
			name: "disabled with retained provider",
			cfg: config.CompanionConfig{
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Tokens: []string{"token"}, Contact: contactID},
				},
			},
		},
		{
			name: "enabled without token",
			cfg: config.CompanionConfig{
				Enabled: true,
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Contact: contactID},
				},
			},
		},
		{
			name: "configured",
			cfg: config.CompanionConfig{
				Enabled: true,
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Tokens: []string{"token"}, Contact: contactID},
				},
			},
			want: map[string][]string{
				"8a1f50a7-91c1-4de5-90d3-b239719d29a8": {"alice"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := companionAccountsByContact(tt.cfg)
			if len(got) != len(tt.want) {
				t.Fatalf("bindings = %#v, want %#v", got, tt.want)
			}
			for contactID, wantAccounts := range tt.want {
				if !slices.Equal(got[contactID], wantAccounts) {
					t.Errorf("accounts for %s = %#v, want %#v", contactID, got[contactID], wantAccounts)
				}
			}
		})
	}
}
