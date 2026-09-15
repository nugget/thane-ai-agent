package email

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	oldSent
	oldFlagged
	oldEdited
)

// TestReconcileSettlesAnInterruptedRevision pins crash recovery. A
// write-ahead record is settled on the next draft tool call, by the
// phase it reached. Before retiring, any absence or \Deleted flag on the
// old version is the operator's doing: Thane's new version is removed
// again, nothing is adopted, nothing the operator flagged is expunged,
// and the entry closes as held; untouched, the revision rolls back to
// the old version, which stays open. Retiring is recorded only once
// Thane saw its own \Deleted flag hold on the old version, so there the
// old version's absence or flag is Thane's own and the revision
// completes, unless an edit under another UID shows the operator took
// over, or the flag was cleared since, which rolls back. A record with a
// new UID and no phase reads as appended.
func TestReconcileSettlesAnInterruptedRevision(t *testing.T) {
	tests := []struct {
		name        string
		phase       string
		appendNew   bool
		old         oldAction
		wantStage   DraftStage
		wantReason  string
		wantAdopted bool
		wantOld     bool
		wantNew     bool
	}{
		{"appending: both versions", pendingAppending, true, oldUntouched, DraftStageOpen, "", false, true, false},
		{"appending: only the old version", pendingAppending, false, oldUntouched, DraftStageOpen, "", false, true, false},
		{"appending: neither version", pendingAppending, false, oldDiscarded, DraftStageGone, closedVanished, false, false, false},
		{"appending: the operator discarded the old version", pendingAppending, true, oldDiscarded, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"appending: the operator flagged the old version", pendingAppending, true, oldFlagged, DraftStageGone, closedTouchedDuringRevision, false, true, false},
		{"appending: the operator edited the old version", pendingAppending, true, oldEdited, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"appended: both versions untouched", pendingAppended, true, oldUntouched, DraftStageOpen, "", false, true, false},
		{"appended: the operator sent the old version", pendingAppended, true, oldSent, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"appended: the operator discarded the old version", pendingAppended, true, oldDiscarded, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"appended: the old version flagged, by the operator or Thane's unconfirmed store", pendingAppended, true, oldFlagged, DraftStageGone, closedTouchedDuringRevision, false, true, false},
		{"appended: the operator edited the old version", pendingAppended, true, oldEdited, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"a record with a new UID and no phase reads as appended", "", true, oldDiscarded, DraftStageGone, closedTouchedDuringRevision, false, false, false},
		{"retiring: Thane's flag on the old version cleared since", pendingRetiring, true, oldUntouched, DraftStageOpen, "", false, true, false},
		{"retiring: the old version flagged", pendingRetiring, true, oldFlagged, DraftStageOpen, "", true, false, true},
		{"retiring: the old version expunged", pendingRetiring, true, oldDiscarded, DraftStageOpen, "", true, false, true},
		{"retiring: the operator edited meanwhile", pendingRetiring, true, oldEdited, DraftStageGone, closedTouchedDuringRevision, false, false, false},
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
			e.Pending = &draftPending{Phase: tt.phase, MessageID: newID, SHA256: contentSum(composed.Bytes), Body: "Second version.", Revision: draftRevision{By: "email-draft-review", At: time.Now().UTC(), Note: "warmer"}}
			var newUID uint32
			if tt.appendNew {
				newUID = f.mem.append("Drafts", string(composed.Bytes), imap.FlagDraft, imap.FlagSeen)
			}
			if tt.phase != pendingAppending {
				e.Pending.NewUID, e.Pending.NewUIDValidity = newUID, e.UIDValidity
			}
			if err := f.svc.saveDraft(&e); err != nil {
				t.Fatal(err)
			}
			editUID := f.mem.operator().actOnOld(e.UID, tt.old)

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
			} else if got.UID != e.UID || got.MessageID != e.MessageID || got.Body != "First version." || got.Revisions != 0 {
				t.Errorf("entry = %+v, want it left at the old version", got)
			}
			if _, there := drafts[e.UID]; there != tt.wantOld {
				t.Errorf("old version at uid %d present = %v, want %v: %v", e.UID, there, tt.wantOld, drafts)
			}
			if tt.wantOld && tt.old == oldFlagged {
				if msg := f.readDraft(t, e.UID); !slices.Contains(msg.Flags, string(imap.FlagDeleted)) {
					t.Errorf("old version flags = %v, want the operator's \\Deleted left as it was", msg.Flags)
				}
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

// TestReviseRecordsEachPhaseBeforeItsStep pins the write-ahead record a
// live revision leaves at each step: appending when the new version's
// APPEND reaches the server, appended with the new UID when the old
// version is checked and still when its \Deleted flag is read back, and
// retiring by the time the old version is expunged, so a crash anywhere
// reads the phase that authorizes no more than was done.
func TestReviseRecordsEachPhaseBeforeItsStep(t *testing.T) {
	f := newDraftFixture(t, nil)
	resp, _ := f.replyDraft(t, context.Background(), "Phases", "First version.")

	var mu sync.Mutex
	var seen []string
	record := func(step string) {
		e, ok, err := f.svc.draftByID(resp.DraftID)
		line := step + ": no record"
		if err == nil && ok && e.Pending != nil {
			line = fmt.Sprintf("%s: %s new_uid=%v", step, e.Pending.Phase, e.Pending.NewUID != 0)
		}
		mu.Lock()
		seen = append(seen, line)
		mu.Unlock()
	}
	f.mem.onNextAppend(func(string, imap.UID) {
		record("append")
		f.mem.onNextFetch(func() {
			record("check")
			f.mem.onNextFetch(func() { record("flag read-back") })
		})
		f.mem.onNextExpunge(func() { record("expunge") })
	})

	if _, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "Second version.", ""); err != nil {
		t.Fatalf("revise: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"append: appending new_uid=false", "check: appended new_uid=true", "flag read-back: appended new_uid=true", "expunge: retiring new_uid=true"}
	if !slices.Equal(seen, want) {
		t.Errorf("records seen = %q, want %q", seen, want)
	}
	if e := f.entry(t, resp.DraftID); e.Pending != nil || e.Revisions != 1 {
		t.Errorf("entry = revisions %d pending %+v, want the revision recorded and the record cleared", e.Revisions, e.Pending)
	}
}

// TestReviseWhoseStoreFailsLeavesTheAppendedPhase pins why the retiring
// record waits for the flag's read-back. Thane's STORE \Deleted on the
// old version fails, as a dropped connection, a cancelled turn, or a
// server NO would fail it, so the revision errors with its record at
// appended, and whatever the operator then does to the old version is
// theirs: left alone, the revision rolls back; sent, discarded, or
// marked \Deleted, the entry closes held, Thane's new version is removed
// and never adopted, and nothing the operator marked is expunged.
func TestReviseWhoseStoreFailsLeavesTheAppendedPhase(t *testing.T) {
	tests := []struct {
		name       string
		old        oldAction
		wantStage  DraftStage
		wantReason string
		wantOld    bool
	}{
		{"the operator leaves the old version", oldUntouched, DraftStageOpen, "", true},
		{"the operator sends the old version", oldSent, DraftStageGone, closedTouchedDuringRevision, false},
		{"the operator discards the old version", oldDiscarded, DraftStageGone, closedTouchedDuringRevision, false},
		{"the operator marks the old version \\Deleted", oldFlagged, DraftStageGone, closedTouchedDuringRevision, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			resp, _ := f.replyDraft(t, context.Background(), "Crash window", "First version.")
			e := f.entry(t, resp.DraftID)

			f.mem.onNextStore(func() error { return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "STORE unavailable"} })
			if _, err := f.revise(loopCtx("email-draft-review"), resp.DraftID, "Second version.", ""); err == nil {
				t.Fatal("revise succeeded, want the failed STORE to fail it")
			}
			pending := f.entry(t, resp.DraftID).Pending
			if pending == nil || pending.phase() != pendingAppended || pending.NewUID == 0 {
				t.Fatalf("record after the failed STORE = %+v, want phase appended with the new UID", pending)
			}
			newUID := pending.NewUID
			f.mem.operator().actOnOld(e.UID, tt.old)

			f.listDrafts(t, map[string]any{"include_closed": true})
			got := f.entry(t, resp.DraftID)
			drafts, _ := f.drafts(t)
			if got.Stage != tt.wantStage || got.ClosedReason != tt.wantReason || got.Pending != nil {
				t.Fatalf("entry = stage %s reason %q pending %+v, want %s %q and no pending record", got.Stage, got.ClosedReason, got.Pending, tt.wantStage, tt.wantReason)
			}
			if got.UID != e.UID || got.MessageID != e.MessageID || got.Body != "First version." || got.Revisions != 0 {
				t.Errorf("entry = %+v, want it left at the old version, never the new one adopted", got)
			}
			if _, there := drafts[newUID]; there {
				t.Errorf("Thane's new version at uid %d was left in Drafts: %v", newUID, drafts)
			}
			if _, there := drafts[e.UID]; there != tt.wantOld {
				t.Errorf("old version at uid %d present = %v, want %v: %v", e.UID, there, tt.wantOld, drafts)
			}
			if tt.old == oldFlagged {
				if msg := f.readDraft(t, e.UID); !slices.Contains(msg.Flags, string(imap.FlagDeleted)) {
					t.Errorf("old version flags = %v, want the operator's \\Deleted left as it was", msg.Flags)
				}
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
