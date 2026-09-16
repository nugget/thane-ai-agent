package email

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestReconcileClosesDraftsNoLongerThanes pins reconcile: a draft still
// at its recorded UID stays open; one the operator discarded is gone as
// vanished, or as marked_deleted while their client has only flagged it
// \Deleted; one they edited, which their client stores under a new UID,
// is gone as operator_took_over, even before the old copy is expunged;
// and a rebuilt drafts folder closes every entry, because no UID in it
// can be proven.
func TestReconcileClosesDraftsNoLongerThanes(t *testing.T) {
	tests := []struct {
		name       string
		act        func(f draftFixture, uid uint32)
		wantStage  DraftStage
		wantReason string
	}{
		{"untouched draft stays open", func(draftFixture, uint32) {}, DraftStageOpen, ""},
		{"discarded draft vanished", func(f draftFixture, uid uint32) {
			if err := f.mem.operator().discard("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, DraftStageGone, closedVanished},
		{"discarded with its expunge deferred", func(f draftFixture, uid uint32) {
			if err := f.mem.operator().flagDeleted("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, DraftStageGone, closedMarkedDeleted},
		{"edited draft taken over", func(f draftFixture, uid uint32) {
			f.mem.operator().edit("Drafts", uid, operatorDraft("Re: Picnic", messageIDFor("Picnic")))
		}, DraftStageGone, closedOperatorTookOver},
		{"edited with its expunge deferred", func(f draftFixture, uid uint32) {
			f.mem.append("Drafts", operatorDraft("Re: Picnic", messageIDFor("Picnic")), imap.FlagDraft, imap.FlagSeen)
			if err := f.mem.operator().flagDeleted("Drafts", uid); err != nil {
				f.mem.t.Fatal(err)
			}
		}, DraftStageGone, closedOperatorTookOver},
		{"rebuilt folder", func(f draftFixture, _ uint32) { f.mem.recreateFolder("Drafts") }, DraftStageGone, closedUIDValidityChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, nil)
			resp, _ := f.replyDraft(t, context.Background(), "Picnic", "Count me in.")
			tt.act(f, resp.DraftUID)

			listed := f.listDrafts(t, map[string]any{"include_closed": true})
			if len(listed.Drafts) != 1 || len(listed.Errors) != 0 {
				t.Fatalf("email_drafts = %+v, want the one draft", listed)
			}
			row := listed.Drafts[0]
			if row.DraftID != resp.DraftID || row.Stage != tt.wantStage || row.ClosedReason != tt.wantReason {
				t.Errorf("row = %+v, want stage %s reason %q", row, tt.wantStage, tt.wantReason)
			}
			if wantOpen := btoi(tt.wantStage == DraftStageOpen); listed.Open != wantOpen {
				t.Errorf("open = %d, want %d", listed.Open, wantOpen)
			}
			if tt.wantStage != DraftStageOpen {
				if hidden := f.listDrafts(t, nil); len(hidden.Drafts) != 0 {
					t.Errorf("without include_closed = %+v, want no rows", hidden.Drafts)
				}
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
