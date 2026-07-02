package provenance

import (
	"context"
	"fmt"
)

// SyncMode selects whether a sync pushes local commits back to the remote.
// The zero value is fetch-only — the safe default: an engine constructed
// without an explicit mode never writes to the remote.
type SyncMode int

const (
	// SyncModeFetch pulls fast-forwards from the remote but never pushes.
	SyncModeFetch SyncMode = iota
	// SyncModeBidirectional additionally pushes local commits that are a
	// fast-forward of the remote.
	SyncModeBidirectional
)

// SyncOutcome classifies the result of one sync pass. The values double as
// stable strings for logs and the operability surface.
type SyncOutcome string

const (
	// SyncClean means local and remote already agree (or, in fetch-only
	// mode, the only difference is unpushed local commits — nothing to do).
	SyncClean SyncOutcome = "clean"
	// SyncFastForwarded means the local branch was advanced to the remote.
	SyncFastForwarded SyncOutcome = "fast_forwarded"
	// SyncPushed means local commits were pushed to the remote.
	SyncPushed SyncOutcome = "pushed"
	// SyncDiverged means both sides have unique commits; fast-forward-only
	// sync cannot reconcile and refuses with no side effects.
	SyncDiverged SyncOutcome = "diverged"
	// SyncBlocked means the engine refused to integrate: an incoming commit
	// failed verification, the worktree was dirty, HEAD was detached, or a
	// similar fail-closed condition. Detail explains which.
	SyncBlocked SyncOutcome = "blocked"
	// SyncRemoteBehind means the remote rewound: the fetched remote head is a
	// strict ancestor of the head the caller last saw (LastKnownRemote). The
	// engine refuses with no side effects rather than pushing local commits
	// back over what looks like an intentional history rewrite; the operator
	// resolves it on the workstation.
	SyncRemoteBehind SyncOutcome = "remote_behind"
)

// SyncRequest parameterizes one sync pass. The engine is stateless: every
// field comes from a root's git.remote config, and no cross-pass state is
// carried. The operability layer builds this and serializes passes per root.
type SyncRequest struct {
	// RemoteURL is the git remote (ssh, https, or a local path).
	RemoteURL string
	// Branch is the branch synced on both sides.
	Branch string
	// SSHCommand is the GIT_SSH_COMMAND for an ssh remote (see
	// [BuildSSHCommand]); empty for https or local remotes.
	SSHCommand string
	// Mode selects fetch-only vs bidirectional.
	Mode SyncMode
	// RequireVerify hard-gates every fast-forward on signature verification
	// against the store's allowed-signers trust set — the in-tree
	// .allowed_signers, or a configured out-of-tree anchor. It must be true
	// for any signed root synced from an untrusted remote; it does not
	// downgrade to advisory ("warn") for a worktree-mutating fast-forward.
	RequireVerify bool
	// LastKnownRemote is the remote head SHA the caller observed on its
	// previous pass, or empty on the first. It lets the otherwise-stateless
	// engine detect a rewound remote: if the freshly fetched remote head is a
	// strict ancestor of LastKnownRemote, the pass returns SyncRemoteBehind
	// with no side effects instead of pushing local commits back over the
	// rewind. Cross-pass memory of this value lives in the operability layer.
	LastKnownRemote string
}

// SyncResult reports what one pass observed and did. Once a pass reaches
// classification, Ahead/Behind and the two head SHAs are populated (they feed
// the operability layer's reporting and its deferred rewind detector); a pass
// blocked before then — a detached HEAD or a branch mismatch, caught before
// the heads are resolved — leaves them zero and explains why in Detail.
type SyncResult struct {
	// Outcome classifies what the pass observed and did — one of the
	// SyncOutcome constants (clean, fast_forwarded, pushed, diverged,
	// blocked, remote_behind).
	Outcome SyncOutcome

	// Ahead counts local commits the remote does not have. Non-zero
	// means local work has not been pushed (or the histories diverged).
	Ahead int

	// Behind counts remote commits the local checkout does not have —
	// what a fast-forward would apply.
	Behind int

	// LocalHead is the resolved local branch head SHA at classification
	// time. Empty when the pass blocked before resolving heads.
	LocalHead string

	// RemoteHead is the fetched remote branch head SHA at
	// classification time. Empty when the pass blocked before
	// resolving heads.
	RemoteHead string

	// Detail is the human-readable reason for any refusing outcome
	// (blocked, diverged, remote_behind); empty when the pass succeeded.
	Detail string
}

// Sync runs one fast-forward-only sync pass against the remote: fetch, then a
// single locked critical section that verifies and integrates, then (for a
// bidirectional local lead) a push performed outside the lock.
//
// The security posture, enforced step by step below, is: the remote is
// untrusted transport; trust is decided per-commit against the store's
// allowed-signers trust set over the exact incoming range; the branch only
// ever moves forward; and the engine never force-pushes nor rewrites local
// history. A hostile or broken remote can, at worst, leave the pass Blocked or
// Diverged — never
// corrupt the signed document root.
func (s *Store) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	if err := checkRemoteArg("remote url", req.RemoteURL); err != nil {
		return SyncResult{}, err
	}
	if err := checkRevisionArg("branch", req.Branch); err != nil {
		return SyncResult{}, err
	}
	if err := s.checkBranchName(ctx, req.Branch); err != nil {
		return SyncResult{}, err
	}

	// 1. Fetch — outside the lock. Its only local effect is the
	// remote-tracking ref; it is bounded so a hung remote cannot delay the
	// critical section. Fetch failure aborts before any classification.
	if err := s.Fetch(ctx, FetchOptions{RemoteURL: req.RemoteURL, Branch: req.Branch, SSHCommand: req.SSHCommand}); err != nil {
		return SyncResult{}, err
	}

	// 2. Verify + integrate — one critical section under the store lock.
	res, pushSHA, err := s.syncLocked(ctx, req)
	if err != nil {
		return res, err
	}

	// 3. Push — outside the lock, so a slow remote cannot wedge document
	// writes. The captured local SHA is pushed with a fixed refspec, so a
	// concurrent local commit landing after the lock released does not change
	// what we push; a remote that advanced under us rejects the non-forced
	// push and the next pass reclassifies.
	if pushSHA != "" {
		if err := s.push(ctx, req.RemoteURL, req.Branch, pushSHA, req.SSHCommand); err != nil {
			return res, fmt.Errorf("push: %w", err)
		}
		res.Outcome = SyncPushed
	}
	return res, nil
}

// syncLocked is the critical section: it captures the two heads immutably,
// classifies, and (for a fast-forward) verifies and advances the branch. It
// returns a non-empty pushSHA to signal the caller to push that commit after
// the lock is released; it never performs the network push itself.
func (s *Store) syncLocked(ctx context.Context, req SyncRequest) (SyncResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Fail closed on trust-file placement before verifying or integrating. The
	// fetch has already run, but it touched only objects and the tracking ref,
	// not the worktree — so .allowed_signers is still HEAD's version here,
	// which is exactly why the in-tree file is permitted. A configured external
	// anchor must resolve outside the worktree. Re-checked every pass, not once
	// at construction.
	if req.RequireVerify {
		if err := s.assertAnchorPlacement(); err != nil {
			return SyncResult{}, "", err
		}
	}

	// HEAD must symbolically track the configured branch. A detached HEAD or
	// a branch-name mismatch is refused, never silently overwritten — the
	// fast-forward and push both assume HEAD == refs/heads/<branch>. (An
	// unborn HEAD still has a symbolic ref, so it passes here and is caught
	// at the revParse below instead.)
	branch, err := s.symbolicBranch(ctx)
	if err != nil {
		return SyncResult{Outcome: SyncBlocked, Detail: fmt.Sprintf("cannot resolve HEAD to a branch (a detached HEAD, or an unhealthy repository); sync requires HEAD on branch %q: %v", req.Branch, err)}, "", nil
	}
	if branch != req.Branch {
		return SyncResult{Outcome: SyncBlocked, Detail: fmt.Sprintf("repository is on branch %q, not the configured sync branch %q", branch, req.Branch)}, "", nil
	}

	// Capture both heads ONCE, by SHA. Every subsequent git operation
	// consumes these values — never the ref names — so a concurrent lock-free
	// fetch moving the tracking ref cannot make us verify one commit and
	// fast-forward to another (TOCTOU). An unborn HEAD fails rev-parse here,
	// yielding a clear error rather than a mutation.
	localHead, err := s.revParse(ctx, "HEAD")
	if err != nil {
		return SyncResult{}, "", fmt.Errorf("resolve local HEAD (an unborn repository cannot be fast-forward-synced): %w", err)
	}
	remoteHead, err := s.revParse(ctx, "refs/remotes/origin/"+req.Branch)
	if err != nil {
		return SyncResult{}, "", fmt.Errorf("resolve fetched remote head for %q: %w", req.Branch, err)
	}

	ahead, behind, err := s.countAheadBehind(ctx, localHead, remoteHead)
	if err != nil {
		return SyncResult{}, "", fmt.Errorf("compute ahead/behind: %w", err)
	}

	res := SyncResult{Ahead: ahead, Behind: behind, LocalHead: localHead, RemoteHead: remoteHead}

	// Rewind guard: if the caller saw a different remote head last pass and the
	// freshly fetched head is a strict ancestor of it, the remote rewound.
	// Refuse with no side effects rather than pushing local commits back over
	// what is likely an intentional history rewrite. A diverging rewrite (the
	// new head is neither ancestor nor descendant) is not caught here — it
	// falls through to the diverged classification, which also refuses safely.
	if req.LastKnownRemote != "" && remoteHead != req.LastKnownRemote {
		rewound, aerr := s.isAncestor(ctx, remoteHead, req.LastKnownRemote)
		switch {
		case aerr != nil:
			// The prior remote head should be reachable (it was fetched last
			// pass); a probe failure means it is pruned or misconfigured. The
			// guard fails open, so surface it rather than silently skipping.
			s.logger.Warn("rewind check failed; proceeding without it",
				"branch", req.Branch, "remote_head", shorten(remoteHead),
				"last_known_remote", shorten(req.LastKnownRemote), "error", aerr)
		case rewound:
			res.Outcome = SyncRemoteBehind
			res.Detail = fmt.Sprintf("remote rewound: %s is behind the previously seen %s; refusing to push over an apparent history rewrite — resolve on the workstation", shorten(remoteHead), shorten(req.LastKnownRemote))
			return res, "", nil
		}
	}

	switch {
	case ahead == 0 && behind == 0:
		// Already in sync. Touch nothing — is-ancestor is true for equal
		// refs, so the fast-forward path must never run here.
		res.Outcome = SyncClean
		return res, "", nil

	case ahead == 0 && behind > 0:
		return s.fastForwardLocked(ctx, req, res)

	case ahead > 0 && behind == 0:
		if req.Mode == SyncModeBidirectional {
			// Push happens after the lock releases; signal it by SHA so the
			// exact classified commit is what gets pushed.
			return res, localHead, nil
		}
		// Fetch-only: an unpushed local lead is expected and needs no action.
		res.Outcome = SyncClean
		return res, "", nil

	default: // ahead > 0 && behind > 0
		res.Outcome = SyncDiverged
		res.Detail = fmt.Sprintf("local and remote diverged (%d local, %d remote); fast-forward-only sync cannot reconcile — resolve on the workstation", ahead, behind)
		return res, "", nil
	}
}

// fastForwardLocked handles the behind>0 && ahead==0 case under the held lock:
// it refuses a dirty worktree, re-proves true ancestry over the captured SHAs,
// verifies every incoming commit, and only then advances the branch.
func (s *Store) fastForwardLocked(ctx context.Context, req SyncRequest, res SyncResult) (SyncResult, string, error) {
	// Never destroy uncommitted local work to make a sync succeed.
	clean, err := s.worktreeMatchesHead(ctx)
	if err != nil {
		return SyncResult{}, "", fmt.Errorf("check worktree: %w", err)
	}
	if !clean {
		res.Outcome = SyncBlocked
		res.Detail = "worktree has uncommitted changes; refusing to fast-forward over them"
		return res, "", nil
	}

	// Ancestry is the sole authority for a fast-forward. ahead==0 implies it,
	// but prove it over the captured SHAs anyway — never mutate on doubt.
	anc, err := s.isAncestor(ctx, res.LocalHead, res.RemoteHead)
	if err != nil {
		return SyncResult{}, "", fmt.Errorf("ancestry check: %w", err)
	}
	if !anc {
		res.Outcome = SyncBlocked
		res.Detail = "local head is not an ancestor of the remote head; refusing a non-fast-forward"
		return res, "", nil
	}

	// Verify EVERY incoming commit (localHead..remoteHead) against the
	// allowed-signers trust set, fail-closed. This runs before the branch
	// moves, so the trust file is still HEAD's version.
	if req.RequireVerify {
		commits, err := s.rangeCommits(ctx, res.LocalHead, res.RemoteHead)
		if err != nil {
			return SyncResult{}, "", fmt.Errorf("enumerate incoming commits: %w", err)
		}
		if len(commits) == 0 {
			// behind>0 guarantees at least one incoming commit; an empty
			// enumeration means verification would be vacuous — block, never
			// fast-forward unverified.
			res.Outcome = SyncBlocked
			res.Detail = "no incoming commits enumerated despite the remote being ahead; refusing a vacuous verification"
			return res, "", nil
		}
		if reason := s.firstUntrusted(ctx, commits); reason != "" {
			res.Outcome = SyncBlocked
			res.Detail = reason
			return res, "", nil
		}
	}

	if err := s.fastForward(ctx, res.RemoteHead); err != nil {
		return SyncResult{}, "", fmt.Errorf("fast-forward: %w", err)
	}
	res.Outcome = SyncFastForwarded
	return res, "", nil
}
