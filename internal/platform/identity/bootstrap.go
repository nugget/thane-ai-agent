package identity

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
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
	// SelfSigned reports that this bootstrap founded core without an
	// operator key, so its only seed signer is the instance's own agent
	// key: nothing outside the instance attests to it and the agent can
	// re-establish the root holding its config. On a Created result it is
	// always equal to OperatorPrincipal == "" — both are derived from the
	// same fact, so they cannot disagree. When Created is false no
	// bootstrap ran, and neither field claims anything about the existing
	// core's posture.
	SelfSigned bool
	// OperatorPrincipal is the principal of the operator key that founded
	// core; empty when core founded itself with its own agent key.
	OperatorPrincipal string
}

// coreTrustSurface renders core's in-tree trust file.
//
// The operator is listed alongside the agent rather than instead of it: the
// seed set decides who may establish the root, while this file decides who may
// sign its contents, and the agent has to keep writing ego.md and
// metacognitive.md after an operator founds the root. Keeping the two distinct
// is what makes the anchored shape usable rather than merely strict.
func coreTrustSurface(agentPublicKey string, operator *OperatorSigner) (string, error) {
	var operators []provenance.TrustedSigner
	if operator != nil {
		operators = append(operators, provenance.TrustedSigner{
			Principal: operator.Principal,
			PublicKey: operator.PublicKey,
		})
	}
	return provenance.RenderAllowedSigners(agentPublicKey, operators)
}

func operatorPrincipal(operator *OperatorSigner) string {
	if operator == nil {
		return ""
	}
	return operator.Principal
}

// BootstrapCore initializes the core trust root for a Thane instance.
// Private key material is written under core/ with 0600 permissions and
// ignored by git. Public key material, the channel CA certificate, and
// core/config.yaml are committed together as the signed birth commit.
// operatorContactID, when non-empty, is written as the stable runtime
// operator identity. New cores also declare an explicit empty
// person.contact_bindings map so signed config owns that relationship from
// birth rather than inheriting the legacy CardDAV mutation path.
// birthFiles are additional repo-relative files to include in the birth
// commit. The talent set travels this way: talents steer every turn, so an
// instance whose behaviour definitions are covered by the same operator
// signature that founds it is attested from its first moment rather than from
// whenever someone remembers to commit them.
func BootstrapCore(ctx context.Context, coreDir, instanceName string, operator *OperatorSigner, operatorContactID string, birthFiles map[string]string, logger *slog.Logger) (*BootstrapResult, error) {
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

	// The birth commit is signed by whoever seeds the root, which is the whole
	// point of the operator key: a seed set naming the operator while the agent
	// signs the birth produces a root admission refuses, so the key that will
	// be declared has to be the key that founds it.
	birthKey := filepath.Join(absCoreDir, SigningPrivateKeyFile)
	if operator != nil {
		birthKey = operator.PrivateKeyPath
	}
	signed, err := checkout.OpenSigned(ctx, checkout.SignedSpec{
		Name:            "core.identity",
		WorktreePath:    absCoreDir,
		SigningKeyPath:  birthKey,
		SkipBirthCommit: true,
		Logger:          logger.With("component", "core_identity_checkout"),
	})
	if err != nil {
		return nil, fmt.Errorf("open core identity checkout: %w", err)
	}

	trustSurface, err := coreTrustSurface(signing.Public, operator)
	if err != nil {
		return nil, err
	}
	policy, err := renderCoreConfig(instanceName, now, signing, channelCA, operator, operatorContactID)
	if err != nil {
		return nil, err
	}

	birth := map[string]string{
		".gitignore":         coreGitIgnore,
		".allowed_signers":   trustSurface,
		SigningPublicKeyFile: signing.Public,
		ChannelCACertFile:    string(channelCA.Certificate),
		CoreConfigFile:       string(policy),
	}
	for name, content := range birthFiles {
		// Identity files win: a caller cannot displace the trust surface or
		// the config by naming them, which would make the birth commit
		// describe an instance other than the one being created.
		if _, reserved := birth[name]; reserved {
			return nil, fmt.Errorf("birth file %q collides with core identity material", name)
		}
		birth[name] = content
	}
	if err := signed.Store.WriteFiles(ctx, birth, "bootstrap core identity"); err != nil {
		return nil, err
	}

	created = true
	// One fact, two projections: SelfSigned is derived from the same
	// principal callers read, never set independently of it.
	principal := operatorPrincipal(operator)
	return &BootstrapResult{
		Created:               true,
		CoreDir:               absCoreDir,
		SigningKeyFingerprint: signing.Fingerprint,
		ChannelCAFingerprint:  channelCA.Fingerprint,
		SelfSigned:            principal == "",
		OperatorPrincipal:     principal,
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
	Version     int                       `yaml:"version"`
	GeneratedAt string                    `yaml:"generated_at"`
	Listen      listenPolicy              `yaml:"listen"`
	Identity    identityPolicy            `yaml:"identity"`
	Person      personPolicy              `yaml:"person"`
	Trust       trustPolicy               `yaml:"trust"`
	Delegation  delegationPolicy          `yaml:"delegation"`
	Channels    channelPolicy             `yaml:"channels"`
	Roots       map[string]coreRootPolicy `yaml:"roots,omitempty"`
}

// listenPolicy is the native API's authentication block as thane init
// writes it: one freshly minted operator token, so a new workspace's API
// and console are closed from the first boot rather than open until
// someone remembers.
type listenPolicy struct {
	Auth listenAuthPolicy `yaml:"auth"`
}

type listenAuthPolicy struct {
	Tokens []apiTokenPolicy `yaml:"tokens"`
}

type apiTokenPolicy struct {
	Label string `yaml:"label"`
	Token string `yaml:"token"`
}

type identityPolicy struct {
	InstanceName      string         `yaml:"instance_name"`
	OperatorContactID string         `yaml:"operator_contact_id,omitempty"`
	SigningKey        signingKeyRef  `yaml:"signing_key"`
	ChannelCA         certificateRef `yaml:"channel_ca"`
}

type personPolicy struct {
	ContactBindings map[string]string `yaml:"contact_bindings"`
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

// coreRootPolicy carries one generated document-root declaration. Core emits
// only seed_signers; the dossier roots also emit their managed authoring and
// signed-history policy so a fresh instance has complete roots without
// post-init path surgery. Contacts additionally advertises exact subjects.
type coreRootPolicy struct {
	Authoring   string                `yaml:"authoring,omitempty"`
	Context     coreRootContextPolicy `yaml:"context,omitempty"`
	SeedSigners []coreSeedSigner      `yaml:"seed_signers,omitempty"`
	Git         coreRootGitPolicy     `yaml:"git,omitempty"`
}

type coreRootContextPolicy struct {
	Advertise string `yaml:"advertise,omitempty"`
}

type coreRootGitPolicy struct {
	Enabled          bool   `yaml:"enabled,omitempty"`
	SignCommits      bool   `yaml:"sign_commits,omitempty"`
	VerifySignatures string `yaml:"verify_signatures,omitempty"`
	SigningKey       string `yaml:"signing_key,omitempty"`
}

type coreSeedSigner struct {
	Principal string `yaml:"principal"`
	Key       string `yaml:"key"`
	Label     string `yaml:"label,omitempty"`
}

// coreSeedDeclaration names who may establish core.
//
// An anchored core declares the operator alone, which is what makes the agent
// unable to found or re-found the root holding its own config. A self-signed
// core declares the agent, because something has to be declared — a root with
// no seed set is governed by nothing, which is the state this exists to end.
func coreSeedDeclaration(signing *SigningKeyPair, operator *OperatorSigner) coreRootPolicy {
	if operator != nil {
		return coreRootPolicy{SeedSigners: []coreSeedSigner{{
			Principal: operator.Principal,
			Key:       strings.TrimSpace(operator.PublicKey),
			Label:     "operator",
		}}}
	}
	return coreRootPolicy{SeedSigners: []coreSeedSigner{{
		Principal: provenance.AgentPrincipal,
		Key:       strings.TrimSpace(signing.Public),
		Label:     "self-signed",
	}}}
}

func contactsRootDeclaration(signing *SigningKeyPair) coreRootPolicy {
	policy := managedDossierRootDeclaration(signing)
	policy.Context = coreRootContextPolicy{
		Advertise: "exact_subject",
	}
	return policy
}

func dossiersRootDeclaration(signing *SigningKeyPair) coreRootPolicy {
	return managedDossierRootDeclaration(signing)
}

func managedDossierRootDeclaration(signing *SigningKeyPair) coreRootPolicy {
	return coreRootPolicy{
		Authoring: "managed",
		SeedSigners: []coreSeedSigner{{
			Principal: provenance.AgentPrincipal,
			Key:       strings.TrimSpace(signing.Public),
			Label:     "agent",
		}},
		Git: coreRootGitPolicy{
			Enabled:          true,
			SignCommits:      true,
			VerifySignatures: "required",
			SigningKey:       "core:" + SigningPrivateKeyFile,
		},
	}
}

func renderCoreConfig(instanceName string, generatedAt time.Time, signing *SigningKeyPair, ca *CertificateAuthority, operator *OperatorSigner, operatorContactID string) ([]byte, error) {
	apiToken, err := GenerateAPIToken()
	if err != nil {
		return nil, err
	}
	cfg := coreConfig{
		Listen: listenPolicy{Auth: listenAuthPolicy{Tokens: []apiTokenPolicy{{Label: "operator", Token: apiToken}}}},
		Roots: map[string]coreRootPolicy{
			"core":     coreSeedDeclaration(signing, operator),
			"contacts": contactsRootDeclaration(signing),
			"dossiers": dossiersRootDeclaration(signing),
		},
		Version:     1,
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Identity: identityPolicy{
			InstanceName:      instanceName,
			OperatorContactID: strings.TrimSpace(operatorContactID),
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
		Person: personPolicy{ContactBindings: map[string]string{}},
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

// GenerateAPIToken mints an operator credential for the native API: 32
// random bytes, base64url without padding, so it is safe in headers,
// YAML, and shell without quoting.
func GenerateAPIToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
