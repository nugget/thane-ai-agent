package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

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
			if got.Core.Birth.Anchor != tc.wantAnchor {
				t.Errorf("anchor = %q, want %q", got.Core.Birth.Anchor, tc.wantAnchor)
			}
			if got.Core.Birth.TimeAssurance != "signed_claim" {
				t.Errorf("time assurance = %q, want signed_claim", got.Core.Birth.TimeAssurance)
			}
			if got.Core.Birth.Commit.Algorithm != "sha1" || got.Core.Birth.Commit.OID == "" {
				t.Errorf("birth commit = %+v, want algorithm-qualified oid", got.Core.Birth.Commit)
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
	if _, err := BootstrapCore(t.Context(), coreDir, "pocket", operator, nil, nil); err != nil {
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
