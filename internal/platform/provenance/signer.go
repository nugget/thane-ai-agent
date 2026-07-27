package provenance

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CommitSigner describes who signed a commit and whether the signature is
// trusted against the repository's allowed_signers. It is additive — the
// boolean VerifyFile/VerifyTree contract is unchanged — and lets revision
// history distinguish an agent-authored commit from an operator's.
type CommitSigner struct {
	// Verified is true only for a good signature by a trusted key.
	Verified bool
	// Principal is the allowed_signers identity that signed (for example
	// "thane@provenance.local" or "alice@example.com"), when git reports one.
	Principal string
	// Kind classifies the principal, one of [SignerKindAgent],
	// [SignerKindOperator], or [SignerKindUnknown].
	Kind string
	// KeyFingerprint is the signing key's fingerprint, when git reports one.
	KeyFingerprint string
	// Reason explains a non-verified result (for example "signer not in
	// allow-list" or "unsigned"). Empty when Verified is true.
	Reason string
}

const (
	SignerKindAgent    = "agent"
	SignerKindOperator = "operator"
	SignerKindUnknown  = "unknown"
)

// SignerFor reports who signed one commit. Verification never fails the call:
// an unsigned or untrusted commit is a valid, reportable result, not an error.
func (s *Store) SignerFor(ctx context.Context, commit string) (CommitSigner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return signerFor(ctx, s.path, s.allowedSignersPath, commit)
}

// SignerFor reports who signed one commit (verify-only path).
func (v *Verifier) SignerFor(ctx context.Context, commit string) (CommitSigner, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return signerFor(ctx, v.path, v.allowedSignersPath, commit)
}

func signerFor(ctx context.Context, repoPath, allowedSignersPath, commit string) (CommitSigner, error) {
	if err := checkRevisionArg("commit", commit); err != nil {
		return CommitSigner{}, err
	}
	out, err := runGitTextVerify(ctx, repoPath, allowedSignersPath,
		"log", "-1", "--format=%G?%x00%GS%x00%GF", "--end-of-options", commit)
	if err != nil {
		return CommitSigner{}, fmt.Errorf("read signer for %s: %w", shorten(commit), err)
	}
	verdict, principal, fingerprint := splitSignerFields(out)
	return parseCommitSigner(verdict, principal, fingerprint), nil
}

// splitSignerFields splits a NUL-delimited "%G?\x00%GS\x00%GF" line, padding
// missing trailing fields.
func splitSignerFields(line string) (verdict, principal, fingerprint string) {
	parts := strings.SplitN(strings.TrimRight(line, "\n"), "\x00", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

// parseCommitSigner maps git's %G? verdict, %GS signer, and %GF fingerprint
// onto a CommitSigner. Only "G" (good, trusted) counts as verified.
func parseCommitSigner(verdict, principal, fingerprint string) CommitSigner {
	cs := CommitSigner{
		Principal:      strings.TrimSpace(principal),
		KeyFingerprint: strings.TrimSpace(fingerprint),
	}
	switch strings.TrimSpace(verdict) {
	case "G":
		cs.Verified = true
	case "U":
		cs.Reason = "signature valid but signer not in allow-list"
	case "B":
		cs.Reason = "bad signature"
	case "X":
		cs.Reason = "signature outside the key's validity window"
	case "Y":
		cs.Reason = "signing key expired"
	case "R":
		cs.Reason = "signing key revoked"
	case "E":
		cs.Reason = "signature cannot be verified"
	case "N", "":
		cs.Reason = "unsigned"
	default:
		cs.Reason = "unrecognized verification result " + strings.TrimSpace(verdict)
	}
	cs.Kind = signerKind(cs.Principal)
	return cs
}

// signerKind classifies a principal. The agent's own principal is the trust
// anchor; any other named principal is an operator; an empty one is unknown.
func signerKind(principal string) string {
	switch principal {
	case "":
		return SignerKindUnknown
	case AgentPrincipal:
		return SignerKindAgent
	default:
		return SignerKindOperator
	}
}

// sshKeygenProgram is the executable git is told to use for SSH signature
// operations, resolved once rather than left to configuration.
//
// Resolution still consults PATH, which is not a boundary against someone who
// controls the process environment — but such a person could replace the thane
// binary itself, so it is not an escalation. Configuration is a different
// matter: it is ordinary writable state, in the operator's home directory or
// inside the very repository being judged.
var sshKeygenProgram = func() string {
	if resolved, err := exec.LookPath("ssh-keygen"); err == nil {
		return resolved
	}
	return "ssh-keygen"
}()

// signatureTrustArgs pins the configuration that decides whether a signature
// is good, overriding whatever the system, global, and repository config files
// say.
//
// `gpg.ssh.program` names the executable git consults to answer that question,
// and `gpg.format` selects the scheme. Both are writable by anything with
// filesystem access — including this agent, wherever shell access is enabled —
// so leaving them to configuration means the verdict of the trust gate can be
// redirected by editing a file. A gate whose answer can be reassigned that way
// is not a gate. These flags are passed with -c, which takes precedence over
// every config file, so a repo-local .git/config inside an untrusted root
// cannot reach the decision.
func signatureTrustArgs() []string {
	return []string{"-c", "gpg.format=ssh", "-c", "gpg.ssh.program=" + sshKeygenProgram}
}

// runGitTextVerify runs a read-only git command with SSH signature
// verification enabled: it injects the allowed_signers file when one is
// configured (a verify-only Verifier), and otherwise relies on the repo-local
// git config a Store already set at init.
func runGitTextVerify(ctx context.Context, repoPath, allowedSignersPath string, args ...string) (string, error) {
	prefix := signatureTrustArgs()
	if strings.TrimSpace(allowedSignersPath) != "" {
		prefix = append(prefix, "-c", "gpg.ssh.allowedSignersFile="+allowedSignersPath)
	}
	return runGitText(ctx, repoPath, append(prefix, args...)...)
}
