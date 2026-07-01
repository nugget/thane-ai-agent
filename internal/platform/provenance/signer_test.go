package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCommitSigner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		verdict      string
		principal    string
		wantVerified bool
		wantKind     string
		wantReason   string // substring; "" means Reason must be empty
	}{
		{"agent good", "G", AgentPrincipal, true, signerKindAgent, ""},
		{"operator good", "G", "alice@example.com", true, signerKindOperator, ""},
		{"valid but untrusted", "U", "", false, signerKindUnknown, "allow-list"},
		{"unsigned", "N", "", false, signerKindUnknown, "unsigned"},
		{"empty verdict is unsigned", "", "", false, signerKindUnknown, "unsigned"},
		{"bad signature", "B", "", false, signerKindUnknown, "bad"},
		{"cannot verify", "E", "", false, signerKindUnknown, "cannot"},
		{"revoked", "R", "bob@example.com", false, signerKindOperator, "revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cs := parseCommitSigner(tc.verdict, tc.principal, "SHA256:fp")
			if cs.Verified != tc.wantVerified {
				t.Fatalf("Verified = %v, want %v", cs.Verified, tc.wantVerified)
			}
			if cs.Kind != tc.wantKind {
				t.Fatalf("Kind = %q, want %q", cs.Kind, tc.wantKind)
			}
			if tc.wantReason == "" {
				if cs.Reason != "" {
					t.Fatalf("Reason = %q, want empty", cs.Reason)
				}
			} else if !strings.Contains(cs.Reason, tc.wantReason) {
				t.Fatalf("Reason = %q, want containing %q", cs.Reason, tc.wantReason)
			}
		})
	}
}

func TestSignerForAgentCommit(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	head, err := s.ResolveRevision(ctx, "kb/doc.md", "HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	cs, err := s.SignerFor(ctx, head.Commit)
	if err != nil {
		t.Fatalf("SignerFor: %v", err)
	}
	if !cs.Verified {
		t.Fatalf("agent commit not verified: %+v", cs)
	}
	if cs.Kind != signerKindAgent {
		t.Fatalf("Kind = %q, want agent", cs.Kind)
	}
	if cs.Principal != AgentPrincipal {
		t.Fatalf("Principal = %q, want %q", cs.Principal, AgentPrincipal)
	}
	if cs.Reason != "" {
		t.Fatalf("Reason = %q, want empty for a verified commit", cs.Reason)
	}
}

func TestSignerForUntrusted(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()
	head, _ := s.ResolveRevision(ctx, "kb/doc.md", "HEAD")

	// Replace the trust file with a different key: HEAD's signature is still
	// cryptographically intact, but its signer is no longer trusted.
	other := testSigner(t)
	if err := os.WriteFile(filepath.Join(s.path, ".allowed_signers"),
		[]byte(AgentPrincipal+" "+other.PublicKey()+"\n"), 0o644); err != nil {
		t.Fatalf("overwrite .allowed_signers: %v", err)
	}
	cs, err := s.SignerFor(ctx, head.Commit)
	if err != nil {
		t.Fatalf("SignerFor: %v", err)
	}
	if cs.Verified {
		t.Fatalf("untrusted commit reported verified: %+v", cs)
	}
	if cs.Reason == "" {
		t.Fatal("untrusted commit has empty Reason")
	}
}

func TestRevisionsWithSigners(t *testing.T) {
	s := buildReaderRepo(t)
	ctx := t.Context()

	// Without the option, signers are not populated.
	plain, err := s.Revisions(ctx, "kb/doc.md", RevisionOptions{})
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if plain.Revisions[0].Signer != nil {
		t.Fatalf("Signer populated without WithSigners: %+v", plain.Revisions[0].Signer)
	}

	page, err := s.Revisions(ctx, "kb/doc.md", RevisionOptions{WithSigners: true})
	if err != nil {
		t.Fatalf("Revisions WithSigners: %v", err)
	}
	if len(page.Revisions) != 3 {
		t.Fatalf("len = %d, want 3", len(page.Revisions))
	}
	for i, rev := range page.Revisions {
		if rev.Signer == nil {
			t.Fatalf("Revisions[%d].Signer is nil with WithSigners", i)
		}
		if !rev.Signer.Verified || rev.Signer.Kind != signerKindAgent {
			t.Fatalf("Revisions[%d].Signer = %+v, want verified agent", i, rev.Signer)
		}
	}
}
