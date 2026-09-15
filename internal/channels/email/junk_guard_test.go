package email

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// guardContacts is the directory the junk guard tests read: Alice is
// trusted, the operator's own record sits at known so that is_owner
// alone must protect it, and Bob is a stranger.
func guardContacts() *stubContacts {
	return &stubContacts{
		zones:  map[string]string{"alice@example.org": "trusted", "operator@example.org": "known"},
		owners: map[string]bool{"operator@example.org": true},
	}
}

// guardSenders are the three messages every guard case starts with,
// keyed by subject.
var guardSenders = map[string]string{
	"from alice":        "Alice <alice@example.org>",
	"from the operator": "The operator <operator@example.org>",
	"from bob":          "Bob <bob@example.org>",
}

// TestJunkGuardRefusesProtectedSendersInUnattendedMoves pins the guard
// end to end: in a turn the operator is not present for, a move into
// the junk folder, named by role or by name and on any account, refuses
// per message the trusted sender and the operator's own record and
// moves the stranger; an attended turn and a move anywhere else are not
// guarded.
func TestJunkGuardRefusesProtectedSendersInUnattendedMoves(t *testing.T) {
	all := []string{"from alice", "from bob", "from the operator"}
	protected := map[string]string{"from alice": "a contact at trusted holds the address", "from the operator": "the operator's own contact record (is_owner)"}
	tests := []struct {
		name        string
		owner       string
		ctx         context.Context
		target      map[string]any
		subjects    []string
		wantAction  string
		wantMoved   []string
		wantRefused map[string]string
	}{
		{"unattended by role", "", context.Background(), map[string]any{"destination_role": "junk"}, all, "moved", []string{"from bob"}, protected},
		{"unattended by the junk folder's name", "", context.Background(), map[string]any{"destination": junkTestFolder}, all, "moved", []string{"from bob"}, protected},
		{"unattended on an operator mailbox", "operator", context.Background(), map[string]any{"destination_role": "junk"}, all, "moved", []string{"from bob"}, protected},
		{"attended is not guarded", "", attendedCtx(), map[string]any{"destination_role": "junk"}, all, "moved", all, nil},
		{"unattended elsewhere is not guarded", "", context.Background(), map[string]any{"destination": "Receipts"}, all, "moved", all, nil},
		{"unattended with every message refused", "", context.Background(), map[string]any{"destination_role": "junk"}, []string{"from alice"}, "refused", nil, map[string]string{"from alice": "a contact at trusted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, guardContacts(), false, func(a *AccountConfig) { a.Mailbox.Owner = tt.owner })
			uids := make([]any, 0, len(tt.subjects))
			subjectByUID := make(map[uint32]string)
			for _, subject := range tt.subjects {
				uid := mem.append("INBOX", rawMessage(guardSenders[subject], "alice@example.org", subject, "x"))
				uids = append(uids, float64(uid))
				subjectByUID[uid] = subject
			}
			args := map[string]any{"uids": uids}
			for k, v := range tt.target {
				args[k] = v
			}
			out, err := svc.ToolProvider().HandleMove(tt.ctx, args)
			if err != nil {
				t.Fatalf("HandleMove: %v", err)
			}
			resp := decodeMove(t, out)
			if resp.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", resp.Action, tt.wantAction)
			}
			if len(resp.UIDsNotFound) != 0 {
				t.Errorf("uids_not_found = %v; a refused UID is listed under refused, not as missing", resp.UIDsNotFound)
			}

			var moved []string
			for _, m := range resp.Moved {
				subject := subjectByUID[m.UID]
				moved = append(moved, subject)
				if m.MessageID != messageIDFor(subject) || m.From != guardSenders[subject] || m.TrustZone == "" || m.DestinationUID == 0 {
					t.Errorf("moved entry = %+v, want message_id %q, from %q, a trust_zone, and a destination_uid", m, messageIDFor(subject), guardSenders[subject])
				}
			}
			slices.Sort(moved)
			if !slices.Equal(moved, tt.wantMoved) {
				t.Errorf("moved = %v, want %v", moved, tt.wantMoved)
			}

			if len(resp.Refused) != len(tt.wantRefused) {
				t.Fatalf("refused = %+v, want %d entries", resp.Refused, len(tt.wantRefused))
			}
			for _, r := range resp.Refused {
				subject := subjectByUID[r.UID]
				want, ok := tt.wantRefused[subject]
				if !ok {
					t.Errorf("refused %q, which the guard must let through", subject)
					continue
				}
				if r.From != guardSenders[subject] || r.TrustZone == "" || !strings.Contains(r.Reason, want) {
					t.Errorf("refused entry = %+v, want from %q and a reason containing %q", r, guardSenders[subject], want)
				}
				mustContain(t, r.Recovery, `email_mark flag "flagged"`, "request_core_attention", "do not retry the move")
			}

			stayed := make([]string, 0, len(tt.wantRefused))
			for subject := range tt.wantRefused {
				stayed = append(stayed, subject)
			}
			slices.Sort(stayed)
			if got := subjectsIn(t, svc, "INBOX"); !slices.Equal(got, stayed) {
				t.Errorf("INBOX after the move = %v, want %v", got, stayed)
			}
		})
	}
}

// TestJunkGuardResultCarriesTrustZones pins what a guarded result says
// about each sender: the stranger moved at unknown, the trusted sender
// was refused at trusted, and the operator's record at its own zone.
func TestJunkGuardResultCarriesTrustZones(t *testing.T) {
	svc, mem := filingService(t, guardContacts(), false, nil)
	var uids []any
	for _, subject := range []string{"from alice", "from bob", "from the operator"} {
		uids = append(uids, float64(mem.append("INBOX", rawMessage(guardSenders[subject], "alice@example.org", subject, "x"))))
	}
	out, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uids": uids, "destination_role": "junk"})
	if err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	resp := decodeMove(t, out)
	if len(resp.Moved) != 1 || resp.Moved[0].From != guardSenders["from bob"] || resp.Moved[0].TrustZone != ZoneUnknown || resp.Moved[0].MessageID != messageIDFor("from bob") {
		t.Errorf("moved = %+v, want Bob at unknown with his message_id", resp.Moved)
	}
	zones := make(map[string]string)
	for _, r := range resp.Refused {
		zones[r.From] = r.TrustZone
	}
	want := map[string]string{guardSenders["from alice"]: "trusted", guardSenders["from the operator"]: "known"}
	if len(zones) != len(want) {
		t.Fatalf("refused = %+v, want %v", resp.Refused, want)
	}
	for from, zone := range want {
		if zones[from] != zone {
			t.Errorf("refused %s at %q, want %q", from, zones[from], zone)
		}
	}
}

// TestJunkGuardLogsEachRefusal pins the forensic trail: one line per
// refused message, naming it and the reason.
func TestJunkGuardLogsEachRefusal(t *testing.T) {
	var buf bytes.Buffer
	svc, mem, _ := identityServiceWith(t, ServiceDependencies{Contacts: guardContacts(), Logger: slog.New(slog.NewTextHandler(&buf, nil))}, nil)
	mem.addFolder(junkTestFolder, imap.MailboxAttrJunk)
	uid := mem.append("INBOX", rawMessage(guardSenders["from alice"], "alice@example.org", "from alice", "x"))
	if _, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid), "destination_role": "junk"}); err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	mustContain(t, buf.String(), "email junk move refused", "from_address=alice@example.org", "trust_zone=trusted", `reason="a contact at trusted holds the address"`)
}

// TestJunkProtection pins which directory answers protect a sender
// from an unattended junk move.
func TestJunkProtection(t *testing.T) {
	matched := func(zone string, owner bool) ContactMatch {
		return ContactMatch{Status: ContactMatched, TrustZone: zone, Binding: &memory.ChannelBinding{ContactID: "c", TrustZone: zone, IsOwner: owner}}
	}
	tests := []struct {
		name  string
		match ContactMatch
		want  string
	}{
		{"admin", matched("admin", false), "a contact at admin"},
		{"household", matched("household", false), "a contact at household"},
		{"trusted", matched("trusted", false), "a contact at trusted"},
		{"known", matched("known", false), ""},
		{"operator's record at any zone", matched("known", true), "is_owner"},
		{"automated notice at a trusted record", ContactMatch{Status: ContactMatched, TrustZone: "known", Automated: true, Binding: &memory.ChannelBinding{TrustZone: "trusted"}}, "a contact at trusted"},
		{"stranger", ContactMatch{Status: ContactUnmatched, TrustZone: ZoneUnknown}, ""},
		{"ambiguous, all protected", ContactMatch{Status: ContactAmbiguous, TrustZone: "trusted", Candidates: []ContactCandidate{{TrustZone: "admin"}, {TrustZone: "trusted"}}}, "all of them at trusted or above"},
		{"ambiguous, one protected", ContactMatch{Status: ContactAmbiguous, TrustZone: "known", Candidates: []ContactCandidate{{TrustZone: "household"}, {TrustZone: "known"}}}, "one of them at household"},
		{"ambiguous, none protected", ContactMatch{Status: ContactAmbiguous, TrustZone: "known", Candidates: []ContactCandidate{{TrustZone: "known"}, {TrustZone: "known"}}}, ""},
		{"ambiguous, none protected among every record", ContactMatch{Status: ContactAmbiguous, TrustZone: "known", Candidates: []ContactCandidate{{TrustZone: "known"}, {TrustZone: "known"}}, CandidatesTotal: 2}, ""},
		{"ambiguous, more records than the directory named", ContactMatch{Status: ContactAmbiguous, TrustZone: "known", Candidates: []ContactCandidate{{TrustZone: "known"}, {TrustZone: "known"}}, CandidatesTotal: 11}, "11 contacts hold the address and not all of them could be checked"},
		{"directory unavailable", ContactMatch{Status: ContactLookupFailed, TrustZone: ZoneUnknown, Err: errors.New("database is locked")}, "could not be consulted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := junkProtection(tt.match)
			if tt.want == "" {
				if got != "" {
					t.Errorf("junkProtection = %q, want no protection", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("junkProtection = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}
