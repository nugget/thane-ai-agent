package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// gateContacts is the directory the drafts-only gate tests share: one
// contact at each zone, an address two records share whose least
// privileged is known, an automated mailbox holding a household record,
// and an address whose lookup fails.
func gateContacts() *stubContacts {
	return &stubContacts{
		zones: map[string]string{
			"operator@example.com": "admin",
			"hannah@example.com":   "household",
			"alice@example.com":    "trusted",
			"kim@example.com":      "known",
			"noreply@example.com":  "household",
		},
		owners: map[string]bool{"operator@example.com": true},
		ambiguous: map[string][]ContactCandidate{
			"twins@example.com": {{ID: "t1", Name: "Twin One", TrustZone: "trusted"}, {ID: "t2", Name: "Twin Two", TrustZone: "known"}},
		},
		failing: map[string]error{"broken@example.com": errors.New("database is locked")},
	}
}

// gateMode is one account configuration every gate input runs against.
type gateMode string

const (
	modeRelaxed       gateMode = "relaxed"
	modeRelaxedNoSMTP gateMode = "relaxed operator mailbox without smtp"
	modeStrict        gateMode = "strict drafts"
	modeByTrustZone   gateMode = "by_trust_zone"
)

var allGateModes = []gateMode{modeRelaxed, modeRelaxedNoSMTP, modeStrict, modeByTrustZone}

// gateService builds the primary account in mode. Every mode denies
// example.org, so the denied-domain input means the same on each.
func gateService(t *testing.T, mode gateMode, tweak func(*Config)) (*Service, *memIMAP, *smtpFake) {
	t.Helper()
	return policyService(t, ServiceDependencies{Contacts: gateContacts()}, func(cfg *Config) {
		acct := &cfg.Accounts[0]
		acct.Policy.DeniedRecipientDomains = []string{"example.org"}
		switch mode {
		case modeRelaxed:
			acct.Policy.Delivery = DeliveryDrafts
		case modeRelaxedNoSMTP:
			acct.SMTP = SMTPConfig{}
			acct.Policy.Access = AccessSend
			acct.Policy.Delivery = DeliveryDrafts
			acct.Mailbox.Owner = "operator"
		case modeStrict:
			acct.Policy.Delivery = DeliveryDrafts
			acct.Policy.DraftGate = DraftGateStrict
		}
		if tweak != nil {
			tweak(cfg)
		}
	})
}

// gateWant is what one input must produce on one mode. recipients maps
// each assessed address to its gating; refusalOmits lists addresses a
// trust-gate refusal sentence must not name, because they passed.
type gateWant struct {
	disposition  Disposition
	route        string
	gating       string
	recipients   map[string]string
	refusalOmits []string
}

func draftedWant(gating string, recipients map[string]string) gateWant {
	return gateWant{disposition: DispositionDrafted, route: RoutePolicyDrafts, gating: gating, recipients: recipients}
}

func trustRefusedWant(recipients map[string]string, omits ...string) gateWant {
	return gateWant{disposition: DispositionRefused, route: RouteTrustGate, recipients: recipients, refusalOmits: omits}
}

// checkGate runs one call and checks its decision, the per-recipient
// gating, and that the wire and the drafts folder saw exactly what the
// disposition says.
func checkGate(t *testing.T, svc *Service, smtp *smtpFake, out string, err error, want gateWant) {
	t.Helper()
	var decision Decision
	if want.disposition == DispositionRefused {
		refusal := refusalOf(t, err)
		decision = refusal.Decision
		if decision.Route == RouteTrustGate {
			for addr, gating := range want.recipients {
				if named := strings.Contains(refusal.Message, addr); named != (gating == GatingBlocked) {
					t.Errorf("refusal sentence names %s = %v, want %v: %s", addr, named, gating == GatingBlocked, refusal.Message)
				}
			}
		}
		for _, addr := range want.refusalOmits {
			if strings.Contains(refusal.Message, addr) {
				t.Errorf("refusal sentence names %s, which passed: %s", addr, refusal.Message)
			}
		}
	} else {
		if err != nil {
			t.Fatalf("want %s, got %v", want.disposition, err)
		}
		decision = decodeSend(t, out).Decision
	}
	if decision.Disposition != want.disposition || decision.Route != want.route || decision.Gating != want.gating {
		t.Errorf("decision = %s route %s gating %q, want %s route %s gating %q", decision.Disposition, decision.Route, decision.Gating, want.disposition, want.route, want.gating)
	}
	got := map[string]string{}
	for _, a := range decision.Recipients {
		got[a.Address] = a.Gating
		if a.Gating == GatingDraftOnly && (!a.Allowed || !strings.Contains(a.Reason, "drafted only because")) {
			t.Errorf("draft_only recipient %s = %+v, want allowed with the drafted-only reason", a.Address, a)
		}
	}
	if want.recipients == nil {
		want.recipients = map[string]string{}
	}
	if !maps.Equal(got, want.recipients) {
		t.Errorf("recipient gating = %v, want %v", got, want.recipients)
	}

	wantSMTP, wantDrafts := 0, 0
	switch want.disposition {
	case DispositionSent:
		wantSMTP = 1
	case DispositionDrafted:
		wantDrafts = 1
	}
	if n := len(smtp.received()); n != wantSMTP {
		t.Errorf("SMTP deliveries = %d, want %d", n, wantSMTP)
	}
	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	drafts, listErr := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "Drafts"})
	if listErr != nil {
		t.Fatalf("list drafts: %v", listErr)
	}
	if n := len(drafts.Envelopes); n != wantDrafts {
		t.Errorf("drafts = %d, want %d", n, wantDrafts)
	}
}

// TestDraftsOnlyGateAcrossModes pins the relaxed gate on drafts-only
// accounts, with and without smtp, against the same inputs on a strict
// drafts account and on by_trust_zone, which must be unchanged: a
// recipient refused only for its zone is drafted as draft_only, while
// an automated address, a failed lookup, a denied domain, an address
// that does not parse, and the recipient limit stay refused everywhere.
func TestDraftsOnlyGateAcrossModes(t *testing.T) {
	var crowd []any
	for i := range maxRecipients + 1 {
		crowd = append(crowd, fmt.Sprintf("member%d@example.net", i))
	}
	tests := []struct {
		name        string
		to, cc      []any
		relaxed     gateWant
		strict      gateWant
		byTrustZone gateWant
	}{
		{
			name:        "admin contact",
			to:          []any{"operator@example.com"},
			relaxed:     draftedWant(GatingAllowed, map[string]string{"operator@example.com": GatingAllowed}),
			strict:      draftedWant(GatingAllowed, map[string]string{"operator@example.com": GatingAllowed}),
			byTrustZone: gateWant{disposition: DispositionSent, route: RouteTrustZone, gating: GatingAllowed, recipients: map[string]string{"operator@example.com": GatingAllowed}},
		},
		{
			name:        "stranger",
			to:          []any{"Stranger <stranger@example.com>"},
			relaxed:     draftedWant(GatingDraftOnly, map[string]string{"stranger@example.com": GatingDraftOnly}),
			strict:      trustRefusedWant(map[string]string{"stranger@example.com": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"stranger@example.com": GatingBlocked}),
		},
		{
			name:        "known contact",
			to:          []any{"kim@example.com"},
			relaxed:     draftedWant(GatingDraftOnly, map[string]string{"kim@example.com": GatingDraftOnly}),
			strict:      trustRefusedWant(map[string]string{"kim@example.com": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"kim@example.com": GatingBlocked}),
		},
		{
			name:        "ambiguous address at a blocked zone",
			to:          []any{"twins@example.com"},
			relaxed:     draftedWant(GatingDraftOnly, map[string]string{"twins@example.com": GatingDraftOnly}),
			strict:      trustRefusedWant(map[string]string{"twins@example.com": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"twins@example.com": GatingBlocked}),
		},
		{
			name:        "automated address",
			to:          []any{"noreply@example.com"},
			relaxed:     trustRefusedWant(map[string]string{"noreply@example.com": GatingBlocked}),
			strict:      trustRefusedWant(map[string]string{"noreply@example.com": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"noreply@example.com": GatingBlocked}),
		},
		{
			name:        "lookup_failed",
			to:          []any{"broken@example.com"},
			relaxed:     trustRefusedWant(map[string]string{"broken@example.com": GatingBlocked}),
			strict:      trustRefusedWant(map[string]string{"broken@example.com": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"broken@example.com": GatingBlocked}),
		},
		{
			name:        "stranger at a denied domain",
			to:          []any{"stranger@example.org"},
			relaxed:     trustRefusedWant(map[string]string{"stranger@example.org": GatingBlocked}),
			strict:      trustRefusedWant(map[string]string{"stranger@example.org": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"stranger@example.org": GatingBlocked}),
		},
		{
			name:        "address that does not parse",
			to:          []any{"not an address"},
			relaxed:     trustRefusedWant(map[string]string{"not an address": GatingBlocked}),
			strict:      trustRefusedWant(map[string]string{"not an address": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"not an address": GatingBlocked}),
		},
		{
			name:        "admin, stranger, and trusted together",
			to:          []any{"operator@example.com", "stranger@example.com"},
			cc:          []any{"alice@example.com"},
			relaxed:     draftedWant(GatingDraftOnly, map[string]string{"operator@example.com": GatingAllowed, "stranger@example.com": GatingDraftOnly, "alice@example.com": GatingConfirmation}),
			strict:      trustRefusedWant(map[string]string{"operator@example.com": GatingAllowed, "stranger@example.com": GatingBlocked, "alice@example.com": GatingConfirmation}),
			byTrustZone: trustRefusedWant(map[string]string{"operator@example.com": GatingAllowed, "stranger@example.com": GatingBlocked, "alice@example.com": GatingConfirmation}),
		},
		{
			name:        "stranger beside an automated address",
			to:          []any{"stranger@example.com", "noreply@example.com"},
			relaxed:     trustRefusedWant(map[string]string{"stranger@example.com": GatingDraftOnly, "noreply@example.com": GatingBlocked}, "stranger@example.com"),
			strict:      trustRefusedWant(map[string]string{"stranger@example.com": GatingBlocked, "noreply@example.com": GatingBlocked}),
			byTrustZone: trustRefusedWant(map[string]string{"stranger@example.com": GatingBlocked, "noreply@example.com": GatingBlocked}),
		},
		{
			name:        "more than 50 recipients",
			to:          crowd,
			relaxed:     gateWant{disposition: DispositionRefused, route: RouteRecipientLimit},
			strict:      gateWant{disposition: DispositionRefused, route: RouteRecipientLimit},
			byTrustZone: gateWant{disposition: DispositionRefused, route: RouteRecipientLimit},
		},
	}
	for _, tt := range tests {
		for _, mode := range allGateModes {
			t.Run(tt.name+"/"+string(mode), func(t *testing.T) {
				want := tt.byTrustZone
				switch mode {
				case modeRelaxed, modeRelaxedNoSMTP:
					want = tt.relaxed
				case modeStrict:
					want = tt.strict
				}
				svc, _, smtp := gateService(t, mode, nil)
				args := map[string]any{"to": tt.to, "subject": "hi", "body": "hello"}
				if tt.cc != nil {
					args["cc"] = tt.cc
				}
				out, err := svc.ToolProvider().HandleSend(attendedCtx(), args)
				checkGate(t, svc, smtp, out, err, want)
				// Only a relaxed account reaches the domain rule for a
				// stranger: elsewhere the zone refuses it first and
				// applyDomainRules skips a recipient already refused.
				if tt.name == "stranger at a denied domain" && (mode == modeRelaxed || mode == modeRelaxedNoSMTP) {
					mustContain(t, err.Error(), "denies recipients at example.org")
				}
			})
		}
	}
}

// TestDraftsOnlyGateReplyAll pins reply_all on a thread that mixes
// zones: a relaxed account drafts the whole reply with each recipient's
// own gating, a denied domain in the thread still refuses it there, and
// strict and by_trust_zone refuse both as before.
func TestDraftsOnlyGateReplyAll(t *testing.T) {
	thread := map[string]string{"hannah@example.com": GatingAllowed, "alice@example.com": GatingConfirmation}
	with := func(extra map[string]string) map[string]string {
		m := maps.Clone(thread)
		maps.Copy(m, extra)
		return m
	}
	tests := []struct {
		name        string
		cc          string
		relaxed     gateWant
		strict      gateWant
		byTrustZone gateWant
	}{
		{
			name:        "stranger and known contact in the thread",
			cc:          "Kim <kim@example.com>, Alice <alice@example.com>",
			relaxed:     draftedWant(GatingDraftOnly, with(map[string]string{"stranger@example.com": GatingDraftOnly, "kim@example.com": GatingDraftOnly})),
			strict:      trustRefusedWant(with(map[string]string{"stranger@example.com": GatingBlocked, "kim@example.com": GatingBlocked})),
			byTrustZone: trustRefusedWant(with(map[string]string{"stranger@example.com": GatingBlocked, "kim@example.com": GatingBlocked})),
		},
		{
			name:        "a denied domain in the thread",
			cc:          "Pat <pat@example.org>, Alice <alice@example.com>",
			relaxed:     trustRefusedWant(with(map[string]string{"stranger@example.com": GatingDraftOnly, "pat@example.org": GatingBlocked}), "stranger@example.com"),
			strict:      trustRefusedWant(with(map[string]string{"stranger@example.com": GatingBlocked, "pat@example.org": GatingBlocked})),
			byTrustZone: trustRefusedWant(with(map[string]string{"stranger@example.com": GatingBlocked, "pat@example.org": GatingBlocked})),
		},
	}
	for _, tt := range tests {
		for _, mode := range allGateModes {
			t.Run(tt.name+"/"+string(mode), func(t *testing.T) {
				want := tt.byTrustZone
				switch mode {
				case modeRelaxed, modeRelaxedNoSMTP:
					want = tt.relaxed
				case modeStrict:
					want = tt.strict
				}
				svc, mem, smtp := gateService(t, mode, nil)
				uid := mem.append("INBOX", rawMessage("Hannah <hannah@example.com>", "thane@example.com, Stranger <stranger@example.com>", "Plans", "see you", "Cc: "+tt.cc))
				out, err := svc.ToolProvider().HandleReply(attendedCtx(), map[string]any{"uid": float64(uid), "body": "count me in", "reply_all": true})
				checkGate(t, svc, smtp, out, err, want)
			})
		}
	}
}

// TestEmailAccountsEntryShowsTheDraftGate pins the routing lists and the
// draft_gate key in the Email Accounts entry: a relaxed account drafts
// for every zone and refuses none, a strict drafts account and
// by_trust_zone are unchanged, and an account that cannot write mail
// refuses every zone whatever its gate.
func TestEmailAccountsEntryShowsTheDraftGate(t *testing.T) {
	all := []string{"admin", "household", "trusted", "known", "unknown"}
	tests := []struct {
		name          string
		mode          gateMode
		tweak         func(*Config)
		wantGate      any
		wantSends     []string
		wantDraftsFor []string
		wantRefuses   []string
	}{
		{"relaxed", modeRelaxed, nil, "relaxed", []string{}, all, []string{}},
		{"relaxed operator mailbox without smtp", modeRelaxedNoSMTP, nil, "relaxed", []string{}, all, []string{}},
		{"strict drafts", modeStrict, nil, nil, []string{}, []string{"admin", "household", "trusted"}, []string{"known", "unknown"}},
		{"by_trust_zone", modeByTrustZone, nil, nil, []string{"admin", "household"}, []string{"trusted"}, []string{"known", "unknown"}},
		{"drafts delivery on an account that cannot write mail", modeRelaxed, func(cfg *Config) { cfg.Accounts[0].Policy.Access = AccessOrganize }, nil, []string{}, []string{}, all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := gateService(t, tt.mode, tt.tweak)
			block, err := svc.ContextProvider().TagContext(attendedCtx(), agentctxRequest())
			if err != nil {
				t.Fatalf("TagContext: %v", err)
			}
			var payload struct {
				Accounts []map[string]any `json:"accounts"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "### Email Accounts\n\n")), &payload); err != nil || len(payload.Accounts) != 1 {
				t.Fatalf("block = %s, %v", block, err)
			}
			entry := payload.Accounts[0]
			if entry["draft_gate"] != tt.wantGate {
				t.Errorf("draft_gate = %#v, want %#v", entry["draft_gate"], tt.wantGate)
			}
			for key, want := range map[string][]string{"sends_directly_to": tt.wantSends, "drafts_for": tt.wantDraftsFor, "refuses": tt.wantRefuses} {
				raw, _ := entry[key].([]any)
				got := make([]string, 0, len(raw))
				for _, z := range raw {
					got = append(got, fmt.Sprint(z))
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
		})
	}
}

// TestDraftOnlyRanksAndNeverSends pins the gating order a mixed list
// decides on and the routing seam's guarantee that a draft_only
// recipient drafts on every delivery mode.
func TestDraftOnlyRanksAndNeverSends(t *testing.T) {
	ranks := []struct {
		name string
		in   []string
		want string
	}{
		{"draft_only outranks confirmation", []string{GatingConfirmation, GatingDraftOnly, GatingAllowed}, GatingDraftOnly},
		{"draft_only outranks confirmation in either order", []string{GatingDraftOnly, GatingConfirmation}, GatingDraftOnly},
		{"blocked outranks draft_only", []string{GatingDraftOnly, GatingBlocked}, GatingBlocked},
	}
	for _, tt := range ranks {
		t.Run(tt.name, func(t *testing.T) {
			var assessments []RecipientAssessment
			for _, g := range tt.in {
				assessments = append(assessments, RecipientAssessment{Gating: g})
			}
			if got := mostRestrictive(assessments); got != tt.want {
				t.Errorf("mostRestrictive(%v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
	for _, delivery := range []string{DeliveryDirect, DeliveryByTrustZone, DeliveryDrafts} {
		for _, isAttended := range []bool{true, false} {
			if disposition, _ := routeDelivery(delivery, GatingDraftOnly, isAttended, false); disposition != DispositionDrafted {
				t.Errorf("routeDelivery(%s, draft_only, attended=%v) = %s, want drafted", delivery, isAttended, disposition)
			}
		}
	}
}

// TestRelaxForDraftsOnlyKeepsTheListsInStep pins the bookkeeping beside
// the assessments: a relaxed recipient leaves Blocked and joins
// Allowed, and a strict account's result is untouched.
func TestRelaxForDraftsOnlyKeepsTheListsInStep(t *testing.T) {
	addresses := []string{"stranger@example.com", "noreply@example.com", "operator@example.com"}
	relaxed := AccountConfig{Name: "a", DefaultFrom: "a@example.com", Policy: PolicyConfig{Access: AccessSend, Delivery: DeliveryDrafts}}
	strict := relaxed
	strict.Policy.DraftGate = DraftGateStrict

	result := CheckRecipientTrust(context.Background(), gateContacts(), addresses)
	relaxForDraftsOnly(&result, strict)
	if len(result.Blocked) != 2 || len(result.Allowed) != 1 {
		t.Fatalf("strict account changed the result: allowed %v blocked %v", result.Allowed, result.Blocked)
	}

	relaxForDraftsOnly(&result, relaxed)
	if !slices.Equal(result.Allowed, []string{"operator@example.com", "stranger@example.com"}) {
		t.Errorf("allowed = %v", result.Allowed)
	}
	if len(result.Blocked) != 1 || !strings.Contains(result.Blocked[0], "noreply@example.com") {
		t.Errorf("blocked = %v, want only the automated address", result.Blocked)
	}
}
