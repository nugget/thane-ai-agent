package email

import (
	"context"
	"time"
)

// pollUpkeepAccountTimeout bounds the draft reconcile one poll spends on
// each account. The reconcile holds that account's draft lock, so a slow
// drafts folder delays drafting on that account alone, and for no longer
// than this; a reconcile cut short is finished by the next draft tool
// call, which reconciles first.
const pollUpkeepAccountTimeout = 10 * time.Second

// pollUpkeep keeps fresh, once per poll and after the poll's wakes are
// delivered, what no wake carries: the draft ledger, so an entry whose
// draft the operator sent, edited, or discarded closes within one poll
// rather than on the next draft tool call, and the pending-review
// counts the Email Accounts block renders. failed names the accounts
// whose poll just failed; their ledgers wait for the next poll, so an
// unreachable server is not waited on twice. Failures are logged;
// upkeep never fails the poll.
func (s *Service) pollUpkeep(ctx context.Context, failed map[string]bool) {
	s.reconcileAfterPoll(ctx, failed)
	s.refreshPendingReview(ctx)
}

// reconcileAfterPoll reconciles the ledger of every account with an
// open draft whose poll did not fail. It reads the ledger once to find
// those accounts, so a poll over accounts with nothing open touches no
// drafts folder, and a reconcile itself fetches only the UIDs the ledger
// recorded.
func (s *Service) reconcileAfterPoll(ctx context.Context, failed map[string]bool) {
	if s.state == nil {
		return
	}
	entries, err := s.draftEntries("")
	if err != nil {
		s.logger.Warn("email draft ledger not read after poll", "error", err)
		return
	}
	open := make(map[string]bool)
	for _, e := range entries {
		if e.Stage == DraftStageOpen {
			open[e.Account] = true
		}
	}
	for _, name := range s.AccountNames() {
		switch {
		case !open[name]:
		case failed[name]:
			s.logger.Debug("email draft ledger not reconciled: the account's poll failed", "account", name)
		default:
			s.reconcileAccountAfterPoll(ctx, name)
		}
	}
}

// reconcileAccountAfterPoll reconciles one account's ledger under its
// lock and the upkeep bound.
func (s *Service) reconcileAccountAfterPoll(ctx context.Context, account string) {
	acct, err := s.ResolveAccount(ctx, account)
	if err != nil {
		s.logger.Warn("email draft ledger not reconciled after poll", "account", account, "error", err)
		return
	}
	actx, cancel := context.WithTimeout(ctx, pollUpkeepAccountTimeout)
	defer cancel()
	entries, err := s.reconcileDraftsLocked(actx, acct)
	if err != nil {
		s.logger.Warn("email draft ledger not reconciled after poll", "account", account, "error", err)
		return
	}
	open := 0
	for _, e := range entries {
		if e.Stage == DraftStageOpen {
			open++
		}
	}
	s.logger.Debug("email draft ledger reconciled after poll", "account", account, "open", open)
}
