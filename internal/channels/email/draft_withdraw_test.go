package email

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// TestWithdrawCountsOnlyAMoveThatCarriedTheDraft pins the proof behind
// action withdrawn: the server's COPYUID for the draft's UID, and
// nothing else. A draft the operator sent or discarded between Thane's
// proof and its MOVE moves as nothing while the server still answers OK,
// and the operator's client may have filed the draft's exact bytes in
// Trash on the way, so a move without that report is unconfirmed, never
// says the draft will not be sent, and leaves the entry for reconcile,
// which closes it as gone, or held when the operator's edit is in
// Drafts; a draft still in Drafts after an unreported move stays open.
func TestWithdrawCountsOnlyAMoveThatCarriedTheDraft(t *testing.T) {
	// The first FETCH of a withdrawal is reconcile's, the second the
	// identity proof's; what happens after it lands between the proof and
	// the MOVE.
	const proofFetch = 2
	tests := []struct {
		name         string
		arrange      func(f draftFixture, op *operatorClient, uid uint32)
		wantAction   string
		wantInTrash  bool
		wantInDrafts bool
		wantNote     []string
		wantStage    DraftStage
		wantReason   string
	}{
		{"COPYUID names the draft", func(draftFixture, *operatorClient, uint32) {}, "withdrawn", true, false,
			[]string{"will not be sent"}, DraftStageWithdrawn, closedWithdrawn},
		{"no COPYUID, though the draft's bytes are in Trash", func(f draftFixture, op *operatorClient, uid uint32) {
			f.mem.onNextMove(func() error { return op.move("Drafts", uid, "Trash") })
		}, "unconfirmed", true, false, []string{"could not confirm", "sent or discarded", "may have filed it there", "not marked withdrawn"}, DraftStageGone, closedVanished},
		{"sent, and the operator's client filed the draft in Trash first", func(f draftFixture, op *operatorClient, uid uint32) {
			f.mem.onNextMove(func() error {
				if err := op.copyTo("Drafts", uid, "Sent"); err != nil {
					return err
				}
				return op.move("Drafts", uid, "Trash")
			})
		}, "unconfirmed", true, false, []string{"could not confirm", "sent or discarded", "may have filed it there", "not marked withdrawn"}, DraftStageGone, closedVanished},
		{"edited, and the operator's client filed the old copy in Trash first", func(f draftFixture, op *operatorClient, uid uint32) {
			f.mem.onNextMove(func() error {
				if err := op.appendDraft("Drafts", operatorDraft("Re: Wrong answer", messageIDFor("Wrong answer"))); err != nil {
					return err
				}
				return op.move("Drafts", uid, "Trash")
			})
		}, "unconfirmed", true, false, []string{"could not confirm", "sent or discarded", "may have filed it there", "not marked withdrawn"}, DraftStageGone, closedOperatorTookOver},
		{"discarded between the proof and the move", func(f draftFixture, op *operatorClient, uid uint32) {
			f.mem.onFetch(proofFetch, func() {
				if err := op.discard("Drafts", uid); err != nil {
					t.Error(err)
				}
			})
		}, "unconfirmed", false, false, []string{"could not confirm", "sent or discarded", "not marked withdrawn"}, DraftStageGone, closedVanished},
		{"sent between the proof and the move", func(f draftFixture, op *operatorClient, uid uint32) {
			f.mem.onFetch(proofFetch, func() {
				if err := op.send("Drafts", uid); err != nil {
					t.Error(err)
				}
			})
		}, "unconfirmed", false, false, []string{"could not confirm", "sent or discarded", "not marked withdrawn"}, DraftStageGone, closedVanished},
		{"discarded before a move that reports no COPYUID", func(f draftFixture, op *operatorClient, uid uint32) {
			f.mem.onFetch(proofFetch, func() {
				if err := op.discard("Drafts", uid); err != nil {
					t.Error(err)
				}
			})
			f.mem.onNextMove(func() error { return nil })
		}, "unconfirmed", false, false, []string{"could not confirm", "sent or discarded"}, DraftStageGone, closedVanished},
		{"a move that reports nothing and moved nothing", func(f draftFixture, _ *operatorClient, _ uint32) {
			f.mem.onNextMove(func() error { return nil })
		}, "unconfirmed", false, true, []string{"could not confirm", "still at uid", "can still be sent"}, DraftStageOpen, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			resp, _ := f.replyDraft(t, context.Background(), "Wrong answer", "Probably not.")
			e := f.entry(t, resp.DraftID)
			tt.arrange(f, f.mem.operator(), e.UID)

			out, err := f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": resp.DraftID, "reason": "moot"})
			if err != nil {
				t.Fatalf("withdraw: %v", err)
			}
			var view draftWithdrawnView
			if err := json.Unmarshal([]byte(out), &view); err != nil {
				t.Fatalf("withdraw result is not JSON: %v\n%s", err, out)
			}
			if view.Action != tt.wantAction || view.DraftID != resp.DraftID {
				t.Fatalf("result = %+v, want action %s", view, tt.wantAction)
			}
			mustContain(t, view.Note, tt.wantNote...)
			if tt.wantAction != "withdrawn" && strings.Contains(view.Note, "will not be sent") {
				t.Errorf("an unconfirmed withdrawal claims the draft will not be sent: %s", view.Note)
			}

			trashUID := uidCarrying(t, f, "Trash", e.MessageID)
			if (trashUID != 0) != tt.wantInTrash {
				t.Errorf("draft in Trash at uid %d, want in Trash = %v", trashUID, tt.wantInTrash)
			}
			if tt.wantAction == "withdrawn" && (!view.TrashUIDKnown || view.TrashUID != trashUID) {
				t.Errorf("result trash_uid %d known %v, want the draft's uid %d in Trash", view.TrashUID, view.TrashUIDKnown, trashUID)
			}
			if tt.wantAction != "withdrawn" && (view.TrashUIDKnown || view.TrashUID != 0) {
				t.Errorf("an unconfirmed withdrawal reports trash_uid %d known %v", view.TrashUID, view.TrashUIDKnown)
			}
			if drafts, _ := f.drafts(t); (drafts[e.UID] != "") != tt.wantInDrafts {
				t.Errorf("draft in Drafts = %v, want %v: %v", drafts[e.UID] != "", tt.wantInDrafts, drafts)
			}

			if got := f.entry(t, resp.DraftID); tt.wantAction != "withdrawn" && (got.Stage != DraftStageOpen || got.WithdrawnTo != nil) {
				t.Errorf("an unconfirmed withdrawal changed the entry: stage %s withdrawn_to %+v", got.Stage, got.WithdrawnTo)
			}
			f.listDrafts(t, map[string]any{"include_closed": true})
			if got := f.entry(t, resp.DraftID); got.Stage != tt.wantStage || got.ClosedReason != tt.wantReason {
				t.Errorf("after reconcile entry = %s %q, want %s %q", got.Stage, got.ClosedReason, tt.wantStage, tt.wantReason)
			}
		})
	}
}

// TestWithdrawRefusesWithoutUIDPLUS pins the refusal a server without
// UIDPLUS gets: it never reports COPYUID, so no move there could be
// confirmed as a withdrawal, and nothing is moved or changed.
func TestWithdrawRefusesWithoutUIDPLUS(t *testing.T) {
	mem := newMemIMAP(t, func(o *memIMAPOptions) { o.rev1Only = true })
	smtp := newSMTPFake(t, nil)
	cfg := Config{Accounts: []AccountConfig{{
		Name:         "primary",
		IMAP:         mem.imapConfig(),
		SMTP:         smtp.config("thane", "pw"),
		DefaultFrom:  "Thane <thane@example.com>",
		DraftsFolder: "Drafts",
		TrashFolder:  "Trash",
	}}}
	cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
	svc, err := NewService(cfg, ServiceDependencies{State: testOpstate(t), MessageBus: messages.NewBus(nil), Contacts: identityStub(), Logger: slog.New(slog.NewTextHandler(discardWriter{}, nil))})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	f := draftFixture{svc: svc, mem: mem}
	resp, _ := f.replyDraft(t, context.Background(), "Old server", "Hello.")
	drafts, uidNext := f.drafts(t)

	_, err = f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": resp.DraftID, "reason": "moot"})
	refusal := draftRefusalOf(t, err)
	if refusal.View.Reason != draftRefusedNoUIDPlus {
		t.Errorf("refusal = %+v, want no_uidplus", refusal.View)
	}
	mustContain(t, refusal.Message, "UIDPLUS", "reports that its move carried this draft", "nothing was changed", "should not be sent")
	after, afterNext := f.drafts(t)
	if !maps.Equal(after, drafts) || afterNext != uidNext {
		t.Errorf("refused withdrawal changed Drafts: %v, was %v", after, drafts)
	}
	if e := f.entry(t, resp.DraftID); e.Stage != DraftStageOpen || e.WithdrawnTo != nil {
		t.Errorf("entry = stage %s withdrawn_to %+v, want it open and untouched", e.Stage, e.WithdrawnTo)
	}
	if uid := uidCarrying(t, f, "Trash", f.entry(t, resp.DraftID).MessageID); uid != 0 {
		t.Errorf("refused withdrawal put the draft in Trash at uid %d", uid)
	}
}

// uidCarrying returns the UID of the message in folder that carries
// messageID, or 0.
func uidCarrying(t *testing.T, f draftFixture, folder, messageID string) uint32 {
	t.Helper()
	acct, err := f.svc.ResolveAccount(context.Background(), "primary")
	if err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: folder, Limit: MaxListLimit})
	if err != nil {
		t.Fatalf("list %s: %v", folder, err)
	}
	for _, env := range listed.Envelopes {
		if sameMessageID(env.MessageID, messageID) {
			return env.UID
		}
	}
	return 0
}
