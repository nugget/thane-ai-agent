package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

func TestX509CertificateEvidenceSelfSignedUsesRawSignature(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if err := certificate.CheckSignatureFrom(certificate); err == nil {
		t.Fatal("CheckSignatureFrom accepted a non-CA certificate; test does not exercise the intended distinction")
	}

	got := x509CertificateEvidence(certificate)
	if !got.SelfSigned {
		t.Fatal("SelfSigned = false for a certificate signed by its own public key")
	}
}

func TestObserveCoreIdentityEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		operator   bool
		wantAnchor string
	}{
		{name: "self signed", wantAnchor: "self_signed"},
		{name: "operator anchored", operator: true, wantAnchor: "operator"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			coreDir, seeds := evidenceCore(t, tc.operator)

			got, err := Observe(t.Context(), coreDir, seeds)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.SchemaVersion != 1 {
				t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
			}
			if got.Instance.Name != "pocket" {
				t.Errorf("instance name = %q, want pocket", got.Instance.Name)
			}
			if !strings.HasPrefix(got.Instance.ID, "thane:ed25519:SHA256:") {
				t.Errorf("instance id = %q, want fingerprint-derived id", got.Instance.ID)
			}
			if got.Instance.IdentityKey.Fingerprint == "" || got.Instance.ChannelCA.Fingerprint == "" {
				t.Errorf("public fingerprints are incomplete: %+v", got.Instance)
			}
			certificate := got.Instance.ChannelCA.Certificate
			if certificate == nil {
				t.Fatal("channel CA certificate metadata is missing")
			}
			if certificate.Subject != "CN=pocket Thane Channel CA" || certificate.Issuer != certificate.Subject {
				t.Errorf("channel CA names = subject %q, issuer %q", certificate.Subject, certificate.Issuer)
			}
			if certificate.SerialNumber == "" {
				t.Error("channel CA serial number is empty")
			}
			if !certificate.IsCA || !certificate.SelfSigned {
				t.Errorf("channel CA posture = is_ca %t, self_signed %t", certificate.IsCA, certificate.SelfSigned)
			}
			if certificate.PublicKeyAlgorithm != "Ed25519" || certificate.SignatureAlgorithm != "Ed25519" {
				t.Errorf("channel CA algorithms = public %q, signature %q", certificate.PublicKeyAlgorithm, certificate.SignatureAlgorithm)
			}
			if !certificate.NotAfter.After(certificate.NotBefore) {
				t.Errorf("channel CA validity = %s through %s", certificate.NotBefore, certificate.NotAfter)
			}
			if got.Core.Birth.Anchor != tc.wantAnchor {
				t.Errorf("anchor = %q, want %q", got.Core.Birth.Anchor, tc.wantAnchor)
			}
			if got.Core.Birth.TimeAssurance != "signed_claim" {
				t.Errorf("time assurance = %q, want signed_claim", got.Core.Birth.TimeAssurance)
			}
			if got.Core.Birth.Commit.Algorithm != "sha1" || got.Core.Birth.Commit.OID == "" {
				t.Errorf("birth commit = %+v, want algorithm-qualified oid", got.Core.Birth.Commit)
			}
			if got.Core.CurrentCommit.Algorithm != "sha1" || got.Core.CurrentCommit.OID != strings.TrimSpace(gitOutput(t, coreDir, "rev-parse", "HEAD")) {
				t.Errorf("current commit = %+v, want active core HEAD", got.Core.CurrentCommit)
			}
			if got.Core.Verification.Admission.Status != EvidenceVerified {
				t.Errorf("admission = %+v, want verified", got.Core.Verification.Admission)
			}
			if got.Core.Verification.Head.Status != EvidenceVerified || !got.Core.Head.WorktreeClean {
				t.Errorf("head evidence = %+v / %+v, want verified clean head", got.Core.Head, got.Core.Verification.Head)
			}
			if got.Core.Head.TrustFileChangeCount != 1 {
				t.Errorf("trust-file change count = %d, want 1", got.Core.Head.TrustFileChangeCount)
			}
		})
	}
}

func TestObserveReportsFailedAdmissionWithoutLosingIdentity(t *testing.T) {
	t.Parallel()
	coreDir, _ := evidenceCore(t, false)
	other, err := GenerateSigningKeyPair("other")
	if err != nil {
		t.Fatalf("GenerateSigningKeyPair: %v", err)
	}

	got, err := Observe(t.Context(), coreDir, []provenance.TrustedSigner{{
		Principal: "other@example.com",
		PublicKey: other.Public,
	}})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Instance.ID == "" {
		t.Fatal("instance identity was lost when admission failed")
	}
	if got.Core.Verification.Admission.Status != EvidenceFailed {
		t.Fatalf("admission = %+v, want failed", got.Core.Verification.Admission)
	}
}

func TestObserveReportsDirtyCore(t *testing.T) {
	t.Parallel()
	coreDir, seeds := evidenceCore(t, false)
	configPath := filepath.Join(coreDir, CoreConfigFile)
	if err := os.WriteFile(configPath, []byte("changed: true\n"), 0o644); err != nil {
		t.Fatalf("dirty config: %v", err)
	}

	got, err := Observe(t.Context(), coreDir, seeds)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Core.Head.WorktreeClean {
		t.Fatal("worktree_clean = true for a modified tracked config")
	}
	if got.Core.Verification.Head.Status != EvidenceFailed {
		t.Fatalf("head = %+v, want failed", got.Core.Verification.Head)
	}
}

// TestObserveSurvivesAFileNamedHEAD pins the revision/path hardening on the
// evidence git helpers. Core is a directory an agent writes files into, and
// a loose file named HEAD makes a bare revision argument ambiguous with a
// path — git then refuses with usage advice instead of answering, which
// would fail the whole observation (and 503 /v1/identity) over a file
// beside the history rather than anything wrong with the history itself.
func TestObserveSurvivesAFileNamedHEAD(t *testing.T) {
	t.Parallel()
	coreDir, seeds := evidenceCore(t, false)
	if err := os.WriteFile(filepath.Join(coreDir, "HEAD"), []byte("a document, not a revision\n"), 0o644); err != nil {
		t.Fatalf("write HEAD file: %v", err)
	}

	got, err := Observe(t.Context(), coreDir, seeds)
	if err != nil {
		t.Fatalf("Observe with a file named HEAD: %v", err)
	}
	if got.Core.Birth.Commit.OID == "" {
		t.Fatal("birth commit OID is empty")
	}
	if got.Core.Verification.Admission.Status != EvidenceVerified {
		t.Fatalf("admission = %+v, want verified", got.Core.Verification.Admission)
	}
}

// TestObserveWithoutSeedsSaysNoPolicyRatherThanFailedPolicy pins the
// honesty split in the admission detail: with no declared seeds there is
// no policy for core's history to fail, and the evidence must say the
// birth cannot be judged rather than claim a nonexistent policy was
// checked and unsatisfied.
func TestObserveWithoutSeedsSaysNoPolicyRatherThanFailedPolicy(t *testing.T) {
	t.Parallel()
	coreDir, _ := evidenceCore(t, false)

	got, err := Observe(t.Context(), coreDir, nil)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Core.Verification.Admission.Status != EvidenceFailed {
		t.Fatalf("admission = %+v, want failed", got.Core.Verification.Admission)
	}
	if want := "no seed policy is declared for core, so its birth cannot be judged"; got.Core.Verification.Admission.Detail != want {
		t.Fatalf("admission detail = %q, want %q", got.Core.Verification.Admission.Detail, want)
	}
	if got.Core.Birth.Anchor != "unknown" {
		t.Fatalf("anchor = %q, want unknown when no seeds are declared", got.Core.Birth.Anchor)
	}
}

func TestObserveRejectsMissingCore(t *testing.T) {
	t.Parallel()
	if _, err := Observe(t.Context(), filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("Observe missing core returned nil error")
	}
}

func evidenceCore(t *testing.T, operatorAnchored bool) (string, []provenance.TrustedSigner) {
	t.Helper()
	coreDir := filepath.Join(t.TempDir(), "core")
	var operator *OperatorSigner
	if operatorAnchored {
		pair, err := GenerateSigningKeyPair("operator")
		if err != nil {
			t.Fatalf("GenerateSigningKeyPair operator: %v", err)
		}
		keyPath := filepath.Join(t.TempDir(), "operator_ed25519")
		if err := os.WriteFile(keyPath, pair.PrivatePEM, 0o600); err != nil {
			t.Fatalf("write operator key: %v", err)
		}
		operator = &OperatorSigner{
			Principal:      "operator@example.com",
			PublicKey:      pair.Public,
			PrivateKeyPath: keyPath,
		}
	}
	if _, err := BootstrapCore(t.Context(), coreDir, "pocket", operator, "", nil, nil); err != nil {
		t.Fatalf("BootstrapCore: %v", err)
	}
	if operator != nil {
		return coreDir, []provenance.TrustedSigner{{
			Principal: operator.Principal,
			PublicKey: operator.PublicKey,
		}}
	}
	publicKey, err := os.ReadFile(filepath.Join(coreDir, SigningPublicKeyFile))
	if err != nil {
		t.Fatalf("read signing public key: %v", err)
	}
	return coreDir, []provenance.TrustedSigner{{
		Principal: provenance.AgentPrincipal,
		PublicKey: strings.TrimSpace(string(publicKey)),
	}}
}
