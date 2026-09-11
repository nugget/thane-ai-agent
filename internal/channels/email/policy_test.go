package email

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestRouteDelivery(t *testing.T) {
	tests := []struct {
		name      string
		delivery  string
		gating    string
		attended  bool
		draft     bool
		wantDisp  Disposition
		wantRoute string
	}{
		{"requested draft wins over direct", DeliveryDirect, GatingAllowed, true, true, DispositionDrafted, RouteRequestedDraft},
		{"drafts mode holds everything", DeliveryDrafts, GatingAllowed, true, false, DispositionDrafted, RoutePolicyDrafts},
		{"direct sends unattended confirmation", DeliveryDirect, GatingConfirmation, false, false, DispositionSent, RoutePolicyDirect},
		{"by zone attended allowed sends", DeliveryByTrustZone, GatingAllowed, true, false, DispositionSent, RouteTrustZone},
		{"by zone attended confirmation drafts", DeliveryByTrustZone, GatingConfirmation, true, false, DispositionDrafted, RouteTrustZone},
		{"by zone unattended allowed drafts", DeliveryByTrustZone, GatingAllowed, false, false, DispositionDrafted, RouteUnattendedFloor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disp, route := routeDelivery(tt.delivery, tt.gating, tt.attended, tt.draft)
			if disp != tt.wantDisp || route != tt.wantRoute {
				t.Errorf("routeDelivery(%s, %s, attended=%v, draft=%v) = %s/%s, want %s/%s", tt.delivery, tt.gating, tt.attended, tt.draft, disp, route, tt.wantDisp, tt.wantRoute)
			}
		})
	}
}

func TestAttended(t *testing.T) {
	if attended(context.Background()) {
		t.Error("a bare context is unattended")
	}
	if !attended(tools.WithMessageOrigin(context.Background(), memory.OriginAPI)) {
		t.Error("an API-origin turn is attended")
	}
	if attended(tools.WithMessageOrigin(context.Background(), memory.OriginWake)) {
		t.Error("a wake is unattended")
	}
	owner := tools.WithChannelBinding(context.Background(), &memory.ChannelBinding{Channel: "signal", Address: "+1", IsOwner: true})
	if !attended(owner) {
		t.Error("a conversation bound to the operator is attended")
	}
	other := tools.WithChannelBinding(context.Background(), &memory.ChannelBinding{Channel: "signal", Address: "+2", ContactID: "x", TrustZone: "trusted"})
	if attended(other) {
		t.Error("a conversation with anyone else is unattended")
	}
}

func TestApplyDomainRules(t *testing.T) {
	resolver := &stubContacts{zones: map[string]string{
		"alice@example.com":        "admin",
		"bob@sub.example.com":      "household",
		"carol@example.org":        "trusted",
		"dave@blocked.example":     "admin",
		"eve@deep.blocked.example": "admin",
	}}
	all := []string{"alice@example.com", "bob@sub.example.com", "carol@example.org", "dave@blocked.example", "eve@deep.blocked.example"}

	check := func(t *testing.T, policy PolicyConfig, wantAllowed []string, wantReason string) {
		t.Helper()
		result := CheckRecipientTrust(context.Background(), resolver, all)
		applyDomainRules(&result, policy)
		if strings.Join(result.Allowed, ",") != strings.Join(wantAllowed, ",") {
			t.Errorf("allowed = %v, want %v", result.Allowed, wantAllowed)
		}
		for _, a := range result.Assessments {
			if !a.Allowed && !strings.Contains(a.Reason, wantReason) {
				t.Errorf("%s blocked with %q, want it to mention %q", a.Address, a.Reason, wantReason)
			}
			if !a.Allowed && a.Gating != GatingBlocked {
				t.Errorf("%s blocked but gating = %q", a.Address, a.Gating)
			}
		}
		if len(result.Blocked) != len(all)-len(wantAllowed) {
			t.Errorf("blocked = %v", result.Blocked)
		}
	}

	t.Run("denied domain covers subdomains and any zone", func(t *testing.T) {
		check(t, PolicyConfig{DeniedRecipientDomains: []string{"Blocked.example"}}, []string{"alice@example.com", "bob@sub.example.com", "carol@example.org"}, "denies recipients at")
	})
	t.Run("allow list limits everything else", func(t *testing.T) {
		check(t, PolicyConfig{AllowedRecipientDomains: []string{".example.com"}}, []string{"alice@example.com", "bob@sub.example.com"}, "limits recipients to")
	})
	t.Run("denied wins inside the allow list", func(t *testing.T) {
		check(t, PolicyConfig{AllowedRecipientDomains: []string{"example.com", "blocked.example"}, DeniedRecipientDomains: []string{"blocked.example"}}, []string{"alice@example.com", "bob@sub.example.com"}, "")
	})
	t.Run("no rules change nothing", func(t *testing.T) {
		check(t, PolicyConfig{}, all, "")
	})
}

func TestMostRestrictiveGating(t *testing.T) {
	if got := mostRestrictive(nil); got != GatingAllowed {
		t.Errorf("empty = %q", got)
	}
	mixed := []RecipientAssessment{{Gating: GatingAllowed}, {Gating: GatingConfirmation}, {Gating: GatingAllowed}}
	if got := mostRestrictive(mixed); got != GatingConfirmation {
		t.Errorf("mixed = %q", got)
	}
}

func TestPolicyRefusalRendersSentenceThenDecision(t *testing.T) {
	err := &PolicyRefusal{Message: "Email not sent: because.", Decision: Decision{Disposition: DispositionRefused, Account: "primary", Route: RouteTrustGate, Recipients: []RecipientAssessment{}}}
	text := err.Error()
	sentence, rest, found := strings.Cut(text, "\n")
	if !found || sentence != "Email not sent: because." {
		t.Fatalf("refusal must be one sentence then a newline: %q", text)
	}
	var decision Decision
	if jsonErr := json.Unmarshal([]byte(rest), &decision); jsonErr != nil {
		t.Fatalf("refusal payload is not decision JSON: %v\n%s", jsonErr, rest)
	}
	if decision.Route != RouteTrustGate || decision.Disposition != DispositionRefused {
		t.Errorf("decision = %+v", decision)
	}
	mustContain(t, rest, `"recipients":[]`)
}

func TestRoutingByZoneProjectsThePolicy(t *testing.T) {
	sending := AccountConfig{Name: "a", SMTP: SMTPConfig{Host: "h", Username: "u"}, Policy: PolicyConfig{Access: AccessSend, Delivery: DeliveryByTrustZone}}
	attendedRouting := routingByZone(sending, true)
	if strings.Join(attendedRouting.SendsDirectlyTo, ",") != "admin,household" || strings.Join(attendedRouting.DraftsFor, ",") != "trusted" || strings.Join(attendedRouting.Refuses, ",") != "known,unknown" {
		t.Errorf("attended by_trust_zone = %+v", attendedRouting)
	}
	unattendedRouting := routingByZone(sending, false)
	if len(unattendedRouting.SendsDirectlyTo) != 0 || strings.Join(unattendedRouting.DraftsFor, ",") != "admin,household,trusted" {
		t.Errorf("unattended by_trust_zone = %+v", unattendedRouting)
	}

	direct := sending
	direct.Policy.Delivery = DeliveryDirect
	if r := routingByZone(direct, false); strings.Join(r.SendsDirectlyTo, ",") != "admin,household,trusted" || len(r.DraftsFor) != 0 {
		t.Errorf("direct = %+v", r)
	}

	draftsOnly := AccountConfig{Name: "b", Policy: PolicyConfig{Access: AccessSend, Delivery: DeliveryDrafts}}
	if r := routingByZone(draftsOnly, true); len(r.SendsDirectlyTo) != 0 || strings.Join(r.DraftsFor, ",") != "admin,household,trusted" {
		t.Errorf("drafts without smtp = %+v", r)
	}

	organize := AccountConfig{Name: "c", Policy: PolicyConfig{Access: AccessOrganize}}
	if r := routingByZone(organize, true); len(r.Refuses) != 5 || len(r.SendsDirectlyTo) != 0 || len(r.DraftsFor) != 0 {
		t.Errorf("organize = %+v", r)
	}
}
