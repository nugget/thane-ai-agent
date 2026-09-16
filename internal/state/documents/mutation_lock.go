package documents

import (
	"slices"
	"sync"
)

// lockRootMutations takes the mutation lock of every named root and
// returns the release, which the caller defers.
//
// The lock serializes this process's document mutations within one root,
// so a decision a caller made from a document's current content — above
// all the managed-document guards, which read a document's owner and then
// delete, move, or replace it — cannot be invalidated by another tool's
// write landing in between. Every mutation path takes it: the write and
// remove helpers take it for the caller, and doc_delete, doc_move,
// doc_copy, doc_move_section, and doc_copy_section take it around the
// whole read-decide-mutate sequence and call the already-locked helpers.
//
// The lock is held across the root writer's commit and the index refresh
// that follows it, so a contended root costs a waiting mutation the whole
// of both. It is a plain [sync.Mutex] and takes no context, so a caller
// whose tool call is cancelled while queued waits for its turn anyway.
// Nothing under it blocks on refreshMu — see [Store.touchLastRefresh] —
// so the wait is bounded by other mutations, not by a refresh pass.
//
// Duplicates collapse, so a transfer whose source and destination share a
// root takes one lock rather than deadlocking on itself, and roots lock in
// name order, so two transfers between the same pair of roots cannot each
// hold what the other waits for.
//
// The lock coordinates this Store's callers and nothing else. A root
// backed by a revision store gets a second, cross-process guarantee from
// that store's own compare-and-commit; a root that writes straight to the
// filesystem has no revisions to compare, and an editor or a separate
// process writing into it is outside what any in-process lock can protect.
func (s *Store) lockRootMutations(roots ...string) func() {
	ordered := make([]string, 0, len(roots))
	for _, root := range roots {
		if root = normalizeRootName(root); root != "" {
			ordered = append(ordered, root)
		}
	}
	slices.Sort(ordered)
	ordered = slices.Compact(ordered)

	held := make([]*sync.Mutex, 0, len(ordered))
	for _, root := range ordered {
		lock := s.rootMutationLock(root)
		lock.Lock()
		held = append(held, lock)
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}
}

// rootMutationLock returns root's mutation lock, creating it on first use
// so a store needs no knowledge of which roots will ever be written.
func (s *Store) rootMutationLock(root string) *sync.Mutex {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if s.rootMutations == nil {
		s.rootMutations = make(map[string]*sync.Mutex)
	}
	lock, ok := s.rootMutations[root]
	if !ok {
		lock = &sync.Mutex{}
		s.rootMutations[root] = lock
	}
	return lock
}
