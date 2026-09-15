package email

import (
	"context"
	"sync"
)

// The draft ledger is locked per account. Every ledger read-modify-write
// holds its account's lock together with the IMAP work that proves it,
// so two calls never race one entry. No ledger operation spans
// accounts, so a slow or unreachable drafts folder holds drafting on its
// own account only. An account's lock is always taken before any
// Client's lock, never after, and no path holds two accounts' locks.

// draftLocks holds one mutex per account, made on first use.
type draftLocks struct {
	mu        sync.Mutex
	byAccount map[string]*sync.Mutex
}

// lockDraftLedger takes an account's draft lock when there is a ledger
// to guard, and returns its release.
func (s *Service) lockDraftLedger(account string) func() {
	if s.state == nil {
		return func() {}
	}
	s.drafts.mu.Lock()
	if s.drafts.byAccount == nil {
		s.drafts.byAccount = make(map[string]*sync.Mutex)
	}
	m, ok := s.drafts.byAccount[account]
	if !ok {
		m = &sync.Mutex{}
		s.drafts.byAccount[account] = m
	}
	s.drafts.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// reconcileDraftsLocked reconciles one account's ledger under its lock.
func (s *Service) reconcileDraftsLocked(ctx context.Context, acct ResolvedAccount) ([]draftEntry, error) {
	unlock := s.lockDraftLedger(acct.Name)
	defer unlock()
	return s.reconcileDrafts(ctx, acct)
}

// lockDraft takes the lock of the account a draft_id belongs to. An
// entry's account never changes, so reading it before the lock is safe,
// and draftFor reads the entry again under the lock and reports any
// error or missing id this read met. Such an id takes a lock no account
// uses.
func (t *Tools) lockDraft(id string) func() {
	s := t.service
	account := ""
	if e, ok, err := s.draftByID(id); err == nil && ok {
		account = e.Account
	}
	return s.lockDraftLedger(account)
}
