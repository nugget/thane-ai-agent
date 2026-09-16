package email

import (
	"context"
	"testing"
	"time"
)

// TestDraftLedgerLocksPerAccount pins that the ledger lock is per
// account: while the post-poll reconcile waits on one account's slow
// drafts folder, a draft on another account does not wait for it.
func TestDraftLedgerLocksPerAccount(t *testing.T) {
	second := newMemIMAP(t)
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
		acct := cfg.Accounts[0]
		acct.Name = "second"
		acct.IMAP = second.imapConfig()
		cfg.Accounts = append(cfg.Accounts, acct)
	})
	f := draftFixture{svc: svc, mem: mem}
	f.replyDraft(t, loopCtx(OwnerTriageLoopName), "Saturday", "Yes, Saturday works.")
	secondUID := second.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Sunday", "And Sunday?"))

	holding := make(chan struct{})
	mem.onNextFetch(func() {
		close(holding)
		time.Sleep(2 * time.Second) // primary's drafts folder is slow
	})
	done := make(chan struct{})
	go func() {
		svc.pollUpkeep(context.Background(), nil)
		close(done)
	}()
	select {
	case <-holding:
	case <-time.After(5 * time.Second):
		t.Fatal("the post-poll reconcile never fetched primary's drafts folder")
	}

	start := time.Now()
	out, err := svc.ToolProvider().HandleReply(loopCtx(OwnerTriageLoopName), map[string]any{"account": "second", "uid": float64(secondUID), "body": "Sunday too."})
	elapsed := time.Since(start)
	<-done
	if err != nil {
		t.Fatalf("reply on second: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted {
		t.Fatalf("reply on second = %+v", resp)
	}
	if elapsed > time.Second {
		t.Fatalf("a draft on account %q waited %s for the post-poll reconcile of account %q", "second", elapsed.Round(time.Millisecond), "primary")
	}
}

// TestPollUpkeepSkipsFailedAccounts pins that the post-poll reconcile
// leaves an account whose poll just failed for the next poll, so an
// unreachable server is not waited on twice, and reconciles the rest.
func TestPollUpkeepSkipsFailedAccounts(t *testing.T) {
	tests := []struct {
		name      string
		failed    map[string]bool
		wantStage DraftStage
	}{
		{"the account's poll failed", map[string]bool{"primary": true}, DraftStageOpen},
		{"another account's poll failed", map[string]bool{"second": true}, DraftStageGone},
		{"no poll failed", nil, DraftStageGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := reviewFixture(t, "", nil)
			resp, _ := f.replyDraft(t, loopCtx(OwnerTriageLoopName), "Dinner", "Dinner at seven works.")
			if err := f.mem.operator().discard("Drafts", resp.DraftUID); err != nil {
				t.Fatalf("operator discard: %v", err)
			}
			f.svc.pollUpkeep(context.Background(), tt.failed)
			if got := f.entry(t, resp.DraftID).Stage; got != tt.wantStage {
				t.Errorf("stage after upkeep = %s, want %s", got, tt.wantStage)
			}
		})
	}
}

// TestPollerSkipsReconcileForAFailedAccount pins the poller's side: an
// account whose poll fails in a cycle is not reconciled in that cycle.
func TestPollerSkipsReconcileForAFailedAccount(t *testing.T) {
	f, _ := reviewFixture(t, "", nil)
	resp, _ := f.replyDraft(t, loopCtx(OwnerTriageLoopName), "Dinner", "Dinner at seven works.")
	if err := f.mem.operator().discard("Drafts", resp.DraftUID); err != nil {
		t.Fatalf("operator discard: %v", err)
	}
	if err := f.mem.user.Delete("INBOX"); err != nil { // the account's poll now fails
		t.Fatalf("delete INBOX: %v", err)
	}
	if _, err := f.svc.CheckNewMessages(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := f.entry(t, resp.DraftID).Stage; got != DraftStageOpen {
		t.Errorf("stage after a failed poll = %s, want open: the reconcile ran for an account whose poll had just failed", got)
	}
}
