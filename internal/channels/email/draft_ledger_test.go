package email

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestDraftLedgerRecordsEveryDraft pins the entry Service.Send writes
// after a drafted APPEND: the identity the proof checks (UIDVALIDITY,
// the APPENDUID, the Message-ID), the original a reply answers, the
// recipients and subject, stage open, and the first version's author,
// which is the loop's name, or operator in the operator's own turn.
func TestDraftLedgerRecordsEveryDraft(t *testing.T) {
	tests := []struct {
		name   string
		ctx    context.Context
		reply  bool
		wantBy string
	}{
		{"reply in the operator's turn", attendedCtx(), true, "operator"},
		{"reply from a named loop", loopCtx("email-owner-triage"), true, "email-owner-triage"},
		{"new message from a loop with no name", tools.WithLoopID(context.Background(), "loop-9"), false, "loop:loop-9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			var resp sendResponse
			var original uint32
			if tt.reply {
				resp, original = f.replyDraft(t, tt.ctx, "Saturday plan", "Yes, Saturday works.")
			} else {
				out, err := f.svc.ToolProvider().HandleSend(tt.ctx, sendArgs("alice@example.com"))
				if err != nil {
					t.Fatalf("send: %v", err)
				}
				resp = decodeSend(t, out)
			}
			if resp.Disposition != DispositionDrafted || resp.DraftID == "" {
				t.Fatalf("result = %+v, want drafted with a draft_id", resp)
			}
			mustContain(t, resp.Note, resp.DraftID, "email_draft_revise")

			e := f.entry(t, resp.DraftID)
			_, uidNext := f.drafts(t)
			acct, _ := f.svc.ResolveAccount(context.Background(), "primary")
			status, _ := acct.Client.MailboxStatus(context.Background(), "Drafts")
			if e.Stage != DraftStageOpen || e.Folder != "Drafts" || e.UID != resp.DraftUID || e.UID == 0 || e.UID >= uidNext || e.UIDValidity != status.UIDValidity {
				t.Errorf("identity = stage %s folder %s uid %d validity %d, want open Drafts uid %d validity %d", e.Stage, e.Folder, e.UID, e.UIDValidity, resp.DraftUID, status.UIDValidity)
			}
			if e.MessageID != resp.MessageID || e.Subject != resp.Subject || !slices.Equal(e.To, resp.To) || e.From != "Thane <thane@example.com>" {
				t.Errorf("headers = %+v, want the result's %+v", e, resp)
			}
			if len(e.History) != 1 || e.History[0].By != tt.wantBy || e.Revisions != 0 || e.Pending != nil {
				t.Errorf("history = %+v revisions %d, want one version by %s", e.History, e.Revisions, tt.wantBy)
			}
			if tt.reply {
				if e.Original == nil || e.Original.MessageID != messageIDFor("Saturday plan") || e.Original.Folder != "INBOX" || e.Original.From.Address != "alice@example.com" || e.InReplyTo != e.Original.MessageID {
					t.Errorf("original = %+v in_reply_to %q, want Alice's message in INBOX (uid %d)", e.Original, e.InReplyTo, original)
				}
			} else if e.Original != nil || e.InReplyTo != "" {
				t.Errorf("a new message records original %+v, want none", e.Original)
			}
		})
	}
}

// TestDraftLedgerIgnoresSentMail pins that only a draft is recorded: a
// message SMTP delivered is not Thane's to edit.
func TestDraftLedgerIgnoresSentMail(t *testing.T) {
	svc, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	out, err := svc.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionSent || resp.DraftID != "" {
		t.Fatalf("result = %+v, want sent with no draft_id", resp)
	}
	if entries, _ := svc.draftEntries(""); len(entries) != 0 {
		t.Errorf("ledger = %+v, want empty", entries)
	}
}

// seedDraft writes an entry straight into the ledger with the
// UpdatedAt given, so a test controls which entry is oldest.
func seedDraft(t *testing.T, svc *Service, e draftEntry) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.state.Set(draftLedgerNamespace, e.key(), string(data)); err != nil {
		t.Fatal(err)
	}
}

// seedLiveDraft stores a real draft in Drafts and seeds an entry of the
// given stage that records it, with the UpdatedAt given, so reconcile
// proves an open one and a test controls which entry is oldest.
func seedLiveDraft(t *testing.T, f draftFixture, uidValidity uint32, i int, stage DraftStage, at time.Time) {
	t.Helper()
	subject := fmt.Sprintf("Seed %03d", i)
	uid := f.mem.append("Drafts", rawMessage("Thane <thane@example.com>", "alice@example.com", subject, "Seeded."), imap.FlagDraft)
	seedDraft(t, f.svc, draftEntry{ID: fmt.Sprintf("seed-%03d", i), Account: "primary", Stage: stage, Folder: "Drafts", UIDValidity: uidValidity, UID: uid, MessageID: messageIDFor(subject), UpdatedAt: at})
}

// TestDraftLedgerCapsEachAccount pins the 200-entry cap: a new draft on
// a full account forgets the oldest closed entry, never an open one,
// because an open entry is all that marks a draft still in the folder as
// Thane's; and an account whose 200 drafts are all open refuses a new
// draft with route draft_limit, writing nothing.
func TestDraftLedgerCapsEachAccount(t *testing.T) {
	tests := []struct {
		name        string
		closed      []int
		wantRoute   string
		wantEvicted []string
		wantKept    []string
	}{
		{"closed entries go first, oldest first", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, "", []string{"seed-000"}, []string{"seed-001", "seed-009", "seed-010"}},
		{"an open entry is never evicted", []int{100}, "", []string{"seed-100"}, []string{"seed-000", "seed-099", "seed-199"}},
		{"200 open drafts refuse a new one", nil, RouteDraftLimit, nil, []string{"seed-000", "seed-199"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newDraftFixture(t, nil)
			acct, _ := f.svc.ResolveAccount(ctx, "primary")
			status, err := acct.Client.MailboxStatus(ctx, "Drafts")
			if err != nil {
				t.Fatal(err)
			}
			base := time.Now().Add(-time.Hour).UTC()
			for i := range maxDraftEntriesPerAccount {
				stage := DraftStageOpen
				if slices.Contains(tt.closed, i) {
					stage = DraftStageGone
				}
				seedLiveDraft(t, f, status.UIDValidity, i, stage, base.Add(time.Duration(i)*time.Second))
			}
			_, uidNext := f.drafts(t)

			out, err := f.svc.ToolProvider().HandleSend(ctx, sendArgs("alice@example.com"))
			entries, _ := f.svc.draftEntries("primary")
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.ID)
			}
			if len(entries) != maxDraftEntriesPerAccount {
				t.Errorf("account keeps %d entries, want %d", len(entries), maxDraftEntriesPerAccount)
			}
			if tt.wantRoute != "" {
				refusal := refusalOf(t, err)
				if refusal.Decision.Route != tt.wantRoute {
					t.Errorf("route = %s, want %s", refusal.Decision.Route, tt.wantRoute)
				}
				mustContain(t, refusal.Message, "200", "email_draft_withdraw")
				if _, afterNext := f.drafts(t); afterNext != uidNext {
					t.Errorf("a refused draft wrote to the drafts folder: uidnext %d, was %d", afterNext, uidNext)
				}
			} else if err != nil {
				t.Fatalf("send: %v", err)
			} else if id := decodeSend(t, out).DraftID; !slices.Contains(ids, id) {
				t.Errorf("the new draft %s was not kept", id)
			}
			for _, gone := range tt.wantEvicted {
				if slices.Contains(ids, gone) {
					t.Errorf("%s was kept, want it evicted", gone)
				}
			}
			for _, kept := range tt.wantKept {
				if !slices.Contains(ids, kept) {
					t.Errorf("%s was evicted, want it kept", kept)
				}
			}
		})
	}
}

// TestDraftLedgerFailureSaysTheDraftIsUntracked pins the result of a
// draft the ledger could not record: the draft is in the folder, so the
// result is drafted, but it carries no draft_id, and its note says no
// draft tool can change it and not to draft the message again.
func TestDraftLedgerFailureSaysTheDraftIsUntracked(t *testing.T) {
	f, db := ledgerDBService(t)
	original := f.deliverOriginal("Unrecorded")
	for _, trigger := range []string{
		`CREATE TRIGGER block_ledger_insert BEFORE INSERT ON operational_state WHEN NEW.namespace = 'email_drafts' BEGIN SELECT RAISE(ABORT, 'ledger unavailable'); END`,
		`CREATE TRIGGER block_ledger_update BEFORE UPDATE ON operational_state WHEN NEW.namespace = 'email_drafts' BEGIN SELECT RAISE(ABORT, 'ledger unavailable'); END`,
	} {
		if _, err := db.Exec(trigger); err != nil {
			t.Fatalf("install trigger: %v", err)
		}
	}
	out, err := f.svc.ToolProvider().HandleReply(context.Background(), map[string]any{"uid": float64(original), "body": "Noted."})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	resp := decodeSend(t, out)
	if resp.Disposition != DispositionDrafted || resp.DraftUID == 0 || resp.DraftID != "" {
		t.Fatalf("result = %+v, want drafted with no draft_id", resp)
	}
	mustContain(t, resp.Note, "could not record it", "no draft tool can change it", "do not draft this message again")
}

// ledgerDBService is a drafts-delivery service over a store whose
// database the test holds, so it can read expires_at.
func ledgerDBService(t *testing.T) (draftFixture, *sql.DB) {
	t.Helper()
	mem := newMemIMAP(t)
	smtp := newSMTPFake(t, nil)
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("opstate: %v", err)
	}
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "primary",
		IMAP:        mem.imapConfig(),
		SMTP:        smtp.config("thane", "pw"),
		DefaultFrom: "Thane <thane@example.com>",
	}}}
	cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
	svc, err := NewService(cfg, ServiceDependencies{State: store, MessageBus: messages.NewBus(nil), Contacts: identityStub(), Logger: quietSlog()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return draftFixture{svc: svc, mem: mem}, db
}

// TestDraftLedgerExpiresClosedEntries pins the TTL: an open entry never
// expires, and one that closes expires 14 days later through the store's
// expires_at.
func TestDraftLedgerExpiresClosedEntries(t *testing.T) {
	f, db := ledgerDBService(t)
	resp, _ := f.replyDraft(t, context.Background(), "Expiry check", "Noted.")
	expiresAt := func() sql.NullString {
		t.Helper()
		var v sql.NullString
		if err := db.QueryRow(`SELECT expires_at FROM operational_state WHERE namespace = ? AND key = ?`, draftLedgerNamespace, "primary/"+resp.DraftID).Scan(&v); err != nil {
			t.Fatalf("read expires_at: %v", err)
		}
		return v
	}
	if v := expiresAt(); v.Valid {
		t.Fatalf("open entry expires_at = %q, want none", v.String)
	}

	if _, err := f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": resp.DraftID}); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	v := expiresAt()
	if !v.Valid {
		t.Fatal("withdrawn entry has no expires_at")
	}
	at, err := time.Parse(time.RFC3339, v.String)
	if err != nil {
		t.Fatalf("expires_at %q: %v", v.String, err)
	}
	if want := time.Now().Add(draftClosedTTL); at.Before(want.Add(-time.Minute)) || at.After(want.Add(time.Minute)) {
		t.Errorf("expires_at = %s, want about %s", at, want)
	}
}
