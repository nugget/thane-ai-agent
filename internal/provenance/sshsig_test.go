package provenance

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestSSHSigSign verifies that sshsigSign produces output that
// ssh-keygen -Y verify accepts. This is the strongest correctness
// guarantee — if OpenSSH accepts the signature, git will too.
func TestSSHSigSign(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}

	// Generate an ed25519 key pair in-process.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatal(err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("tree abc123\nauthor Test <test@example.com>\n\ntest commit\n")

	armoredSig, err := sshsigSign(signer, payload)
	if err != nil {
		t.Fatalf("sshsigSign: %v", err)
	}

	// Verify the armor envelope.
	sigStr := string(armoredSig)
	if !strings.HasPrefix(sigStr, "-----BEGIN SSH SIGNATURE-----\n") {
		t.Error("missing BEGIN marker")
	}
	if !strings.HasSuffix(sigStr, "-----END SSH SIGNATURE-----") {
		t.Error("missing END marker")
	}

	// Write files for ssh-keygen verification.
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "data")
	sigFile := filepath.Join(dir, "data.sig")
	allowedSignersFile := filepath.Join(dir, "allowed_signers")

	if err := os.WriteFile(dataFile, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigFile, armoredSig, 0o644); err != nil {
		t.Fatal(err)
	}

	// Build allowed_signers entry: "test@example.com <key-type> <base64-key>"
	authorizedKey := ssh.MarshalAuthorizedKey(sshPub)
	allowedLine := "test@example.com " + strings.TrimSpace(string(authorizedKey)) + "\n"
	if err := os.WriteFile(allowedSignersFile, []byte(allowedLine), 0o644); err != nil {
		t.Fatal(err)
	}

	// ssh-keygen -Y verify -f allowed_signers -I test@example.com -n git -s sig < data
	cmd := exec.Command("ssh-keygen",
		"-Y", "verify",
		"-f", allowedSignersFile,
		"-I", "test@example.com",
		"-n", sshsigNamespace,
		"-s", sigFile,
	)
	cmd.Stdin, err = os.Open(dataFile)
	if err != nil {
		t.Fatal(err)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen verify failed: %v\noutput: %s", err, out)
	}

	if !strings.Contains(string(out), "Good") {
		t.Errorf("expected 'Good' in ssh-keygen output, got: %s", out)
	}
}

// TestSSHSigDeterministicFormat verifies structural properties of the
// armored signature output.
func TestSSHSigDeterministicFormat(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatal(err)
	}

	sig, err := sshsigSign(signer, []byte("test payload"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(sig), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	if lines[0] != "-----BEGIN SSH SIGNATURE-----" {
		t.Errorf("first line = %q, want BEGIN marker", lines[0])
	}
	if lines[len(lines)-1] != "-----END SSH SIGNATURE-----" {
		t.Errorf("last line = %q, want END marker", lines[len(lines)-1])
	}

	// Verify base64 lines are at most 76 chars.
	for i, line := range lines[1 : len(lines)-1] {
		if len(line) > 76 {
			t.Errorf("line %d length %d exceeds 76 chars", i+1, len(line))
		}
	}
}
