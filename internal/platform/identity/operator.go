package identity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// OperatorSigner is a key held by a person rather than by the instance.
//
// Founding core with one is what anchors an instance to someone outside
// itself. Without it core is self-signed: it works, and it is honest about
// what it is, but nothing external attests to it and the agent can
// re-establish the root that holds its own config.
type OperatorSigner struct {
	// Principal is the allowed_signers identity, conventionally an email.
	Principal string
	// PublicKey is the key in authorized_keys form, for the trust file and
	// the seed declaration.
	PublicKey string
	// PrivateKeyPath is the key core's birth commit is signed with. It is
	// required: a key that can only be declared and not signed with would
	// produce a root whose birth no declared seed signed, which admission
	// refuses — an instance that initializes cleanly and then refuses to
	// serve.
	PrivateKeyPath string
}

// LoadOperatorSigner resolves an operator key from a private key path.
//
// The principal is the identity the key signs as. It falls back to git's
// user.email because that is what the operator already curates, and an
// allowed_signers entry needs a name whether or not anyone chose one for this
// purpose.
func LoadOperatorSigner(ctx context.Context, keyPath, principal string) (*OperatorSigner, error) {
	expanded, err := expandHome(strings.TrimSpace(keyPath))
	if err != nil {
		return nil, err
	}
	if expanded == "" {
		return nil, fmt.Errorf("no operator key path given")
	}
	signer, err := provenance.NewSSHFileSigner(expanded)
	if err != nil {
		return nil, err
	}
	if principal = strings.TrimSpace(principal); principal == "" {
		principal = gitConfigValue(ctx, "user.email")
	}
	if principal == "" {
		return nil, fmt.Errorf("cannot name the operator key's identity: set git's user.email, or pass -operator-principal")
	}
	return &OperatorSigner{
		Principal:      principal,
		PublicKey:      signer.PublicKey(),
		PrivateKeyPath: expanded,
	}, nil
}

// DiscoverOperatorSigner resolves an operator key from the operator's own git
// configuration, so an instance anchors itself without anyone being asked to
// think about trust roots on first run.
//
// It returns a reason rather than an error when it finds nothing usable.
// Failing to discover a key is the ordinary case on a machine that does not
// sign commits, and it produces a self-signed core rather than a failed setup
// — but the operator should be told which of the several reasons applied,
// because each has a different remedy and only some are worth acting on.
func DiscoverOperatorSigner(ctx context.Context, principal string) (*OperatorSigner, string) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, "git is not installed, so there is no operator configuration to read"
	}
	switch format := gitConfigValue(ctx, "gpg.format"); {
	case format == "":
		return nil, "git is not configured to sign commits with an SSH key (gpg.format is unset)"
	case !strings.EqualFold(format, "ssh"):
		return nil, fmt.Sprintf("git signs commits with %s rather than ssh, so user.signingkey is not an SSH key", format)
	}
	raw := gitConfigValue(ctx, "user.signingkey")
	if raw == "" {
		return nil, "git config names no user.signingkey"
	}
	keyPath, why := signableKeyPath(raw)
	if keyPath == "" {
		return nil, why
	}
	signer, err := LoadOperatorSigner(ctx, keyPath, principal)
	if err != nil {
		return nil, err.Error()
	}
	return signer, ""
}

// signableKeyPath maps a user.signingkey value onto a private key this process
// can actually sign with, or explains why it cannot.
//
// The distinction matters more than it looks. git is happy to sign with a key
// it only knows the public half of, because it hands the work to an agent or a
// helper program. Founding core happens here, in this process, from a file — so
// a value that git signs with perfectly well may still be unusable, and saying
// so is better than a confusing failure later.
func signableKeyPath(raw string) (path, why string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "key::") {
		return "", "user.signingkey is a literal public key, which cannot sign anything on its own"
	}
	expanded, err := expandHome(raw)
	if err != nil {
		return "", err.Error()
	}
	// A .pub value is the common shape, and the private half conventionally
	// sits beside it under the same name.
	if strings.HasSuffix(expanded, ".pub") {
		private := strings.TrimSuffix(expanded, ".pub")
		if fileExists(private) {
			return private, ""
		}
		return "", fmt.Sprintf("user.signingkey names the public key %s and no private key sits beside it", expanded)
	}
	if !fileExists(expanded) {
		return "", fmt.Sprintf("user.signingkey names %s, which does not exist", expanded)
	}
	return expanded, ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func expandHome(path string) (string, error) {
	if path == "" || !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for %s: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
}

// gitConfigValue reads one git configuration value, treating every failure as
// absence: this is discovery, and a missing key, an unreadable config, and a
// git that will not run are all the same answer to the caller.
func gitConfigValue(ctx context.Context, key string) string {
	out, err := exec.CommandContext(ctx, "git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
