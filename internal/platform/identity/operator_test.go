package identity

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

func writeOperatorKey(t *testing.T, dir, comment string) string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	path := filepath.Join(dir, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen failed: %v\n%s", err, out)
	}
	return path
}

// TestSignableKeyPath covers the shapes a user.signingkey can take, because
// git accepts several that this process cannot sign with. git hands signing to
// an agent or a helper; founding core reads a private key from disk. A value
// that works perfectly for `git commit -S` may still be unusable here, and the
// operator deserves to be told which case they are in rather than left with a
// confusing failure.
func TestSignableKeyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	private := writeOperatorKey(t, dir, "operator@example.com")

	for _, tc := range []struct {
		name    string
		raw     string
		want    string
		wantWhy string
	}{
		{name: "private key path", raw: private, want: private},
		{name: "public key beside its private half", raw: private + ".pub", want: private},
		{
			name:    "literal public key",
			raw:     "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA",
			wantWhy: "cannot sign",
		},
		{
			name:    "public key with no private half",
			raw:     filepath.Join(dir, "orphan.pub"),
			wantWhy: "no private key sits beside it",
		},
		{
			name:    "path that does not exist",
			raw:     filepath.Join(dir, "absent"),
			wantWhy: "does not exist",
		},
		{
			// Silently rewriting this under our own home would report a
			// missing file and send the reader hunting for a typo that is
			// not there.
			name:    "another user's home",
			raw:     "~someone/.ssh/id_ed25519",
			wantWhy: "another user's home directory",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, why := signableKeyPath(tc.raw)
			if got != tc.want {
				t.Fatalf("signableKeyPath(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if tc.wantWhy != "" && !strings.Contains(why, tc.wantWhy) {
				t.Fatalf("reason = %q, want it to mention %q", why, tc.wantWhy)
			}
			if tc.wantWhy == "" && why != "" {
				t.Fatalf("usable key still reported a reason: %q", why)
			}
		})
	}
}

// TestBootstrapCoreAnchorsToAnOperatorKey proves the anchored posture end to
// end, and specifically that the operator's key is the one that *signs* the
// birth.
//
// Declaring a seed without signing with it is the failure this design exists
// to avoid: init would succeed and the first serve would refuse, because
// admission judges the birth against the declared seed set.
func TestBootstrapCoreAnchorsToAnOperatorKey(t *testing.T) {
	t.Parallel()
	keyDir := t.TempDir()
	keyPath := writeOperatorKey(t, keyDir, "operator@example.com")
	operator, err := LoadOperatorSigner(t.Context(), keyPath, "operator@example.com")
	if err != nil {
		t.Fatalf("LoadOperatorSigner: %v", err)
	}

	coreDir := filepath.Join(t.TempDir(), "core")
	result, err := BootstrapCore(t.Context(), coreDir, "pocket", operator, nil)
	if err != nil {
		t.Fatalf("BootstrapCore: %v", err)
	}
	if result.SelfSigned || result.OperatorPrincipal != "operator@example.com" {
		t.Fatalf("result = %+v, want an anchored core naming the operator", result)
	}

	// Admission is the real assertion: it verifies the birth against the
	// declared seed set alone, which is exactly what a declare-without-signing
	// mistake would fail.
	if _, err := provenance.VerifyAdmission(t.Context(), coreDir, []provenance.TrustedSigner{{
		Principal: operator.Principal,
		PublicKey: operator.PublicKey,
	}}); err != nil {
		t.Fatalf("an operator-founded core should be admitted by its own seed: %v", err)
	}

	// The agent stays able to sign core's contents: the seed set decides who
	// may establish the root, the trust file decides who may write in it.
	trust, err := os.ReadFile(filepath.Join(coreDir, ".allowed_signers"))
	if err != nil {
		t.Fatalf("read trust file: %v", err)
	}
	if !strings.Contains(string(trust), provenance.AgentPrincipal) {
		t.Fatalf("trust file should still name the agent so it can write core:\n%s", trust)
	}
	if !strings.Contains(string(trust), "operator@example.com") {
		t.Fatalf("trust file should name the operator:\n%s", trust)
	}
}

// TestBootstrapCoreSelfSignedIsAdmissible checks the fallback is a real
// posture rather than a broken one: a self-signed core must still be governed
// by admission, which means declaring the agent as its own seed.
func TestBootstrapCoreSelfSignedIsAdmissible(t *testing.T) {
	t.Parallel()
	coreDir := filepath.Join(t.TempDir(), "core")
	result, err := BootstrapCore(t.Context(), coreDir, "pocket", nil, nil)
	if err != nil {
		t.Fatalf("BootstrapCore: %v", err)
	}
	if !result.SelfSigned {
		t.Fatal("a core founded without an operator key should report itself self-signed")
	}

	agentKey, err := os.ReadFile(filepath.Join(coreDir, SigningPublicKeyFile))
	if err != nil {
		t.Fatalf("read agent public key: %v", err)
	}
	if _, err := provenance.VerifyAdmission(t.Context(), coreDir, []provenance.TrustedSigner{{
		Principal: provenance.AgentPrincipal,
		PublicKey: strings.TrimSpace(string(agentKey)),
	}}); err != nil {
		t.Fatalf("a self-signed core should be admitted by its own agent seed: %v", err)
	}
}
