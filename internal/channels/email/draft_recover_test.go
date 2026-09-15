package email

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// oldAction is what the operator's client did to a draft's old version
// while a revision was interrupted.
type oldAction int

const (
	oldUntouched oldAction = iota
	oldDiscarded
	oldFlagged
	oldEdited
)

// TestReconcileSettlesAnInterruptedRevision pins crash recovery. A
// write-ahead record is settled on the next draft tool call, by the
// phase it reached. Before Thane began removing the old version, any
// absence or \Deleted flag there is the operator's doing, so Thane's new
// version is removed again and the entry closes as held; untouched, the
// revision completes. Once Thane had begun removing the old version,
// its absence or flag is Thane's own, and only an edit under another
// UID shows the operator took over.
func TestReconcileSettlesAnInterruptedRevision(t *testing.T) {
	tests := []struct {
		name        string
		appendNew   bool
		removing    bool
		old         oldAction
		wantStage   DraftStage
		wantReason  string
		wantAdopted bool
		wantOld     bool
		wantNew     bool
	}{
		{"before removing: both versions", true, false, oldUntouched, DraftStageOpen, "", true, false, true},
		{"before removing: only the old version", false, false, oldUntouched, DraftStageOpen, "", false, true, false},
		{"before removing: neither version", false, false, oldDiscarded, DraftStageGone, closedVanished, false, false, false},
		{"before removing: the operator discarded the old version", true, false, oldDiscarded, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"before removing: the operator flagged the old version", true, false, oldFlagged, DraftStageGone, closedTouchedDuringRevision, false, true, false},
		{"before removing: the operator edited the old version", true, false, oldEdited, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"removing: the old version untouched", true, true, oldUntouched, DraftStageOpen, "", true, false, true},
		{"removing: the old version flagged", true, true, oldFlagged, DraftStageOpen, "", true, false, true},
		{"removing: the old version expunged", true, true, oldDiscarded, DraftStageOpen, "", true, false, true},
		{"removing: the operator edited meanwhile", true, true, oldEdited, DraftStageGone, closedTouchedDuringRevision, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			resp, _ := f.replyDraft(t, context.Background(), "Crash window", "First version.")
			e := f.entry(t, resp.DraftID)

			newID, err := freshMessageID(e.From)
			if err != nil {
				t.Fatal(err)
			}
			composed, err := ComposeMessage(ComposeOptions{From: e.From, To: e.To, Subject: e.Subject, Body: "Second version.", InReplyTo: e.InReplyTo, References: e.References, MessageID: newID})
			if err != nil {
				t.Fatal(err)
			}
			e.Pending = &draftPending{MessageID: newID, SHA256: contentSum(composed.Bytes), Body: "Second version.", Revision: draftRevision{By: "email-draft-review", At: time.Now().UTC(), Note: "warmer"}}
			var newUID uint32
			if tt.appendNew {
				newUID = f.mem.append("Drafts", string(composed.Bytes), imap.FlagDraft, imap.FlagSeen)
			}
			if tt.removing {
				e.Pending.NewUID, e.Pending.NewUIDValidity = newUID, e.UIDValidity
			}
			if err := f.svc.saveDraft(&e); err != nil {
				t.Fatal(err)
			}
			op := f.mem.operator()
			var editUID uint32
			switch tt.old {
			case oldDiscarded:
				if err := op.discard("Drafts", e.UID); err != nil {
					t.Fatal(err)
				}
			case oldFlagged:
				if err := op.flagDeleted("Drafts", e.UID); err != nil {
					t.Fatal(err)
				}
			case oldEdited:
				editUID = op.edit("Drafts", e.UID, operatorDraft("Re: Crash window", messageIDFor("Crash window")))
			}

			f.listDrafts(t, map[string]any{"include_closed": true})
			got := f.entry(t, resp.DraftID)
			drafts, _ := f.drafts(t)
			if got.Stage != tt.wantStage || got.ClosedReason != tt.wantReason || got.Pending != nil {
				t.Fatalf("entry = stage %s reason %q pending %+v, want %s %q and no pending record", got.Stage, got.ClosedReason, got.Pending, tt.wantStage, tt.wantReason)
			}
			if tt.wantAdopted {
				if got.UID != newUID || got.MessageID != newID || got.SHA256 != e.Pending.SHA256 || got.Body != "Second version." || got.PreviousBody != "First version." || got.Revisions != 1 || got.lastRevision().By != "email-draft-review" {
					t.Errorf("entry = %+v, want the new version adopted at uid %d", got, newUID)
				}
			} else if got.UID != e.UID || got.Revisions != 0 {
				t.Errorf("entry = %+v, want it left at the old version", got)
			}
			if _, there := drafts[e.UID]; there != tt.wantOld {
				t.Errorf("old version at uid %d present = %v, want %v: %v", e.UID, there, tt.wantOld, drafts)
			}
			if tt.appendNew {
				if _, there := drafts[newUID]; there != tt.wantNew {
					t.Errorf("new version at uid %d present = %v, want %v: %v", newUID, there, tt.wantNew, drafts)
				}
			}
			if _, there := drafts[editUID]; editUID != 0 && !there {
				t.Errorf("the operator's edit at uid %d was removed: %v", editUID, drafts)
			}
		})
	}
}

// TestReconcileSettlesADraftWithAnUnknownUID pins the entry a server
// that returned no APPENDUID leaves behind. Reconcile finds the draft by
// its Message-ID and proves it by its bytes, so the entry learns its UID;
// a draft the operator discarded closes, so it cannot hold the message's
// one draft forever; and one whose bytes cannot be proven stays open,
// counting as the message's draft while the draft tools refuse it
// without closing it.
func TestReconcileSettlesADraftWithAnUnknownUID(t *testing.T) {
	tests := []struct {
		name       string
		act        func(f draftFixture, uid uint32, e *draftEntry)
		wantStage  DraftStage
		wantReason string
		wantUID    bool
	}{
		{"still there: its UID is learned", func(draftFixture, uint32, *draftEntry) {}, DraftStageOpen, "", true},
		{"discarded: it closes", func(f draftFixture, uid uint32, _ *draftEntry) {
			if err := f.mem.operator().discard("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, DraftStageGone, closedVanished, false},
		{"marked \\Deleted: it closes", func(f draftFixture, uid uint32, _ *draftEntry) {
			if err := f.mem.operator().flagDeleted("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, DraftStageGone, closedMarkedDeleted, false},
		{"bytes cannot be proven: it stays open", func(_ draftFixture, _ uint32, e *draftEntry) {
			e.SHA256 = strings.Repeat("0", 64)
		}, DraftStageOpen, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			resp, original := f.replyDraft(t, context.Background(), "Old server", "Hello.")
			e := f.entry(t, resp.DraftID)
			uid := e.UID
			e.UID, e.UIDValidity = 0, 0
			tt.act(f, uid, &e)
			if err := f.svc.saveDraft(&e); err != nil {
				t.Fatal(err)
			}

			f.listDrafts(t, map[string]any{"include_closed": true})
			got := f.entry(t, resp.DraftID)
			if got.Stage != tt.wantStage || got.ClosedReason != tt.wantReason {
				t.Fatalf("entry = stage %s reason %q, want %s %q", got.Stage, got.ClosedReason, tt.wantStage, tt.wantReason)
			}
			if learned := got.UID == uid && got.UIDValidity != 0; learned != tt.wantUID || (!tt.wantUID && got.UID != 0) {
				t.Errorf("entry uid %d validity %d, want learned=%v (uid %d)", got.UID, got.UIDValidity, tt.wantUID, uid)
			}

			_, err := f.svc.ToolProvider().HandleReply(context.Background(), map[string]any{"uid": float64(original), "body": "A second answer."})
			if tt.wantStage != DraftStageOpen {
				if err != nil {
					t.Errorf("a reply after the draft closed = %v, want it drafted", err)
				}
				return
			}
			if refusal := refusalOf(t, err); refusal.Decision.Route != RouteDraftOpen {
				t.Errorf("second reply route = %s, want %s", refusal.Decision.Route, RouteDraftOpen)
			}
			if tt.wantUID {
				return
			}
			mustContain(t, err.Error(), "never reported that draft's UID")
			for name, call := range map[string]func() error{
				"revise": func() error { _, err := f.revise(context.Background(), resp.DraftID, "Changed.", ""); return err },
				"withdraw": func() error {
					_, err := f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": resp.DraftID})
					return err
				},
			} {
				refusal := draftRefusalOf(t, call())
				if refusal.View.Reason != draftRefusedNoUIDPlus {
					t.Errorf("%s refusal = %+v, want no_uidplus", name, refusal.View)
				}
				mustContain(t, refusal.Message, "never reported this draft's UID", "nothing was changed")
				if e := f.entry(t, resp.DraftID); e.Stage != DraftStageOpen {
					t.Errorf("%s closed the entry: %s %s", name, e.Stage, e.ClosedReason)
				}
			}
		})
	}
}
