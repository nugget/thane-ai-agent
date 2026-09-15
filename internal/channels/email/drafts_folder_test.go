package email

import (
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// setFolderAttrs replaces a folder's special-use attributes on the
// in-memory server; none removes its role while the folder stays.
func (m *memIMAP) setFolderAttrs(name string, attrs ...imap.MailboxAttr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(attrs) == 0 {
		delete(m.attrs, name)
		return
	}
	m.attrs[name] = attrs
}

// messageCount counts the messages in every folder of the account, so a
// refusal can be shown to have written nothing anywhere.
func messageCount(t *testing.T, svc *Service, mem *memIMAP) int {
	t.Helper()
	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	total := 0
	for _, folder := range mem.folderNames() {
		listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: folder})
		if err != nil {
			t.Fatalf("list %s: %v", folder, err)
		}
		total += len(listed.Envelopes)
	}
	return total
}

// TestUnresolvableDraftsFolderRefuses pins the drafts role: with no
// drafts_folder and no folder the server marks as drafts (a folder that
// happens to be called Drafts does not count), every message the policy
// or the caller would draft is refused with route no_drafts_folder and
// written nowhere, and by_trust_zone never sends in the draft's place.
// A configured drafts_folder resolves without the mark, and mail the
// policy sends is unaffected.
func TestUnresolvableDraftsFolderRefuses(t *testing.T) {
	tests := []struct {
		name            string
		delivery        string
		draftsFolder    string
		ctx             func() context.Context
		to              string
		draft           bool
		wantDisposition Disposition
		wantRoute       string
	}{
		{name: "relaxed drafts account", delivery: DeliveryDrafts, ctx: attendedCtx, to: "operator@example.com",
			wantDisposition: DispositionRefused, wantRoute: RouteNoDraftsFolder},
		{name: "relaxed drafts account to a stranger", delivery: DeliveryDrafts, ctx: attendedCtx, to: "stranger@example.com",
			wantDisposition: DispositionRefused, wantRoute: RouteNoDraftsFolder},
		{name: "by_trust_zone unattended floor", ctx: context.Background, to: "operator@example.com",
			wantDisposition: DispositionRefused, wantRoute: RouteNoDraftsFolder},
		{name: "by_trust_zone trusted recipient", ctx: attendedCtx, to: "alice@example.com",
			wantDisposition: DispositionRefused, wantRoute: RouteNoDraftsFolder},
		{name: "by_trust_zone requested draft", ctx: attendedCtx, to: "operator@example.com", draft: true,
			wantDisposition: DispositionRefused, wantRoute: RouteNoDraftsFolder},
		{name: "control: by_trust_zone attended admin is sent", ctx: attendedCtx, to: "operator@example.com",
			wantDisposition: DispositionSent, wantRoute: RouteTrustZone},
		{name: "control: configured drafts_folder resolves without the mark", delivery: DeliveryDrafts, draftsFolder: "Drafts", ctx: attendedCtx, to: "operator@example.com",
			wantDisposition: DispositionDrafted, wantRoute: RoutePolicyDrafts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, smtp := gateService(t, modeByTrustZone, func(cfg *Config) {
				cfg.Accounts[0].Policy.Delivery = tt.delivery
				cfg.Accounts[0].DraftsFolder = tt.draftsFolder
			})
			mem.setFolderAttrs("Drafts")
			args := sendArgs(tt.to)
			if tt.draft {
				args["draft"] = true
			}
			out, err := svc.ToolProvider().HandleSend(tt.ctx(), args)

			wantSMTP, wantStored := 0, 0
			switch tt.wantDisposition {
			case DispositionRefused:
				refusal := refusalOf(t, err)
				if refusal.Decision.Route != tt.wantRoute || refusal.Decision.DraftsFolder != "" {
					t.Errorf("refusal decision = %+v, want route %s and no drafts folder", refusal.Decision, tt.wantRoute)
				}
				sentence, _, _ := strings.Cut(err.Error(), "\n")
				if !strings.HasSuffix(sentence, ".") || strings.Contains(sentence, ". ") {
					t.Errorf("refusal reason must be one sentence: %q", sentence)
				}
				mustContain(t, sentence, `account "primary"`, "drafts role", "configure drafts_folder", "nothing was sent or drafted")
				// Only a draft the caller asked for can be got out by
				// dropping the flag, so only its refusal forbids that.
				if forbids := strings.Contains(sentence, "neither resend it without the draft: true you asked for"); forbids != tt.draft {
					t.Errorf("refusal forbids resending without draft: true = %v, want %v: %s", forbids, tt.draft, sentence)
				}
			case DispositionSent:
				wantSMTP = 1
				fallthrough
			default:
				if err != nil {
					t.Fatalf("want %s, got %v", tt.wantDisposition, err)
				}
				resp := decodeSend(t, out)
				if resp.Disposition != tt.wantDisposition || resp.Decision.Route != tt.wantRoute {
					t.Errorf("result = %s route %s, want %s route %s", resp.Disposition, resp.Decision.Route, tt.wantDisposition, tt.wantRoute)
				}
				if tt.wantDisposition == DispositionDrafted {
					wantStored = 1
					if resp.DraftsFolder != tt.draftsFolder {
						t.Errorf("drafts_folder = %q, want %q", resp.DraftsFolder, tt.draftsFolder)
					}
				}
			}
			if n := len(smtp.received()); n != wantSMTP {
				t.Errorf("SMTP deliveries = %d, want %d", n, wantSMTP)
			}
			if n := messageCount(t, svc, mem); n != wantStored {
				t.Errorf("messages stored = %d, want %d", n, wantStored)
			}
		})
	}
}

// TestDraftsFolderResolvesByAFreshListing pins the resolver's last step:
// a cached listing without the drafts role is not the final word, so
// once the server marks a folder, the next draft finds it with one fresh
// LIST, and the Email Accounts entry shows the folder only from then on.
func TestDraftsFolderResolvesByAFreshListing(t *testing.T) {
	svc, mem, _ := gateService(t, modeRelaxed, nil)
	mem.setFolderAttrs("Drafts")
	send := svc.ToolProvider().HandleSend

	if _, err := send(attendedCtx(), sendArgs("operator@example.com")); refusalOf(t, err).Decision.Route != RouteNoDraftsFolder {
		t.Fatalf("with no drafts role: %v", err)
	}
	block, _ := svc.ContextProvider().TagContext(attendedCtx(), agentctxRequest())
	if strings.Contains(block, `"drafts_folder"`) {
		t.Errorf("the entry shows a drafts folder the send path could not resolve:\n%s", block)
	}

	mem.setFolderAttrs("Drafts", imap.MailboxAttrDrafts)
	lists := func() int { mem.mu.Lock(); defer mem.mu.Unlock(); return mem.lists }
	before := lists()
	out, err := send(attendedCtx(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("once the server marks the folder: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted || resp.DraftsFolder != "Drafts" {
		t.Errorf("result = %+v", resp)
	}
	if n := lists() - before; n != 1 {
		t.Errorf("LIST commands = %d, want exactly one fresh listing", n)
	}
	block, _ = svc.ContextProvider().TagContext(attendedCtx(), agentctxRequest())
	mustContain(t, block, `"drafts_folder":"Drafts"`)
}

// TestUnresolvedDraftsRoleIsNoDestination pins the refusal a move by
// destination_role drafts gets when no folder has the drafts role: it
// says drafts is never a destination without claiming that a drafts
// folder exists, and nothing moves.
func TestUnresolvedDraftsRoleIsNoDestination(t *testing.T) {
	svc, mem, _ := gateService(t, modeByTrustZone, func(cfg *Config) {
		cfg.Accounts[0].Mailbox.MoveInto = []string{"*"}
	})
	mem.setFolderAttrs("Drafts")
	uid := mem.append("INBOX", rawMessage("Bob <bob@example.net>", "thane@example.com", "stays", "x"))
	_, err := svc.ToolProvider().HandleMove(attendedCtx(), map[string]any{"uid": float64(uid), "destination_role": "drafts"})
	if err == nil {
		t.Fatal("destination_role drafts must be refused")
	}
	mustContain(t, err.Error(), `destination_role drafts on account "primary"`, "drafts is not among destination_role's values", "nothing was moved")
	if strings.Contains(err.Error(), "drafts folder") {
		t.Errorf("the refusal claims a drafts folder where no folder has the role: %v", err)
	}
	if n := messageCount(t, svc, mem); n != 1 {
		t.Errorf("messages stored = %d, want the one left in INBOX", n)
	}
}
