package provenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// AgentPrincipal is the fixed principal under which thane's own signing key
// is trusted in every rendered allowed_signers file. It is an internal
// identity, not a contactable address.
const AgentPrincipal = "thane@provenance.local"

// TrustedSigner is one operator identity destined for an OpenSSH
// allowed_signers file. Times are RFC3339 as authored in config;
// [RenderAllowedSigners] converts them to OpenSSH's on-disk format.
type TrustedSigner struct {
	// Principal is the OpenSSH signer identity, conventionally an email.
	Principal string

	// PublicKey is the key in authorized_keys form ("ssh-ed25519 AAAA...").
	// Any trailing comment is ignored for identity and rendering; use
	// Comment for the rendered trailing note.
	PublicKey string

	// Comment is an optional trailing note rendered after the key.
	Comment string

	// ValidAfter and ValidBefore are optional RFC3339 validity bounds.
	ValidAfter  string
	ValidBefore string
}

// RenderAllowedSigners produces the content of an OpenSSH allowed_signers
// file that trusts the agent key plus the operator keys, deterministically.
//
// The agent key is the unremovable trust anchor: it is always emitted first,
// under [AgentPrincipal], and an operator entry whose public key equals the
// agent key is rejected — that is a principal-spoof that would let another
// identity ride the agent's own key. Operator keys are canonicalized (comment
// and whitespace stripped for identity), deduplicated by key blob, and sorted
// by (principal, blob) so the rendered file never churns across boots when the
// configured set is unchanged. The returned content ends with a trailing
// newline and is safe to compare byte-for-byte for drift detection.
func RenderAllowedSigners(agentPublicKey string, operators []TrustedSigner) (string, error) {
	agentBlob, err := canonicalKeyBlob(agentPublicKey)
	if err != nil {
		return "", fmt.Errorf("agent signing key: %w", err)
	}

	type entry struct {
		principal string
		blob      string
		line      string
	}
	// seen maps a canonical key blob to the principal that first claimed
	// it, so a key reused under a second principal is caught.
	seen := map[string]string{agentBlob: AgentPrincipal}
	others := make([]entry, 0, len(operators))
	for i, s := range operators {
		blob, err := canonicalKeyBlob(s.PublicKey)
		if err != nil {
			return "", fmt.Errorf("operator signer %d (%s): %w", i, strings.TrimSpace(s.Principal), err)
		}
		principal := strings.TrimSpace(s.Principal)
		if blob == agentBlob {
			return "", fmt.Errorf("operator signer %q uses the agent's own signing key; the agent key is trusted implicitly and must not be listed", principal)
		}
		if prev, ok := seen[blob]; ok {
			// The same key under the same principal collapses silently:
			// this is the benign case where a key is listed in both the
			// shared block and a root's own list (the sets union). The
			// same key under a *different* principal is a spoof and is
			// refused.
			if prev == principal {
				continue
			}
			return "", fmt.Errorf("operator signer %q duplicates the key already trusted for %q", principal, prev)
		}
		seen[blob] = principal
		line, err := renderSignerLine(principal, blob, s.Comment, s.ValidAfter, s.ValidBefore)
		if err != nil {
			return "", fmt.Errorf("operator signer %q: %w", principal, err)
		}
		others = append(others, entry{principal: principal, blob: blob, line: line})
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].principal != others[j].principal {
			return others[i].principal < others[j].principal
		}
		return others[i].blob < others[j].blob
	})

	agentLine, err := renderSignerLine(AgentPrincipal, agentBlob, "", "", "")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(agentLine)
	b.WriteByte('\n')
	for _, e := range others {
		b.WriteString(e.line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// ReconcileAllowedSigners renders this repository's trust set — the agent key
// plus the given operator signers — and, when it differs from the repo's
// current .allowed_signers, writes it and makes a signed commit so the tracked
// trust surface stays clean and covered by signed history. It reports whether
// a commit was made and is idempotent: an unchanged set is a no-op with no
// commit, so it can run on every boot without churning history.
//
// The repository must already have a HEAD, so call it after
// [Store.BootstrapBirthCommit]. Rendering enforces the trust invariants
// (agent key unremovable and never reused by an operator, no principal-spoof
// duplicates), so a config that violates them fails here rather than silently
// weakening verification.
func (s *Store) ReconcileAllowedSigners(ctx context.Context, operators []TrustedSigner) (bool, error) {
	// This reconcile writes and commits the repo-local trust file. When the
	// Store verifies against an external allowed_signers file, that file is
	// not in the worktree and cannot be committed — reconciling the
	// repo-local file would write a trust surface git never consults. Refuse
	// rather than silently diverge; out-of-tree trust is a separate path.
	if s.allowedSignersPath != "" {
		return false, fmt.Errorf("ReconcileAllowedSigners: store verifies against an external allowed_signers file (%s); in-tree reconcile does not apply", s.allowedSignersPath)
	}

	desired, err := RenderAllowedSigners(s.signer.PublicKey(), operators)
	if err != nil {
		return false, fmt.Errorf("render allowed_signers: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.path, ".allowed_signers")
	// A symlink here would let the trust surface be redirected out from
	// under verification; reject anything but a regular file (absent is
	// fine — we are about to create it).
	if err := validateAllowedSignersFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read .allowed_signers: %w", err)
	}
	if string(current) == desired {
		return false, nil
	}
	if err := atomicWriteFile(path, []byte(desired), 0o644); err != nil {
		return false, fmt.Errorf("write .allowed_signers: %w", err)
	}
	// Re-validate after the rename: a swapped symlink would have been
	// replaced by our regular file, but confirm the invariant still holds.
	if err := validateAllowedSignersFile(path); err != nil {
		return false, err
	}
	if _, err := s.commitFiles(ctx, []string{".allowed_signers"}, "reconcile allowed_signers"); err != nil {
		return false, fmt.Errorf("commit .allowed_signers: %w", err)
	}
	s.logger.Info("reconciled allowed_signers", "operator_entries", len(operators))
	return true, nil
}

// VerifyHead confirms the repository's HEAD commit verifies as trusted against
// the current allowed_signers file. Run it after reconciling the trust set as
// a boot-time round-trip: git parses the whole allowed_signers file to verify
// any commit, so this catches a malformed signer line or an OpenSSH version
// that cannot parse a rendered option (such as a validity window) right away,
// rather than letting it surface later as a silent verification failure on the
// first managed read.
func (s *Store) VerifyHead(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.git(ctx, nil, nil, "verify-commit", "HEAD"); err != nil {
		return fmt.Errorf("verify HEAD against allowed_signers: %w", err)
	}
	return nil
}

// atomicWriteFile writes data to path atomically: it writes a temp file in the
// same directory, fsyncs it, renames it into place, then fsyncs the directory
// so a crash cannot leave a partial or truncated file. Renaming over the
// target also collapses the symlink/regular-file TOCTOU window — the result is
// always the regular file we wrote.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".allowed_signers-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup; a no-op once renamed

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// renderSignerLine builds one allowed_signers line:
//
//	principal [valid-after="...",valid-before="..."] keytype base64 [comment]
//
// blob is the canonical "keytype base64" form (no comment).
func renderSignerLine(principal, blob, comment, validAfter, validBefore string) (string, error) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return "", fmt.Errorf("principal is required")
	}
	parts := []string{principal}
	opts, err := renderValidityOptions(validAfter, validBefore)
	if err != nil {
		return "", err
	}
	if opts != "" {
		parts = append(parts, opts)
	}
	parts = append(parts, blob)
	if c := strings.TrimSpace(comment); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, " "), nil
}

// renderValidityOptions renders the comma-joined OpenSSH options field for a
// validity window, or "" when neither bound is set. Config already validates
// the window, but this renderer is a security-sensitive seam other callers can
// reach, so it independently enforces that valid_after is strictly before
// valid_before rather than emit an options field that violates the contract.
func renderValidityOptions(validAfter, validBefore string) (string, error) {
	var opts []string
	var after, before time.Time
	haveAfter, haveBefore := false, false

	if v := strings.TrimSpace(validAfter); v != "" {
		t, ts, err := opensshTime(v)
		if err != nil {
			return "", fmt.Errorf("valid_after: %w", err)
		}
		after, haveAfter = t, true
		opts = append(opts, `valid-after="`+ts+`"`)
	}
	if v := strings.TrimSpace(validBefore); v != "" {
		t, ts, err := opensshTime(v)
		if err != nil {
			return "", fmt.Errorf("valid_before: %w", err)
		}
		before, haveBefore = t, true
		opts = append(opts, `valid-before="`+ts+`"`)
	}
	if haveAfter && haveBefore && !after.Before(before) {
		return "", fmt.Errorf("valid_after must be strictly before valid_before")
	}
	return strings.Join(opts, ","), nil
}

// opensshTime parses an RFC3339 timestamp and returns both the parsed time and
// its rendering in OpenSSH's allowed_signers time format (YYYYMMDDHHMMSSZ, UTC).
func opensshTime(rfc3339 string) (time.Time, string, error) {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%q must be an RFC3339 timestamp: %w", rfc3339, err)
	}
	return t, t.UTC().Format("20060102150405Z"), nil
}

// canonicalKeyBlob parses an authorized_keys-form public key and returns its
// canonical "keytype base64" form with the comment stripped, so keys that
// differ only by comment or surrounding whitespace compare equal.
//
// It rejects any value carrying more than one key: ssh.ParseAuthorizedKey
// parses only the first line and returns the remainder in rest, so a value
// with an embedded newline and a second key would otherwise be silently
// accepted (and its second key dropped on render) — refuse it instead.
func canonicalKeyBlob(key string) (string, error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return "", fmt.Errorf("not a valid SSH public key: %w", err)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", fmt.Errorf("value must contain exactly one SSH public key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}
