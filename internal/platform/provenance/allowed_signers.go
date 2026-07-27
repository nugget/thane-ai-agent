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
// under [AgentPrincipal]. An operator entry may name that same key under
// [AgentPrincipal] — that is how a root declares the agent entitled to
// establish it — and collapses into the implicit line rather than repeating
// it. The same key under any *other* principal is refused: that is a
// principal-spoof that would let another identity ride the agent's own key.
//
// Operator keys are canonicalized (comment and whitespace stripped for
// identity), deduplicated by key blob, and sorted by (principal, blob) so the
// rendered file never churns across boots when the configured set is
// unchanged. The returned content ends with a trailing newline and is safe to
// compare byte-for-byte for drift detection.
func RenderAllowedSigners(agentPublicKey string, operators []TrustedSigner) (string, error) {
	agentBlob, err := canonicalKeyBlob(agentPublicKey)
	if err != nil {
		return "", fmt.Errorf("agent signing key: %w", err)
	}

	// The agent line is emitted first and unconditionally, so its blob is
	// already claimed before any operator entry is considered.
	lines, err := renderSignerSet(operators, map[string]string{agentBlob: AgentPrincipal})
	if err != nil {
		return "", err
	}

	agentLine, err := renderSignerLine(AgentPrincipal, agentBlob, "", "", "")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(agentLine)
	b.WriteByte('\n')
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// RenderSeedSigners produces an allowed_signers file containing exactly the
// declared seed signers and nothing else — in particular, no implicit agent
// line.
//
// That omission is the whole point. Admission asks whether a repository's
// birth is attributable to a key someone deliberately entitled, and the agent
// key is trusted implicitly everywhere else. Rendering it here too would make
// every agent-founded root self-admitting and quietly delete the property that
// an instance can be configured so its own agent cannot establish or amend the
// root holding its config.
func RenderSeedSigners(seeds []TrustedSigner) (string, error) {
	lines, err := renderSignerSet(seeds, nil)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// renderSignerSet canonicalizes, deduplicates, and sorts signer entries into
// allowed_signers lines.
//
// reserved maps a key blob the caller has already emitted to the principal
// holding it, so an entry claiming that key under a different principal is
// caught. An entry that restates a reserved key under its own principal is
// dropped rather than repeated.
//
// Errors name a plain "signer" because this renders both the operator set and
// the seed set. Saying "operator" here would report a malformed seed signer as
// an operator problem and send the reader to the wrong config block; the
// calling function supplies which set it was rendering.
func renderSignerSet(signers []TrustedSigner, reserved map[string]string) ([]string, error) {
	type entry struct {
		principal string
		blob      string
		line      string
	}
	// seen maps a canonical key blob to the principal that first claimed
	// it, so a key reused under a second principal is caught.
	seen := make(map[string]string, len(signers)+len(reserved))
	for blob, principal := range reserved {
		seen[blob] = principal
	}
	out := make([]entry, 0, len(signers))
	for i, s := range signers {
		blob, err := canonicalKeyBlob(s.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("signer %d (%s): %w", i, strings.TrimSpace(s.Principal), err)
		}
		principal := strings.TrimSpace(s.Principal)
		if prev, ok := seen[blob]; ok {
			// The same key under the same principal collapses silently:
			// this is the benign case where a key is listed in more than
			// one place that feeds the same set, or names the agent key
			// under the agent's own principal. The same key under a
			// *different* principal is a spoof and is refused.
			if prev == principal {
				continue
			}
			return nil, fmt.Errorf("signer %q duplicates the key already trusted for %q", principal, prev)
		}
		seen[blob] = principal
		line, err := renderSignerLine(principal, blob, s.Comment, s.ValidAfter, s.ValidBefore)
		if err != nil {
			return nil, fmt.Errorf("signer %q: %w", principal, err)
		}
		out = append(out, entry{principal: principal, blob: blob, line: line})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].principal != out[j].principal {
			return out[i].principal < out[j].principal
		}
		return out[i].blob < out[j].blob
	})
	lines := make([]string, 0, len(out))
	for _, e := range out {
		lines = append(lines, e.line)
	}
	return lines, nil
}

// SeedAllowedSigners establishes this repository's trust set once — the agent
// key plus the root's declared seed signers — and leaves it alone thereafter.
// It reports whether it wrote and committed the file.
//
// Seeding rather than reconciling is the difference between config being a
// root's origin and config being its permanent authority. A root's
// .allowed_signers is that root's own record of whom it trusts, extended by
// commits signed with keys it already trusts. Rewriting it from config on
// every boot would mean one instance's config silently redefines who may sign
// a corpus shared with others, and would collapse every root into whatever
// the widest list says.
//
// An existing file is left alone even when it differs from config. That is
// the point rather than a limitation: divergence is the root exercising its
// own delegation, not drift to be corrected.
//
// The repository must already have a HEAD, so call it after
// [Store.BootstrapBirthCommit]. Rendering enforces the trust invariants
// (agent key unremovable and never reused by an operator, no principal-spoof
// duplicates), so a config that violates them fails here rather than silently
// weakening verification.
func (s *Store) SeedAllowedSigners(ctx context.Context, seed []TrustedSigner) (bool, error) {
	if s.allowedSignersPath != "" {
		return false, fmt.Errorf("SeedAllowedSigners: store verifies against an external allowed_signers file (%s); in-tree seeding does not apply", s.allowedSignersPath)
	}
	s.mu.Lock()
	_, statErr := os.Stat(filepath.Join(s.path, ".allowed_signers"))
	s.mu.Unlock()
	switch {
	case statErr == nil:
		return false, nil
	case !errors.Is(statErr, os.ErrNotExist):
		return false, fmt.Errorf("stat .allowed_signers: %w", statErr)
	}
	return s.writeAllowedSigners(ctx, seed)
}

func (s *Store) writeAllowedSigners(ctx context.Context, operators []TrustedSigner) (bool, error) {
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
	args := append(signatureTrustArgs(), "verify-commit", "HEAD")
	if err := s.git(ctx, nil, nil, args...); err != nil {
		// A seed signer may always sign this root, whatever the in-tree file
		// currently says — see trustedBySeed.
		if !trustedBySeed(ctx, s.path, s.seedSigners, "HEAD") {
			return fmt.Errorf("verify HEAD against allowed_signers: %w", err)
		}
		logSeedFloorUsed(s.logger, s.path, "HEAD")
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
