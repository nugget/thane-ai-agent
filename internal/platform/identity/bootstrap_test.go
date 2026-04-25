package identity

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"
)

func clearUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestBootstrapCoreCreatesSignedBirthCommit(t *testing.T) {
	requireGit(t)
	clearUmask(t)

	coreDir := filepath.Join(t.TempDir(), "core")
	result, err := BootstrapCore(t.Context(), coreDir, "pocket", nil)
	if err != nil {
		t.Fatalf("BootstrapCore: %v", err)
	}
	if !result.Created {
		t.Fatal("Created = false, want true")
	}
	if result.SigningKeyFingerprint == "" || result.ChannelCAFingerprint == "" {
		t.Fatalf("missing fingerprints: %+v", result)
	}

	for _, rel := range []string{
		CoreConfigFile,
		SigningPrivateKeyFile,
		SigningPublicKeyFile,
		ChannelCAKeyFile,
		ChannelCACertFile,
		".gitignore",
		".allowed_signers",
	} {
		if _, err := os.Stat(filepath.Join(coreDir, rel)); err != nil {
			t.Fatalf("expected %s: %v", rel, err)
		}
	}

	assertMode(t, filepath.Join(coreDir, SigningPrivateKeyFile), 0o600)
	assertMode(t, filepath.Join(coreDir, ChannelCAKeyFile), 0o600)
	assertMode(t, filepath.Join(coreDir, SigningPublicKeyFile), 0o644)
	assertMode(t, filepath.Join(coreDir, ChannelCACertFile), 0o644)

	certPEM, err := os.ReadFile(filepath.Join(coreDir, ChannelCACertFile))
	if err != nil {
		t.Fatalf("read CA cert: %v", err)
	}
	if _, err := ParseCACertificate(certPEM); err != nil {
		t.Fatalf("CA cert is not parseable: %v", err)
	}

	var cfg coreConfig
	cfgData, err := os.ReadFile(filepath.Join(coreDir, CoreConfigFile))
	if err != nil {
		t.Fatalf("read core config: %v", err)
	}
	if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("unmarshal core config: %v", err)
	}
	if cfg.Identity.InstanceName != "pocket" {
		t.Fatalf("instance_name = %q, want pocket", cfg.Identity.InstanceName)
	}
	if cfg.Identity.SigningKey.Fingerprint != result.SigningKeyFingerprint {
		t.Fatalf("signing fingerprint = %q, want %q", cfg.Identity.SigningKey.Fingerprint, result.SigningKeyFingerprint)
	}
	if cfg.Identity.ChannelCA.Fingerprint != result.ChannelCAFingerprint {
		t.Fatalf("CA fingerprint = %q, want %q", cfg.Identity.ChannelCA.Fingerprint, result.ChannelCAFingerprint)
	}

	tracked := gitOutput(t, coreDir, "ls-files")
	trackedFiles := strings.Split(strings.TrimSpace(tracked), "\n")
	for _, want := range []string{CoreConfigFile, SigningPublicKeyFile, ChannelCACertFile, ".gitignore", ".allowed_signers"} {
		if !containsString(trackedFiles, want) {
			t.Fatalf("tracked files missing %s:\n%s", want, tracked)
		}
	}
	for _, private := range []string{SigningPrivateKeyFile, ChannelCAKeyFile} {
		if containsString(trackedFiles, private) {
			t.Fatalf("private key %s was committed:\n%s", private, tracked)
		}
	}

	if got := strings.TrimSpace(gitOutput(t, coreDir, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("commit count = %s, want 1", got)
	}
	if status := strings.TrimSpace(gitOutput(t, coreDir, "status", "--short", "--untracked-files=all")); status != "" {
		t.Fatalf("core git status = %q, want clean", status)
	}
	verifyGitCommit(t, coreDir)
}

func TestBootstrapCoreSkipsCompleteIdentity(t *testing.T) {
	requireGit(t)

	coreDir := filepath.Join(t.TempDir(), "core")
	if _, err := BootstrapCore(t.Context(), coreDir, "pocket", nil); err != nil {
		t.Fatalf("first BootstrapCore: %v", err)
	}
	result, err := BootstrapCore(t.Context(), coreDir, "pocket", nil)
	if err != nil {
		t.Fatalf("second BootstrapCore: %v", err)
	}
	if result.Created {
		t.Fatal("Created = true, want false for existing identity")
	}
	if got := strings.TrimSpace(gitOutput(t, coreDir, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("commit count = %s, want 1", got)
	}
}

func TestBootstrapCoreRejectsPartialIdentity(t *testing.T) {
	coreDir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(filepath.Join(coreDir, "identity"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, SigningPublicKeyFile), []byte("ssh-ed25519 test"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := BootstrapCore(t.Context(), coreDir, "pocket", nil); err == nil {
		t.Fatal("BootstrapCore partial identity returned nil, want error")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %o, want %o", path, got, want)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func verifyGitCommit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "verify-commit", "HEAD")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git verify-commit failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if combined := stdout.String() + stderr.String(); !strings.Contains(combined, "Good") {
		t.Fatalf("verify-commit output missing Good signature:\n%s", combined)
	}
}
