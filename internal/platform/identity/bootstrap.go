package identity

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"gopkg.in/yaml.v3"
)

const (
	// CoreConfigFile is the policy document committed into the core root.
	CoreConfigFile = "config.yaml"
	// SigningPrivateKeyFile is the private Ed25519 signing key path.
	SigningPrivateKeyFile = "identity/signing_ed25519"
	// SigningPublicKeyFile is the public Ed25519 signing key path.
	SigningPublicKeyFile = "identity/signing_ed25519.pub"
	// ChannelCAKeyFile is the private channel CA key path.
	ChannelCAKeyFile = "ca/channel_root.key"
	// ChannelCACertFile is the public channel CA certificate path.
	ChannelCACertFile = "ca/channel_root.crt"
)

const coreGitIgnore = `identity/signing_ed25519
ca/channel_root.key
`

// BootstrapResult describes the outcome of a core identity bootstrap.
type BootstrapResult struct {
	Created               bool
	CoreDir               string
	SigningKeyFingerprint string
	ChannelCAFingerprint  string
}

// BootstrapCore initializes the core trust root for a Thane instance.
// Private key material is written under core/ with 0600 permissions and
// ignored by git. Public key material, the channel CA certificate, and
// core/config.yaml are committed together as the signed birth commit.
func BootstrapCore(ctx context.Context, coreDir, instanceName string, logger *slog.Logger) (*BootstrapResult, error) {
	absCoreDir, err := filepath.Abs(coreDir)
	if err != nil {
		return nil, fmt.Errorf("resolve core dir: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if instanceName = strings.TrimSpace(instanceName); instanceName == "" {
		instanceName = "thane"
	}

	if state, err := existingCoreIdentity(absCoreDir); err != nil {
		return nil, err
	} else if state.complete {
		return &BootstrapResult{Created: false, CoreDir: absCoreDir}, nil
	} else if state.partial {
		return nil, fmt.Errorf("core identity appears partially initialized in %s", absCoreDir)
	}

	created := false
	defer func() {
		if !created {
			cleanupBootstrapArtifacts(absCoreDir, logger)
		}
	}()

	if err := os.MkdirAll(filepath.Join(absCoreDir, "identity"), 0o755); err != nil {
		return nil, fmt.Errorf("create identity directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(absCoreDir, "ca"), 0o755); err != nil {
		return nil, fmt.Errorf("create CA directory: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	signing, err := GenerateSigningKeyPair(instanceName)
	if err != nil {
		return nil, err
	}
	channelCA, err := GenerateCertificateAuthority(instanceName+" Thane Channel CA", now)
	if err != nil {
		return nil, err
	}

	if err := writePrivateFile(filepath.Join(absCoreDir, SigningPrivateKeyFile), signing.PrivatePEM); err != nil {
		return nil, err
	}
	if err := writePrivateFile(filepath.Join(absCoreDir, ChannelCAKeyFile), channelCA.PrivatePEM); err != nil {
		return nil, err
	}

	signer, err := provenance.NewSSHSignerFromKey(signing.PrivateKey)
	if err != nil {
		return nil, err
	}
	store, err := provenance.New(absCoreDir, signer, logger)
	if err != nil {
		return nil, err
	}

	policy, err := renderCoreConfig(instanceName, now, signing, channelCA)
	if err != nil {
		return nil, err
	}

	if err := store.WriteFiles(ctx, map[string]string{
		".gitignore":         coreGitIgnore,
		".allowed_signers":   fmt.Sprintf("thane@provenance.local %s\n", strings.TrimSpace(signing.Public)),
		SigningPublicKeyFile: signing.Public,
		ChannelCACertFile:    string(channelCA.Certificate),
		CoreConfigFile:       string(policy),
	}, "bootstrap core identity"); err != nil {
		return nil, err
	}

	created = true
	return &BootstrapResult{
		Created:               true,
		CoreDir:               absCoreDir,
		SigningKeyFingerprint: signing.Fingerprint,
		ChannelCAFingerprint:  channelCA.Fingerprint,
	}, nil
}

func cleanupBootstrapArtifacts(coreDir string, logger *slog.Logger) {
	for _, rel := range []string{
		CoreConfigFile,
		SigningPrivateKeyFile,
		SigningPublicKeyFile,
		ChannelCAKeyFile,
		ChannelCACertFile,
		".gitignore",
		".allowed_signers",
	} {
		path := filepath.Join(coreDir, rel)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Warn("failed to clean up core identity bootstrap artifact", "path", path, "error", err)
		}
	}

	if err := os.RemoveAll(filepath.Join(coreDir, ".git")); err != nil {
		logger.Warn("failed to clean up core identity git repository", "path", filepath.Join(coreDir, ".git"), "error", err)
	}

	for _, rel := range []string{"identity", "ca", ""} {
		path := filepath.Join(coreDir, rel)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Debug("core identity bootstrap directory not removed", "path", path, "error", err)
		}
	}
}

type coreIdentityState struct {
	complete bool
	partial  bool
}

func existingCoreIdentity(coreDir string) (coreIdentityState, error) {
	paths := []string{
		".git",
		".gitignore",
		".allowed_signers",
		CoreConfigFile,
		SigningPrivateKeyFile,
		SigningPublicKeyFile,
		ChannelCAKeyFile,
		ChannelCACertFile,
	}

	found := 0
	for _, rel := range paths {
		_, err := os.Stat(filepath.Join(coreDir, rel))
		switch {
		case err == nil:
			found++
		case os.IsNotExist(err):
		default:
			return coreIdentityState{}, fmt.Errorf("stat core identity file %s: %w", rel, err)
		}
	}
	return coreIdentityState{
		complete: found == len(paths),
		partial:  found > 0 && found < len(paths),
	}, nil
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private file %s: %w", path, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write private file %s: %w", path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close private file %s: %w", path, closeErr)
	}
	return nil
}

type coreConfig struct {
	Version     int              `yaml:"version"`
	GeneratedAt string           `yaml:"generated_at"`
	Identity    identityPolicy   `yaml:"identity"`
	Trust       trustPolicy      `yaml:"trust"`
	Delegation  delegationPolicy `yaml:"delegation"`
	Channels    channelPolicy    `yaml:"channels"`
}

type identityPolicy struct {
	InstanceName string         `yaml:"instance_name"`
	SigningKey   signingKeyRef  `yaml:"signing_key"`
	ChannelCA    certificateRef `yaml:"channel_ca"`
}

type signingKeyRef struct {
	PublicKeyPath string `yaml:"public_key_path"`
	Fingerprint   string `yaml:"fingerprint"`
}

type certificateRef struct {
	CertPath    string `yaml:"cert_path"`
	Fingerprint string `yaml:"fingerprint"`
	NotBefore   string `yaml:"not_before"`
	NotAfter    string `yaml:"not_after"`
}

type trustPolicy struct {
	TrustedPeerCAs []string `yaml:"trusted_peer_ca_fingerprints"`
	Revocations    []string `yaml:"revocations"`
}

type delegationPolicy struct {
	IssueDelegationCerts bool     `yaml:"issue_delegation_certs"`
	MaxDepth             int      `yaml:"max_depth"`
	DefaultLifetime      string   `yaml:"default_lifetime"`
	Profiles             []string `yaml:"profiles"`
}

type channelPolicy struct {
	InboundAuth     string   `yaml:"inbound_auth"`
	AcceptTOFU      bool     `yaml:"accept_tofu"`
	AllowedKeyTypes []string `yaml:"allowed_key_types"`
}

func renderCoreConfig(instanceName string, generatedAt time.Time, signing *SigningKeyPair, ca *CertificateAuthority) ([]byte, error) {
	cfg := coreConfig{
		Version:     1,
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Identity: identityPolicy{
			InstanceName: instanceName,
			SigningKey: signingKeyRef{
				PublicKeyPath: SigningPublicKeyFile,
				Fingerprint:   signing.Fingerprint,
			},
			ChannelCA: certificateRef{
				CertPath:    ChannelCACertFile,
				Fingerprint: ca.Fingerprint,
				NotBefore:   ca.NotBefore.Format(time.RFC3339),
				NotAfter:    ca.NotAfter.Format(time.RFC3339),
			},
		},
		Trust: trustPolicy{
			TrustedPeerCAs: []string{},
			Revocations:    []string{},
		},
		Delegation: delegationPolicy{
			IssueDelegationCerts: true,
			MaxDepth:             1,
			DefaultLifetime:      "1h",
			Profiles:             []string{"read_only_peer", "task_scoped_delegate"},
		},
		Channels: channelPolicy{
			InboundAuth:     "mtls_required",
			AcceptTOFU:      false,
			AllowedKeyTypes: []string{"ed25519"},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal core config: %w", err)
	}
	return data, nil
}

// ParseCACertificate decodes a generated CA certificate from PEM. It is
// exported for tests and future identity loaders.
func ParseCACertificate(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("missing certificate PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}
