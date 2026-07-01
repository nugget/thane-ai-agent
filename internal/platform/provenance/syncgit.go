package provenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// This file holds the low-level git plumbing the fast-forward-only sync
// engine in sync.go composes: range enumeration and per-commit verification,
// the fast-forward and push operations, ref/branch resolution, and the
// out-of-tree trust-anchor containment check.

// rangeCommits lists the commits reachable from `to` but not from `from`
// (git rev-list from..to) with no history-pruning flags, so every commit —
// including merge second-parents — is enumerated for verification. Direction
// is load-bearing: from must be the local head and to the remote head.
func (s *Store) rangeCommits(ctx context.Context, from, to string) ([]string, error) {
	if err := checkRevisionArg("range base", from); err != nil {
		return nil, err
	}
	if err := checkRevisionArg("range tip", to); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := s.git(ctx, nil, &buf, "rev-list", "--end-of-options", from+".."+to); err != nil {
		return nil, err
	}
	return strings.Fields(buf.String()), nil
}

// firstUntrusted verifies each commit against the store's out-of-tree anchor
// and returns a reason for the first one that is not trusted, or "" if all
// pass. Trust keys strictly on CommitSigner.Verified (git %G?=="G"): a valid
// signature by a key absent from the anchor (a "U" verdict) is untrusted. Any
// verification error is treated as untrusted (fail-closed), never skipped.
func (s *Store) firstUntrusted(ctx context.Context, commits []string) string {
	for _, sha := range commits {
		cs, err := signerFor(ctx, s.path, s.allowedSignersPath, sha)
		if err != nil {
			return fmt.Sprintf("cannot verify commit %s: %v", shorten(sha), err)
		}
		if !cs.Verified {
			reason := cs.Reason
			if reason == "" {
				reason = "not trusted"
			}
			return fmt.Sprintf("commit %s is not trusted: %s (signer %q)", shorten(sha), reason, cs.Principal)
		}
	}
	return ""
}

// fastForward advances the current branch and worktree to target using
// git merge --ff-only. A fast-forward creates no commit (a pure ref move), so
// there is no unsigned-commit concern; --ff-only refuses any non-fast-forward
// and will not clobber conflicting local changes. The branch identity is
// already asserted (HEAD == refs/heads/<branch>) and ancestry re-proven by the
// caller, so this moves exactly the intended branch to the verified SHA.
func (s *Store) fastForward(ctx context.Context, target string) error {
	if err := checkRevisionArg("fast-forward target", target); err != nil {
		return err
	}
	if err := s.git(ctx, nil, nil, "merge", "--ff-only", "--end-of-options", target); err != nil {
		return fmt.Errorf("git merge --ff-only %s: %w", shorten(target), err)
	}
	return nil
}

// push sends the pinned commit to the remote branch with a fixed refspec.
// It never passes --force or --force-with-lease and never prefixes the
// refspec with '+', so a non-fast-forward push is rejected by the remote
// rather than overwriting its history. It is bounded by remoteTimeout.
func (s *Store) push(ctx context.Context, remoteURL, branch, sha, sshCommand string) error {
	if err := checkRemoteArg("remote url", remoteURL); err != nil {
		return err
	}
	if err := checkRevisionArg("branch", branch); err != nil {
		return err
	}
	if err := checkRevisionArg("push commit", sha); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()

	var env []string
	if sshCommand != "" {
		env = []string{"GIT_SSH_COMMAND=" + sshCommand}
	}

	refspec := sha + ":refs/heads/" + branch
	if err := s.gitWithEnv(ctx, env, nil, nil, "push", "--end-of-options", remoteURL, refspec); err != nil {
		return fmt.Errorf("push %s to %s: %w", shorten(sha), remoteURL, err)
	}
	return nil
}

// countAheadBehind counts how many commits `left` is ahead of and behind
// `right` (git rev-list --left-right --count left...right). Both revisions are
// resolved SHAs or refs supplied by the caller; the symmetric "..." form with
// --left-right yields "<ahead>\t<behind>".
func (s *Store) countAheadBehind(ctx context.Context, left, right string) (ahead, behind int, err error) {
	if err := checkRevisionArg("left revision", left); err != nil {
		return 0, 0, err
	}
	if err := checkRevisionArg("right revision", right); err != nil {
		return 0, 0, err
	}
	var out bytes.Buffer
	if err := s.git(ctx, nil, &out, "rev-list", "--left-right", "--count", "--end-of-options", left+"..."+right); err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out.String())
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out.String())
	}
	if ahead, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("parse ahead %q: %w", fields[0], err)
	}
	if behind, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("parse behind %q: %w", fields[1], err)
	}
	return ahead, behind, nil
}

// revParse resolves a revision to a single object SHA (git rev-parse
// --verify). It errors on an unborn HEAD or an absent ref rather than
// returning an empty string.
func (s *Store) revParse(ctx context.Context, rev string) (string, error) {
	if err := checkRevisionArg("revision", rev); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := s.git(ctx, nil, &buf, "rev-parse", "--verify", "--end-of-options", rev); err != nil {
		return "", err
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return "", fmt.Errorf("rev-parse %s returned nothing", rev)
	}
	return out, nil
}

// symbolicBranch returns the short branch name HEAD points at, or an error if
// HEAD is detached.
func (s *Store) symbolicBranch(ctx context.Context) (string, error) {
	var buf bytes.Buffer
	if err := s.git(ctx, nil, &buf, "symbolic-ref", "--short", "HEAD"); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// isAncestor reports whether ancestor is an ancestor of descendant
// (git merge-base --is-ancestor). Exit 1 means "no"; any other non-zero exit
// (e.g. a bad object) is a real error, not a "no".
func (s *Store) isAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	if err := checkRevisionArg("ancestor", ancestor); err != nil {
		return false, err
	}
	if err := checkRevisionArg("descendant", descendant); err != nil {
		return false, err
	}
	err := s.git(ctx, nil, nil, "merge-base", "--is-ancestor", "--end-of-options", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// worktreeMatchesHead reports whether tracked files (staged and unstaged)
// match HEAD, via git diff --quiet HEAD. It ignores untracked files, which a
// fast-forward will not silently overwrite.
func (s *Store) worktreeMatchesHead(ctx context.Context) (bool, error) {
	err := s.git(ctx, nil, nil, "diff", "--quiet", "HEAD")
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// assertAnchorOutsideTree fails closed unless the store's allowed-signers
// anchor is configured and resolves strictly outside the repository working
// tree. A remote-synced root's trust set must live out-of-tree: an in-tree
// .allowed_signers is a file a fetch can rewrite, which would let a pushed
// commit add an attacker key and self-authorize. Both paths are resolved to
// absolute, symlink-free form before a component-wise containment check.
func (s *Store) assertAnchorOutsideTree() error {
	anchor := strings.TrimSpace(s.allowedSignersPath)
	if anchor == "" {
		return fmt.Errorf("remote-synced verification requires an out-of-tree allowed-signers anchor, but the store has none (an in-tree .allowed_signers is rewritable by a fetch)")
	}
	if isWithin(resolveReal(s.path), resolveReal(anchor)) {
		return fmt.Errorf("allowed-signers anchor %q resolves inside the synced tree %q; a fetch could rewrite the trust set and self-authorize", anchor, s.path)
	}
	return nil
}

// resolveReal returns an absolute, symlink-resolved path. When the leaf does
// not exist yet (e.g. an anchor path that has not been created), it resolves
// the deepest existing ancestor and re-attaches the remaining components, so a
// repo path and an anchor path resolve their shared symlinks consistently
// (on macOS, /var → /private/var). Without this, an existing repo would
// resolve while a not-yet-created in-tree anchor would not, and the divergent
// prefixes would make an in-tree anchor falsely appear out-of-tree.
func resolveReal(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// Walk up to the first existing ancestor, resolve it, and re-attach the
	// non-existent tail.
	tail := []string{}
	dir := abs
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the volume root
		}
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			parts := append([]string{real}, filepath.Base(dir))
			for i := len(tail) - 1; i >= 0; i-- {
				parts = append(parts, tail[i])
			}
			return filepath.Join(parts...)
		}
		tail = append(tail, filepath.Base(dir))
		dir = parent
	}
	return abs
}

// isWithin reports whether child is parent or lies inside parent, comparing
// path components (not string prefixes), so a sibling like /a/repo-2 is not
// treated as inside /a/repo.
func isWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	pc := splitComponents(parent)
	cc := splitComponents(child)
	if len(cc) < len(pc) {
		return false
	}
	for i := range pc {
		if pc[i] != cc[i] {
			return false
		}
	}
	return true
}

func splitComponents(p string) []string {
	p = strings.Trim(p, string(filepath.Separator))
	if p == "" {
		return nil
	}
	return strings.Split(p, string(filepath.Separator))
}
