package app

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/unifi"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

func TestShouldPublishUnifiAPRoom(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{name: "UniFi observation", provider: unifi.RoomProvider, want: true},
		{name: "non-UniFi observation", provider: "bermuda", want: false},
		{name: "legacy unidentified clear", provider: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPublishUnifiAPRoom(tt.provider); got != tt.want {
				t.Errorf("shouldPublishUnifiAPRoom(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestCounterpartyPresenceView(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		state              string
		wantRoom           string
		wantRoomProvider   string
		wantRoomSource     string
		roomConflict       bool
		wantRoomConflict   bool
		retainRoom         bool
		wantRoomSinceEmpty bool
	}{
		{
			name:             "home keeps attributed room",
			state:            "home",
			wantRoom:         "office",
			wantRoomProvider: "unifi",
			wantRoomSource:   "ap-office",
			retainRoom:       true,
		},
		{
			name:               "home reports conflict without room",
			state:              "home",
			roomConflict:       true,
			wantRoomConflict:   true,
			retainRoom:         true,
			wantRoomSinceEmpty: true,
		},
		{
			name:               "named zone hides retained room",
			state:              "work",
			roomConflict:       true,
			retainRoom:         true,
			wantRoomSinceEmpty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := contacts.PersonSnapshot{
				State:        tt.state,
				Since:        now.Add(-2 * time.Hour),
				RoomConflict: tt.roomConflict,
			}
			if tt.retainRoom {
				snapshot.Room = "office"
				snapshot.RoomSince = now.Add(-20 * time.Minute)
				snapshot.RoomProvider = "unifi"
				snapshot.RoomSource = "ap-office"
			}
			view := counterpartyPresenceView(snapshot, now)
			if view.Room != tt.wantRoom || view.RoomProvider != tt.wantRoomProvider || view.RoomSource != tt.wantRoomSource {
				t.Errorf("room view = %+v", view)
			}
			if view.RoomConflict != tt.wantRoomConflict {
				t.Errorf("RoomConflict = %v, want %v", view.RoomConflict, tt.wantRoomConflict)
			}
			if (view.RoomSince == "") != tt.wantRoomSinceEmpty {
				t.Errorf("RoomSince = %q, want empty=%v", view.RoomSince, tt.wantRoomSinceEmpty)
			}
			if view.Since != "-2h" {
				t.Errorf("Since = %q, want -2h", view.Since)
			}
		})
	}
}

// TestCompanionContactForAccountRequiresConfiguredSource pins the
// resolver that stamps a device's contact at registration. An
// unconfigured or unclaimed source must yield nil rather than an empty
// resolver, because a resolver that answers "" for everything would
// unbind every device on the next reconcile.
func TestCompanionContactForAccountRequiresConfiguredSource(t *testing.T) {
	const contactID = "8A1F50A7-91C1-4DE5-90D3-B239719D29A8"
	const canonical = "8a1f50a7-91c1-4de5-90d3-b239719d29a8"
	tests := []struct {
		name    string
		cfg     config.CompanionConfig
		account string
		want    string
		wantNil bool
	}{
		{
			name: "disabled with retained provider",
			cfg: config.CompanionConfig{
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Tokens: []string{"token"}, Contact: contactID},
				},
			},
			wantNil: true,
		},
		{
			name: "enabled without token",
			cfg: config.CompanionConfig{
				Enabled: true,
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Contact: contactID},
				},
			},
			wantNil: true,
		},
		{
			name: "configured binding is canonicalized",
			cfg: config.CompanionConfig{
				Enabled: true,
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Tokens: []string{"token"}, Contact: contactID},
				},
			},
			account: "alice",
			want:    canonical,
		},
		{
			name: "configured account the operator did not claim",
			cfg: config.CompanionConfig{
				Enabled: true,
				Providers: map[string]config.CompanionProviderConfig{
					"alice": {Tokens: []string{"token"}, Contact: contactID},
					"bob":   {Tokens: []string{"token"}},
				},
			},
			account: "bob",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolve := companionContactForAccount(tt.cfg)
			if tt.wantNil {
				if resolve != nil {
					t.Fatalf("resolver = non-nil, want nil for an unconfigured source")
				}
				return
			}
			if resolve == nil {
				t.Fatal("resolver = nil, want a resolver")
			}
			if got := resolve(tt.account); got != tt.want {
				t.Errorf("resolve(%q) = %q, want %q", tt.account, got, tt.want)
			}
		})
	}
}
