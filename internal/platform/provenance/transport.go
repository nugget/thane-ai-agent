package provenance

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// remoteTimeout bounds a single network git operation (fetch, and later
// push). It caps how long a slow or unresponsive remote can occupy the
// caller — the sync design fetches before taking the write lock, so a hung
// remote must never be able to wedge that lock indefinitely.
const remoteTimeout = 2 * time.Minute

// FetchOptions configures a single fetch of a remote branch.
type FetchOptions struct {
	// RemoteURL is the git remote to fetch from (ssh, https, or a local
	// path). It is used directly rather than a configured "origin" remote so
	// the transport is stateless — no `git remote add` bootstrap step.
	RemoteURL string

	// Branch is the branch to fetch. It is fetched into the local
	// remote-tracking ref refs/remotes/origin/<Branch>.
	Branch string

	// SSHCommand, when non-empty, is exported as GIT_SSH_COMMAND for the
	// fetch — the hardened ssh invocation from [BuildSSHCommand]. It is
	// empty for https or local-path remotes, which need no ssh transport.
	SSHCommand string
}

// Fetch fetches opts.Branch from opts.RemoteURL into the local
// remote-tracking ref refs/remotes/origin/<Branch>. It writes nothing else:
// HEAD, the index, and the worktree are untouched, and FETCH_HEAD is not
// written. Fetch does not take the store lock — it is a read-from-network
// operation whose only local effect is the tracking ref — so callers may run
// it concurrently with local reads and before entering the write critical
// section.
//
// The operation is bounded by [remoteTimeout] regardless of the deadline on
// ctx, so an unresponsive remote cannot block indefinitely.
func (s *Store) Fetch(ctx context.Context, opts FetchOptions) error {
	if strings.TrimSpace(opts.RemoteURL) == "" {
		return fmt.Errorf("fetch: remote url is empty")
	}
	if err := checkRemoteArg("remote url", opts.RemoteURL); err != nil {
		return err
	}
	if err := checkRevisionArg("branch", opts.Branch); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()

	var env []string
	if opts.SSHCommand != "" {
		env = []string{"GIT_SSH_COMMAND=" + opts.SSHCommand}
	}

	// A forced refspec keeps the tracking ref honest even if the remote
	// rewound; forward-only integration is enforced later, at merge time,
	// not by refusing to observe the remote's true state here.
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", opts.Branch, opts.Branch)

	if err := s.gitWithEnv(ctx, env, nil, nil,
		"fetch", "--no-write-fetch-head", "--end-of-options", opts.RemoteURL, refspec); err != nil {
		return fmt.Errorf("fetch %s %s: %w", opts.RemoteURL, opts.Branch, err)
	}
	return nil
}

// AheadBehind reports how far local HEAD has diverged from the tracked remote
// branch: ahead is the number of commits on HEAD not yet on the remote,
// behind is the number on the remote not yet local. Call [Store.Fetch] first
// so the remote-tracking ref reflects the remote's current state.
//
// The four combinations map directly onto the sync state machine: (0,0) is in
// sync, (>0,0) is a local-only lead to push, (0,>0) is a clean fast-forward
// to pull, and (>0,>0) is divergence that fast-forward-only sync refuses.
func (s *Store) AheadBehind(ctx context.Context, branch string) (ahead, behind int, err error) {
	if err := checkRevisionArg("branch", branch); err != nil {
		return 0, 0, err
	}

	var out bytes.Buffer
	if err := s.git(ctx, nil, &out, "rev-list", "--left-right", "--count",
		"--end-of-options", "HEAD...refs/remotes/origin/"+branch); err != nil {
		return 0, 0, fmt.Errorf("ahead/behind %s: %w", branch, err)
	}

	fields := strings.Fields(out.String())
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("ahead/behind %s: unexpected rev-list output %q", branch, out.String())
	}
	if ahead, err = strconv.Atoi(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("ahead/behind %s: parse ahead %q: %w", branch, fields[0], err)
	}
	if behind, err = strconv.Atoi(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("ahead/behind %s: parse behind %q: %w", branch, fields[1], err)
	}
	return ahead, behind, nil
}

// BuildSSHCommand assembles the GIT_SSH_COMMAND string used for SSH remotes.
// It hard-codes the no-trust-on-first-use posture the sync design requires:
// host keys must already be pinned in knownHosts (StrictHostKeyChecking=yes),
// only the given transport key is offered (IdentitiesOnly, no agent), and the
// connection never prompts (BatchMode=yes) so a stalled handshake fails fast
// rather than hanging.
//
// sshKey and knownHosts are shell-quoted because git interprets
// GIT_SSH_COMMAND through the shell. An empty sshKey or knownHosts omits the
// corresponding option; callers requiring both should validate up front (the
// config layer already requires known_hosts for ssh remotes).
func BuildSSHCommand(sshKey, knownHosts string) string {
	parts := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentityAgent=none"}
	if strings.TrimSpace(sshKey) != "" {
		parts = append(parts, "-i", shellQuote(sshKey), "-o", "IdentitiesOnly=yes")
	}
	if strings.TrimSpace(knownHosts) != "" {
		parts = append(parts, "-o", "UserKnownHostsFile="+shellQuote(knownHosts))
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps s in single quotes, escaping any embedded single quote, so
// it survives the shell that git uses to run GIT_SSH_COMMAND intact — spaces
// and metacharacters in a key or known_hosts path are treated literally.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// checkRemoteArg rejects a remote url that git could mistake for an option.
// A legitimate url (ssh, https, or a path) never begins with "-";
// --end-of-options guards the command line as well, but rejecting early gives
// a clearer error and defends the ahead/behind path that reads the same value.
func checkRemoteArg(kind, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("%s is empty", kind)
	}
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("%s %q must not begin with '-'", kind, url)
	}
	return nil
}
