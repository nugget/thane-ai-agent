package email

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// TestReviseReplacesTheDraft pins a revision end to end: the old UID is
// gone, the new UID holds the new body under a fresh Message-ID with the
// same recipients, subject, and threading, and the ledger records the
// new identity, the previous body, and who revised it.
func TestReviseReplacesTheDraft(t *testing.T) {
	f := newDraftFixture(t, nil)
	resp, _ := f.replyDraft(t, context.Background(), "Venue question", "First draft of the answer.")
	before := f.entry(t, resp.DraftID)
	oldMsg := f.readDraft(t, before.UID)

	out, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "The venue is **confirmed** for Saturday.", "confirmed the venue")
	if err != nil {
		t.Fatalf("revise: %v", err)
	}
	var result draftRevised
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("revise result is not JSON: %v\n%s", err, out)
	}
	if result.Action != "revised" || result.Revision != 1 || result.ReplacedUID != before.UID || result.UID == before.UID || result.UID == 0 {
		t.Fatalf("result = %+v, want revision 1 replacing uid %d", result, before.UID)
	}
	if result.MessageID == "" || sameMessageID(result.MessageID, before.MessageID) {
		t.Errorf("new message_id = %q, want a fresh one, never the old %q", result.MessageID, before.MessageID)
	}

	drafts, _ := f.drafts(t)
	if _, ok := drafts[before.UID]; ok {
		t.Errorf("old version at uid %d survived: %v", before.UID, drafts)
	}
	if len(drafts) != 1 || !sameMessageID(drafts[result.UID], result.MessageID) {
		t.Fatalf("drafts = %v, want only the new version at uid %d", drafts, result.UID)
	}
	msg := f.readDraft(t, result.UID)
	mustContain(t, msg.TextBody, "The venue is confirmed for Saturday.")
	if strings.Contains(msg.TextBody, "First draft") {
		t.Errorf("new body kept the old text: %q", msg.TextBody)
	}
	if msg.Subject != oldMsg.Subject || !slices.Equal(addressStrings(msg.To), addressStrings(oldMsg.To)) || !slices.Equal(msg.InReplyTo, oldMsg.InReplyTo) || !slices.Equal(msg.References, oldMsg.References) || msg.From != oldMsg.From {
		t.Errorf("headers changed: got subject %q to %v in_reply_to %v refs %v, want %q %v %v %v", msg.Subject, msg.To, msg.InReplyTo, msg.References, oldMsg.Subject, oldMsg.To, oldMsg.InReplyTo, oldMsg.References)
	}
	if !slices.Contains(msg.Flags, string(imap.FlagDraft)) || !slices.Contains(msg.Flags, string(imap.FlagSeen)) {
		t.Errorf("new version flags = %v, want \\Draft and \\Seen", msg.Flags)
	}

	after := f.entry(t, resp.DraftID)
	if after.UID != result.UID || !sameMessageID(after.MessageID, result.MessageID) || after.UIDValidity != before.UIDValidity || after.Stage != DraftStageOpen || after.Pending != nil {
		t.Errorf("ledger identity = %+v, want the new version", after)
	}
	if after.Body != "The venue is **confirmed** for Saturday." || after.PreviousBody != "First draft of the answer." || after.Revisions != 1 {
		t.Errorf("ledger bodies = %q / %q revisions %d", after.Body, after.PreviousBody, after.Revisions)
	}
	if last := after.lastRevision(); len(after.History) != 2 || last.By != "email-draft-review" || last.Note != "confirmed the venue" {
		t.Errorf("history = %+v, want the revision by email-draft-review", after.History)
	}
}

// TestReviseRefusesADraftTheOperatorChanged pins the identity proof: a
// draft the operator edited is held, one they discarded is gone, and in
// a rebuilt drafts folder the operator's draft at the recorded UID is
// never touched. A refused revision writes nothing: the folder's
// messages and its UIDNEXT are unchanged.
func TestReviseRefusesADraftTheOperatorChanged(t *testing.T) {
	tests := []struct {
		name       string
		act        func(f draftFixture, uid uint32)
		wantReason string
		wantClosed string
	}{
		{"edited in the operator's client", func(f draftFixture, uid uint32) {
			f.mem.operator().edit("Drafts", uid, operatorDraft("Re: Gate code", messageIDFor("Gate code")))
		}, draftRefusedHeld, closedOperatorTookOver},
		{"discarded by the operator", func(f draftFixture, uid uint32) {
			if err := f.mem.operator().discard("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, draftRefusedGone, closedVanished},
		{"discarded with its expunge deferred", func(f draftFixture, uid uint32) {
			if err := f.mem.operator().flagDeleted("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, draftRefusedGone, closedMarkedDeleted},
		{"edited with its expunge deferred", func(f draftFixture, uid uint32) {
			f.mem.append("Drafts", operatorDraft("Re: Gate code", messageIDFor("Gate code")), imap.FlagDraft, imap.FlagSeen)
			if err := f.mem.operator().flagDeleted("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, draftRefusedHeld, closedOperatorTookOver},
		{"folder rebuilt with the operator's draft at the same UID", func(f draftFixture, uid uint32) {
			f.mem.recreateFolder("Drafts")
			if got := f.mem.append("Drafts", operatorDraft("Unrelated note", "")); got != uid {
				f.mem.t.Fatalf("operator draft got uid %d, want the recorded uid %d", got, uid)
			}
		}, draftRefusedGone, closedUIDValidityChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			resp, _ := f.replyDraft(t, context.Background(), "Gate code", "The code is on the fridge.")
			tt.act(f, resp.DraftUID)
			drafts, uidNext := f.drafts(t)

			_, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "Rewritten.", "")
			refusal := draftRefusalOf(t, err)
			if refusal.View.Reason != tt.wantReason || refusal.View.ClosedReason != tt.wantClosed || refusal.View.DraftID != resp.DraftID {
				t.Errorf("refusal = %+v, want reason %s closed %s", refusal.View, tt.wantReason, tt.wantClosed)
			}
			mustContain(t, refusal.Message, resp.DraftID, "nothing was changed")
			after, afterNext := f.drafts(t)
			if !maps.Equal(after, drafts) || afterNext != uidNext {
				t.Errorf("refused revision changed the folder: %v (uidnext %d), was %v (uidnext %d)", after, afterNext, drafts, uidNext)
			}
			if e := f.entry(t, resp.DraftID); e.Stage != DraftStageGone || e.ClosedReason != tt.wantClosed {
				t.Errorf("entry = stage %s reason %s, want gone %s", e.Stage, e.ClosedReason, tt.wantClosed)
			}
		})
	}
}

// TestReviseLeavesTheOperatorsDeletedDraftAlone pins UID EXPUNGE: a
// draft the operator flagged \Deleted but has not expunged survives a
// revision, because Thane expunges only the one UID it replaced.
func TestReviseLeavesTheOperatorsDeletedDraftAlone(t *testing.T) {
	f := newDraftFixture(t, nil)
	operatorUID := f.mem.append("Drafts", operatorDraft("Half-finished note", ""), imap.FlagDraft, imap.FlagDeleted)
	resp, _ := f.replyDraft(t, context.Background(), "Dinner", "See you at seven.")

	if _, err := f.revise(context.Background(), resp.DraftID, "See you at half past seven.", "later time"); err != nil {
		t.Fatalf("revise: %v", err)
	}
	drafts, _ := f.drafts(t)
	if _, ok := drafts[operatorUID]; !ok {
		t.Fatalf("the operator's \\Deleted draft at uid %d was expunged: %v", operatorUID, drafts)
	}
	if msg := f.readDraft(t, operatorUID); !slices.Contains(msg.Flags, string(imap.FlagDeleted)) {
		t.Errorf("operator draft flags = %v, want its \\Deleted left as it was", msg.Flags)
	}
}

// TestReviseBacksOutWhenTheOperatorActsMidway pins the \Deleted
// read-back: when the operator removes the old version after Thane's new
// version was appended and before the old one is flagged, the flag never
// holds, so Thane removes its own new version again, touches nothing
// else, and closes the entry as held.
func TestReviseBacksOutWhenTheOperatorActsMidway(t *testing.T) {
	f := newDraftFixture(t, nil)
	resp, _ := f.replyDraft(t, context.Background(), "Keys", "Under the mat.")
	op := f.mem.operator()
	var newUID imap.UID
	f.mem.onNextAppend(func(folder string, uid imap.UID) {
		newUID = uid
		if err := op.discard("Drafts", resp.DraftUID); err != nil {
			t.Error(err)
		}
	})

	_, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "With the neighbour.", "")
	refusal := draftRefusalOf(t, err)
	if refusal.View.Reason != draftRefusedHeld || refusal.View.ClosedReason != closedTouchedDuringRevision {
		t.Errorf("refusal = %+v, want held, operator_touched_during_revision", refusal.View)
	}
	drafts, _ := f.drafts(t)
	if newUID == 0 || len(drafts) != 0 {
		t.Errorf("drafts = %v after Thane's new version at uid %d, want neither version left", drafts, newUID)
	}
	if e := f.entry(t, resp.DraftID); e.Stage != DraftStageGone || e.Pending != nil {
		t.Errorf("entry = stage %s pending %+v, want gone with no pending record", e.Stage, e.Pending)
	}
}

// TestReviseBacksOutWhenTheOperatorFlagsTheDraftMidway pins the check
// before the \Deleted store: when the operator's client flags the old
// version \Deleted, without expunging it, after Thane's new version was
// appended, that flag is theirs, so Thane removes its own new version
// again and leaves theirs, rather than expunging the old version on the
// strength of a flag it did not set.
func TestReviseBacksOutWhenTheOperatorFlagsTheDraftMidway(t *testing.T) {
	f := newDraftFixture(t, nil)
	resp, _ := f.replyDraft(t, context.Background(), "Parcel", "It is on the porch.")
	op := f.mem.operator()
	var newUID imap.UID
	f.mem.onNextAppend(func(_ string, uid imap.UID) {
		newUID = uid
		if err := op.flagDeleted("Drafts", resp.DraftUID); err != nil {
			t.Error(err)
		}
	})

	_, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "It is with the neighbour.", "")
	refusal := draftRefusalOf(t, err)
	if refusal.View.Reason != draftRefusedHeld || refusal.View.ClosedReason != closedTouchedDuringRevision {
		t.Errorf("refusal = %+v, want held, operator_touched_during_revision", refusal.View)
	}
	drafts, _ := f.drafts(t)
	if _, left := drafts[uint32(newUID)]; newUID == 0 || left {
		t.Errorf("Thane's new version at uid %d was left: %v", newUID, drafts)
	}
	if _, kept := drafts[resp.DraftUID]; !kept {
		t.Errorf("the draft the operator flagged at uid %d was expunged: %v", resp.DraftUID, drafts)
	}
}

// TestReviseBacksOutWhenTheOperatorActsBetweenCheckAndStore pins the
// \Deleted read-back: the operator removes the old version after Thane
// checked it and before Thane flags it, so the store touches nothing,
// the flag never reads back, and Thane removes its own new version
// instead of adopting it.
func TestReviseBacksOutWhenTheOperatorActsBetweenCheckAndStore(t *testing.T) {
	f := newDraftFixture(t, nil)
	resp, _ := f.replyDraft(t, context.Background(), "Lamp", "Leave it on.")
	op := f.mem.operator()
	var newUID imap.UID
	f.mem.onNextAppend(func(_ string, uid imap.UID) {
		newUID = uid
		f.mem.onNextFetch(func() {
			if err := op.discard("Drafts", resp.DraftUID); err != nil {
				t.Error(err)
			}
		})
	})

	_, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "Switch it off.", "")
	refusal := draftRefusalOf(t, err)
	if refusal.View.Reason != draftRefusedHeld || refusal.View.ClosedReason != closedTouchedDuringRevision {
		t.Errorf("refusal = %+v, want held, operator_touched_during_revision", refusal.View)
	}
	if drafts, _ := f.drafts(t); newUID == 0 || len(drafts) != 0 {
		t.Errorf("drafts = %v after Thane's new version at uid %d, want neither version left", drafts, newUID)
	}
}

// TestReviseRefusesWithoutUIDPLUS pins step one: a server that
// advertises neither UIDPLUS nor IMAP4rev2 cannot say which UID a new
// version got or expunge one UID alone, so revise is refused naming the
// extension, and nothing changes.
func TestReviseRefusesWithoutUIDPLUS(t *testing.T) {
	mem := newMemIMAP(t, func(o *memIMAPOptions) { o.rev1Only = true })
	smtp := newSMTPFake(t, nil)
	cfg := Config{Accounts: []AccountConfig{{
		Name:         "primary",
		IMAP:         mem.imapConfig(),
		SMTP:         smtp.config("thane", "pw"),
		DefaultFrom:  "Thane <thane@example.com>",
		DraftsFolder: "Drafts",
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

	_, err = f.revise(context.Background(), resp.DraftID, "Hello again.", "")
	refusal := draftRefusalOf(t, err)
	if refusal.View.Reason != draftRefusedNoUIDPlus {
		t.Errorf("refusal = %+v, want no_uidplus", refusal.View)
	}
	mustContain(t, refusal.Message, "UIDPLUS", "nothing was changed")
	after, afterNext := f.drafts(t)
	if !maps.Equal(after, drafts) || afterNext != uidNext {
		t.Errorf("refused revision changed the folder: %v, was %v", after, drafts)
	}
	if e := f.entry(t, resp.DraftID); e.Stage != DraftStageOpen || e.Pending != nil {
		t.Errorf("entry = stage %s pending %+v, want it open and untouched", e.Stage, e.Pending)
	}
}
