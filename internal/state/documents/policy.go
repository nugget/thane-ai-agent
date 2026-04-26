package documents

import (
	"context"
	"strings"
)

// AuthoringMode describes whether managed document mutation APIs may
// write to a root.
type AuthoringMode string

const (
	// AuthoringManaged allows managed document mutation APIs to write
	// to the root.
	AuthoringManaged AuthoringMode = "managed"
	// AuthoringReadOnly prevents managed document mutation APIs from
	// writing to the root.
	AuthoringReadOnly AuthoringMode = "read_only"
	// AuthoringRestricted reserves the root for narrower policy-aware
	// authoring flows and blocks generic document mutations.
	AuthoringRestricted AuthoringMode = "restricted"
)

// VerificationMode describes the desired signature verification policy
// for consumers of a managed document root.
type VerificationMode string

const (
	// VerificationNone disables signature verification enforcement.
	VerificationNone VerificationMode = "none"
	// VerificationWarn records verification failures without blocking
	// consumers.
	VerificationWarn VerificationMode = "warn"
	// VerificationRequired marks the root as requiring trusted signed
	// history before high-integrity consumers should load or activate
	// content from it.
	VerificationRequired VerificationMode = "required"
)

// RootPolicy describes indexing, authoring, and integrity policy for a
// managed document root.
type RootPolicy struct {
	Indexing  bool          `json:"indexing"`
	Authoring AuthoringMode `json:"authoring"`
	Git       RootGitPolicy `json:"git,omitempty"`
}

// RootGitPolicy describes git-backed provenance policy for a managed
// document root.
type RootGitPolicy struct {
	Enabled          bool             `json:"enabled"`
	SignCommits      bool             `json:"sign_commits,omitempty"`
	VerifySignatures VerificationMode `json:"verify_signatures,omitempty"`
	RepoPath         string           `json:"-"`
	AllowedSigners   string           `json:"-"`
}

// RootPolicySummary is the model-facing form of [RootPolicy]. It omits
// local filesystem paths and key material.
type RootPolicySummary struct {
	Indexing  bool                 `json:"indexing"`
	Authoring AuthoringMode        `json:"authoring"`
	Git       RootGitPolicySummary `json:"git"`
}

// RootGitPolicySummary is the model-facing form of [RootGitPolicy].
type RootGitPolicySummary struct {
	Enabled          bool             `json:"enabled"`
	SignCommits      bool             `json:"sign_commits,omitempty"`
	VerifySignatures VerificationMode `json:"verify_signatures,omitempty"`
}

// SignatureStatus describes the last known signature verification state
// for a root or document.
type SignatureStatus string

const (
	// SignatureTrusted means the checked content is clean and covered by
	// trusted signed git history.
	SignatureTrusted SignatureStatus = "trusted"
	// SignatureFailed means signature policy could not verify the checked
	// content.
	SignatureFailed SignatureStatus = "failed"
	// SignatureUnavailable means signature verification was requested but
	// no verifier could be configured.
	SignatureUnavailable SignatureStatus = "unavailable"
)

// SignatureVerification is the document package's verifier-neutral
// signature status shape.
type SignatureVerification struct {
	Status    SignatureStatus  `json:"status"`
	Mode      VerificationMode `json:"mode,omitempty"`
	Commit    string           `json:"commit,omitempty"`
	Message   string           `json:"message,omitempty"`
	CheckedAt string           `json:"checked_at,omitempty"`
	Consumer  string           `json:"consumer,omitempty"`
}

// RootWriter applies a managed document mutation to a root. Git-backed
// roots use this hook to sign and commit writes without exposing git to
// the model.
type RootWriter interface {
	Write(ctx context.Context, filename, content, message string) error
	Delete(ctx context.Context, filename, message string) error
}

// RootVerifier verifies that a git-backed root or file is clean and
// trusted before policy-sensitive consumers load it.
type RootVerifier interface {
	Verify(ctx context.Context, filename string) (SignatureVerification, error)
	VerifyRoot(ctx context.Context) (SignatureVerification, error)
}

// StoreOptions configures optional root policy and backing writers for
// [Store].
type StoreOptions struct {
	RootPolicies  map[string]RootPolicy
	RootWriters   map[string]RootWriter
	RootVerifiers map[string]RootVerifier
}

func defaultRootPolicy() RootPolicy {
	return RootPolicy{
		Indexing:  true,
		Authoring: AuthoringManaged,
		Git: RootGitPolicy{
			VerifySignatures: VerificationNone,
		},
	}
}

func normalizePolicies(roots map[string]string, policies map[string]RootPolicy) map[string]RootPolicy {
	out := make(map[string]RootPolicy, len(roots))
	for root := range roots {
		out[root] = defaultRootPolicy()
	}
	for root, policy := range policies {
		root = normalizeRootName(root)
		if root == "" {
			continue
		}
		if _, ok := roots[root]; !ok {
			continue
		}
		out[root] = normalizeRootPolicy(policy)
	}
	return out
}

func normalizeRootPolicy(policy RootPolicy) RootPolicy {
	if policy.Authoring == "" {
		policy.Authoring = AuthoringManaged
	}
	if policy.Git.VerifySignatures == "" {
		policy.Git.VerifySignatures = VerificationNone
	}
	return policy
}

func normalizeRootWriters(roots map[string]string, writers map[string]RootWriter) map[string]RootWriter {
	if len(writers) == 0 {
		return nil
	}
	out := make(map[string]RootWriter, len(writers))
	for root, writer := range writers {
		root = normalizeRootName(root)
		if root == "" || writer == nil {
			continue
		}
		if _, ok := roots[root]; !ok {
			continue
		}
		out[root] = writer
	}
	return out
}

func normalizeRootVerifiers(roots map[string]string, verifiers map[string]RootVerifier) map[string]RootVerifier {
	if len(verifiers) == 0 {
		return nil
	}
	out := make(map[string]RootVerifier, len(verifiers))
	for root, verifier := range verifiers {
		root = normalizeRootName(root)
		if root == "" || verifier == nil {
			continue
		}
		if _, ok := roots[root]; !ok {
			continue
		}
		out[root] = verifier
	}
	return out
}

func normalizeRootName(root string) string {
	return strings.TrimSuffix(strings.TrimSpace(root), ":")
}

func (s *Store) rootPolicy(root string) RootPolicy {
	root = normalizeRootName(root)
	if s == nil {
		return defaultRootPolicy()
	}
	if policy, ok := s.rootPolicies[root]; ok {
		return policy
	}
	return defaultRootPolicy()
}

func (s *Store) rootPolicySummary(root string) RootPolicySummary {
	policy := s.rootPolicy(root)
	return RootPolicySummary{
		Indexing:  policy.Indexing,
		Authoring: policy.Authoring,
		Git: RootGitPolicySummary{
			Enabled:          policy.Git.Enabled,
			SignCommits:      policy.Git.SignCommits,
			VerifySignatures: policy.Git.VerifySignatures,
		},
	}
}

func (s *Store) rootWriter(root string) RootWriter {
	root = normalizeRootName(root)
	if s == nil || len(s.rootWriters) == 0 {
		return nil
	}
	return s.rootWriters[root]
}

func (s *Store) rootVerifier(root string) RootVerifier {
	root = normalizeRootName(root)
	if s == nil || len(s.rootVerifiers) == 0 {
		return nil
	}
	return s.rootVerifiers[root]
}
