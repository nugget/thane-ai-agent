package contacts

import "testing"

func TestPolicy_AllZones(t *testing.T) {
	tests := []struct {
		zone           string
		wantFrontier   bool
		wantLocalOnly  bool
		wantProactive  string
		wantToolAccess string
		wantSendGating string
	}{
		{
			zone:           ZoneAdmin,
			wantFrontier:   true,
			wantProactive:  "full",
			wantToolAccess: "unrestricted",
			wantSendGating: "allowed",
		},
		{
			zone:           ZoneHousehold,
			wantFrontier:   true,
			wantProactive:  "full",
			wantToolAccess: "most",
			wantSendGating: "allowed",
		},
		{
			zone:           ZoneTrusted,
			wantFrontier:   true,
			wantProactive:  "limited",
			wantToolAccess: "safe",
			wantSendGating: "confirmation",
		},
		{
			zone:           ZoneKnown,
			wantLocalOnly:  true,
			wantProactive:  "none",
			wantToolAccess: "readonly",
			wantSendGating: "blocked",
		},
		{
			zone:           ZoneUnknown,
			wantProactive:  "none",
			wantToolAccess: "none",
			wantSendGating: "blocked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			p := Policy(tt.zone)
			if p.Zone != tt.zone {
				t.Errorf("Zone = %q, want %q", p.Zone, tt.zone)
			}
			if p.FrontierModelAccess != tt.wantFrontier {
				t.Errorf("FrontierModelAccess = %v, want %v", p.FrontierModelAccess, tt.wantFrontier)
			}
			if p.LocalModelOnly != tt.wantLocalOnly {
				t.Errorf("LocalModelOnly = %v, want %v", p.LocalModelOnly, tt.wantLocalOnly)
			}
			if p.ProactiveOutreach != tt.wantProactive {
				t.Errorf("ProactiveOutreach = %q, want %q", p.ProactiveOutreach, tt.wantProactive)
			}
			if p.ToolAccess != tt.wantToolAccess {
				t.Errorf("ToolAccess = %q, want %q", p.ToolAccess, tt.wantToolAccess)
			}
			if p.SendGating != tt.wantSendGating {
				t.Errorf("SendGating = %q, want %q", p.SendGating, tt.wantSendGating)
			}
		})
	}
}

func TestPolicy_UnknownFallback(t *testing.T) {
	p := Policy("garbage")
	if p.Zone != ZoneUnknown {
		t.Errorf("garbage zone should fall back to unknown, got %q", p.Zone)
	}
	if p.SendGating != "blocked" {
		t.Errorf("garbage zone SendGating = %q, want %q", p.SendGating, "blocked")
	}
}

func TestPolicies_Order(t *testing.T) {
	all := Policies()
	if len(all) != 5 {
		t.Fatalf("expected 5 policies, got %d", len(all))
	}

	expected := []string{ZoneAdmin, ZoneHousehold, ZoneTrusted, ZoneKnown, ZoneUnknown}
	for i, want := range expected {
		if all[i].Zone != want {
			t.Errorf("Policies()[%d].Zone = %q, want %q", i, all[i].Zone, want)
		}
	}
}

func TestPolicies_Defensive(t *testing.T) {
	// Mutating the returned slice must not affect internal state.
	all := Policies()
	all[0].Zone = "mutated"

	fresh := Policies()
	if fresh[0].Zone != ZoneAdmin {
		t.Error("Policies() returned a slice aliasing internal state")
	}
}

func TestValidTrustZones_Contents(t *testing.T) {
	want := map[string]bool{
		ZoneAdmin:     true,
		ZoneHousehold: true,
		ZoneTrusted:   true,
		ZoneKnown:     true,
	}

	if len(ValidTrustZones) != len(want) {
		t.Fatalf("ValidTrustZones has %d entries, want %d", len(ValidTrustZones), len(want))
	}
	for zone := range want {
		if !ValidTrustZones[zone] {
			t.Errorf("ValidTrustZones missing %q", zone)
		}
	}

	// "unknown" must NOT be storable.
	if ValidTrustZones[ZoneUnknown] {
		t.Error("ValidTrustZones should not contain ZoneUnknown")
	}

	// "owner" (legacy) must NOT be storable.
	if ValidTrustZones["owner"] {
		t.Error("ValidTrustZones should not contain legacy 'owner' zone")
	}
}
