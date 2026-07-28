package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// EvidenceStatus describes whether one local provenance check succeeded.
type EvidenceStatus string

const (
	// EvidenceVerified means the check ran and its stated invariant holds.
	EvidenceVerified EvidenceStatus = "verified"
	// EvidenceFailed means the check ran and its stated invariant does not hold.
	EvidenceFailed EvidenceStatus = "failed"
)

// Evidence is the public, core-backed identity statement for one Thane.
//
// It reports locally verifiable facts rather than a trust verdict. In
// particular, AssertedAt is a claim covered by the signed birth commit, not an
// externally witnessed timestamp.
type Evidence struct {
	// SchemaVersion identifies the wire contract used by this evidence.
	SchemaVersion int `json:"schema_version"`
	// ObservedAt is when the live repository checks were performed.
	ObservedAt time.Time `json:"observed_at"`
	// Instance identifies the Thane from its founding public material.
	Instance InstanceEvidence `json:"instance"`
	// Core describes the founding and active repository state.
	Core CoreEvidence `json:"core"`
}

// InstanceEvidence identifies the Thane and its founding public material.
type InstanceEvidence struct {
	// ID is stable across process restarts and core content changes.
	ID string `json:"id"`
	// Name is the instance name asserted in the signed birth policy.
	Name string `json:"name"`
	// IdentityKey identifies the founding long-lived signing key.
	IdentityKey PublicKeyEvidence `json:"identity_key"`
	// ChannelCA identifies the founding channel certificate authority.
	ChannelCA PublicKeyEvidence `json:"channel_ca"`
}

// PublicKeyEvidence identifies public cryptographic material without exposing
// its encoded contents.
type PublicKeyEvidence struct {
	// Algorithm names the key or certificate algorithm.
	Algorithm string `json:"algorithm"`
	// Fingerprint is the SHA-256 fingerprint recomputed from committed material.
	Fingerprint string `json:"fingerprint"`
}

// CoreEvidence describes the birth, active revision, and verification posture
// of the core repository.
type CoreEvidence struct {
	// Birth identifies the signed parentless commit.
	Birth BirthEvidence `json:"birth"`
	// Head identifies the active revision and worktree posture.
	Head HeadEvidence `json:"head"`
	// Verification reports birth admission and active-tree checks separately.
	Verification VerificationEvidence `json:"verification"`
}

// GitObjectID is an algorithm-qualified Git object identifier.
type GitObjectID struct {
	// Algorithm is the repository's object-hash algorithm.
	Algorithm string `json:"algorithm"`
	// OID is the full Git object identifier.
	OID string `json:"oid"`
}

// BirthEvidence identifies the single parentless commit from which core
// descends.
type BirthEvidence struct {
	// Commit is the single parentless commit from which core descends.
	Commit GitObjectID `json:"commit"`
	// AssertedAt is the birth time claimed inside that signed commit.
	AssertedAt time.Time `json:"asserted_at"`
	// TimeAssurance states what supports AssertedAt.
	TimeAssurance string `json:"time_assurance"`
	// Anchor distinguishes operator-anchored, self-signed, and unknown posture.
	Anchor string `json:"anchor"`
}

// HeadEvidence identifies the active core revision and its worktree posture.
type HeadEvidence struct {
	// Commit is the active core HEAD.
	Commit GitObjectID `json:"commit"`
	// WorktreeClean reports whether tracked core content matches HEAD.
	WorktreeClean bool `json:"worktree_clean"`
	// TrustFileChangeCount is the number of commits that touched the trust file.
	TrustFileChangeCount int `json:"trust_file_change_count"`
}

// VerificationEvidence separates birth admission from verification of the
// currently active tree.
type VerificationEvidence struct {
	// Admission checks birth and trust-file history against declared seeds.
	Admission CheckEvidence `json:"admission"`
	// Head checks that the active tree is clean and covered by trusted history.
	Head CheckEvidence `json:"head"`
}

// CheckEvidence is one bounded verification result.
type CheckEvidence struct {
	// Status reports whether the stated local invariant holds.
	Status EvidenceStatus `json:"status"`
	// Detail is a bounded explanation safe for remote clients.
	Detail string `json:"detail"`
}

type birthPolicy struct {
	GeneratedAt string `yaml:"generated_at"`
	Identity    struct {
		InstanceName string `yaml:"instance_name"`
		SigningKey   struct {
			PublicKeyPath string `yaml:"public_key_path"`
			Fingerprint   string `yaml:"fingerprint"`
		} `yaml:"signing_key"`
		ChannelCA struct {
			CertPath    string `yaml:"cert_path"`
			Fingerprint string `yaml:"fingerprint"`
		} `yaml:"channel_ca"`
	} `yaml:"identity"`
}

// Observe reads identity evidence from the signed birth of core and checks it
// against the currently active repository state.
func Observe(ctx context.Context, coreDir string, seeds []provenance.TrustedSigner) (Evidence, error) {
	var evidence Evidence
	coreDir = strings.TrimSpace(coreDir)
	if coreDir == "" {
		return evidence, fmt.Errorf("identity evidence: core path is empty")
	}
	absCore, err := filepath.Abs(coreDir)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: resolve core path: %w", err)
	}

	objectFormat, err := gitScalar(ctx, absCore, "rev-parse", "--show-object-format")
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: read git object format: %w", err)
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return evidence, fmt.Errorf("identity evidence: unsupported git object format %q", objectFormat)
	}

	rootOutput, err := gitScalar(ctx, absCore, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: read core birth: %w", err)
	}
	roots := nonEmptyLines(rootOutput)
	if len(roots) != 1 {
		return evidence, fmt.Errorf("identity evidence: core has %d parentless commits, want exactly one", len(roots))
	}
	rootCommit := roots[0]

	headCommit, err := gitScalar(ctx, absCore, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: read core HEAD: %w", err)
	}

	policyData, err := gitBlob(ctx, absCore, rootCommit, CoreConfigFile)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: read birth policy: %w", err)
	}
	var policy birthPolicy
	if err := yaml.Unmarshal(policyData, &policy); err != nil {
		return evidence, fmt.Errorf("identity evidence: parse birth policy: %w", err)
	}
	if policy.Identity.SigningKey.PublicKeyPath != SigningPublicKeyFile {
		return evidence, fmt.Errorf("identity evidence: birth policy names unexpected signing public key path")
	}
	if policy.Identity.ChannelCA.CertPath != ChannelCACertFile {
		return evidence, fmt.Errorf("identity evidence: birth policy names unexpected channel CA path")
	}

	publicKeyData, err := gitBlob(ctx, absCore, rootCommit, SigningPublicKeyFile)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: read birth signing key: %w", err)
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKeyData)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: parse birth signing key: %w", err)
	}
	if publicKey.Type() != ssh.KeyAlgoED25519 {
		return evidence, fmt.Errorf("identity evidence: signing key algorithm is %q, want %q", publicKey.Type(), ssh.KeyAlgoED25519)
	}
	signingFingerprint := ssh.FingerprintSHA256(publicKey)
	if signingFingerprint != strings.TrimSpace(policy.Identity.SigningKey.Fingerprint) {
		return evidence, fmt.Errorf("identity evidence: birth signing key fingerprint does not match birth policy")
	}

	certificateData, err := gitBlob(ctx, absCore, rootCommit, ChannelCACertFile)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: read birth channel CA: %w", err)
	}
	certificate, err := ParseCACertificate(certificateData)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: %w", err)
	}
	caFingerprint := certificateFingerprint(certificate)
	if caFingerprint != strings.TrimSpace(policy.Identity.ChannelCA.Fingerprint) {
		return evidence, fmt.Errorf("identity evidence: birth channel CA fingerprint does not match birth policy")
	}
	if err := verifyActiveIdentity(ctx, absCore, headCommit, signingFingerprint, caFingerprint); err != nil {
		return evidence, err
	}

	assertedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(policy.GeneratedAt))
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: parse asserted birth time: %w", err)
	}

	clean, err := coreWorktreeClean(ctx, absCore)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: inspect core worktree: %w", err)
	}
	trustChangeCount, err := trustFileChangeCount(ctx, absCore)
	if err != nil {
		return evidence, fmt.Errorf("identity evidence: inspect trust-file history: %w", err)
	}

	_, admissionErr := provenance.VerifyAdmission(ctx, absCore, seeds)
	admission := CheckEvidence{
		Status: EvidenceVerified,
		Detail: "single birth and trust-file history satisfy the declared seed policy",
	}
	if admissionErr != nil {
		admission.Status = EvidenceFailed
		admission.Detail = "core birth or trust-file history does not satisfy the declared seed policy"
	}

	head := CheckEvidence{
		Status: EvidenceFailed,
		Detail: "current core tree is not covered by trusted clean history",
	}
	verifier, verifierErr := provenance.NewVerifier(absCore, nil, provenance.Options{SeedSigners: seeds})
	if verifierErr == nil {
		if result, verifyErr := verifier.VerifyTree(ctx, ""); verifyErr == nil && result.Trusted() && clean {
			head.Status = EvidenceVerified
			head.Detail = "current core tree is clean and covered by a trusted signed HEAD"
		}
	}

	evidence = Evidence{
		SchemaVersion: 1,
		ObservedAt:    time.Now().UTC().Truncate(time.Second),
		Instance: InstanceEvidence{
			ID:   "thane:ed25519:" + signingFingerprint,
			Name: strings.TrimSpace(policy.Identity.InstanceName),
			IdentityKey: PublicKeyEvidence{
				Algorithm:   "ed25519",
				Fingerprint: signingFingerprint,
			},
			ChannelCA: PublicKeyEvidence{
				Algorithm:   "x509-ed25519",
				Fingerprint: caFingerprint,
			},
		},
		Core: CoreEvidence{
			Birth: BirthEvidence{
				Commit:        GitObjectID{Algorithm: objectFormat, OID: rootCommit},
				AssertedAt:    assertedAt.UTC(),
				TimeAssurance: "signed_claim",
				Anchor:        anchorKind(publicKey, seeds),
			},
			Head: HeadEvidence{
				Commit:               GitObjectID{Algorithm: objectFormat, OID: headCommit},
				WorktreeClean:        clean,
				TrustFileChangeCount: trustChangeCount,
			},
			Verification: VerificationEvidence{
				Admission: admission,
				Head:      head,
			},
		},
	}
	return evidence, nil
}

func anchorKind(identityKey ssh.PublicKey, seeds []provenance.TrustedSigner) string {
	if len(seeds) == 0 {
		return "unknown"
	}
	if len(seeds) != 1 || seeds[0].Principal != provenance.AgentPrincipal {
		return "operator"
	}
	seedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(seeds[0].PublicKey))
	if err != nil || !bytes.Equal(seedKey.Marshal(), identityKey.Marshal()) {
		return "operator"
	}
	return "self_signed"
}

func verifyActiveIdentity(ctx context.Context, repo, headCommit, signingFingerprint, caFingerprint string) error {
	policyData, err := gitBlob(ctx, repo, headCommit, CoreConfigFile)
	if err != nil {
		return fmt.Errorf("identity evidence: read active policy: %w", err)
	}
	var policy birthPolicy
	if err := yaml.Unmarshal(policyData, &policy); err != nil {
		return fmt.Errorf("identity evidence: parse active policy: %w", err)
	}
	if policy.Identity.SigningKey.PublicKeyPath != SigningPublicKeyFile ||
		policy.Identity.ChannelCA.CertPath != ChannelCACertFile {
		return fmt.Errorf("identity evidence: active policy names unexpected identity material")
	}

	publicKeyData, err := gitBlob(ctx, repo, headCommit, SigningPublicKeyFile)
	if err != nil {
		return fmt.Errorf("identity evidence: read active signing key: %w", err)
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKeyData)
	if err != nil {
		return fmt.Errorf("identity evidence: parse active signing key: %w", err)
	}
	activeSigningFingerprint := ssh.FingerprintSHA256(publicKey)
	if activeSigningFingerprint != strings.TrimSpace(policy.Identity.SigningKey.Fingerprint) {
		return fmt.Errorf("identity evidence: active signing key fingerprint does not match active policy")
	}
	if activeSigningFingerprint != signingFingerprint {
		return fmt.Errorf("identity evidence: identity-key rotation is not supported by schema version 1")
	}

	certificateData, err := gitBlob(ctx, repo, headCommit, ChannelCACertFile)
	if err != nil {
		return fmt.Errorf("identity evidence: read active channel CA: %w", err)
	}
	certificate, err := ParseCACertificate(certificateData)
	if err != nil {
		return fmt.Errorf("identity evidence: parse active channel CA: %w", err)
	}
	activeCAFingerprint := certificateFingerprint(certificate)
	if activeCAFingerprint != strings.TrimSpace(policy.Identity.ChannelCA.Fingerprint) {
		return fmt.Errorf("identity evidence: active channel CA fingerprint does not match active policy")
	}
	if activeCAFingerprint != caFingerprint {
		return fmt.Errorf("identity evidence: channel-CA rotation is not supported by schema version 1")
	}
	return nil
}

func gitScalar(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func gitBlob(ctx context.Context, repo, commit, name string) ([]byte, error) {
	if strings.Contains(name, ":") {
		return nil, fmt.Errorf("invalid birth artifact name %q", name)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "show", commit+":"+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func coreWorktreeClean(ctx context.Context, repo string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--quiet", "HEAD", "--")
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git diff --quiet HEAD: %w", err)
}

func trustFileChangeCount(ctx context.Context, repo string) (int, error) {
	count, err := gitScalar(ctx, repo, "rev-list", "--count", "--full-history", "HEAD", "--", provenance.TrustFileName)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(count)
	if err != nil {
		return 0, fmt.Errorf("parse git rev-list count %q: %w", count, err)
	}
	return value, nil
}

func nonEmptyLines(value string) []string {
	var lines []string
	for line := range strings.SplitSeq(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
