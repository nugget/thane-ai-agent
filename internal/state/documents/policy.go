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
	Indexing  bool              `json:"indexing"`
	Authoring AuthoringMode     `json:"authoring"`
	Git       RootGitPolicy     `json:"git,omitempty"`
	Context   RootContextPolicy `json:"context,omitempty"`
}

// Root context policy values, mirroring the config surface. Injection
// eligibility is per root because a corpus is the only place the answer
// can be given for documents Thane does not own and cannot annotate.
const (
	RootInjectNone      = "none"
	RootInjectTagged    = "tagged"
	RootSearchDefault   = "default"
	RootSearchOnRequest = "on_request"
	RootSearchNever     = "never"
	RootUntaggedIgnore  = "ignore"
	RootUntaggedRefuse  = "refuse"
)

// RootContextPolicy describes how a root's documents may reach a model:
// whether they can be injected into a prompt, and how they surface in
// search.
type RootContextPolicy struct {
	Inject      string `json:"inject,omitempty"`
	Search      string `json:"search,omitempty"`
	RequiresTag string `json:"requires_tag,omitempty"`
	Untagged    string `json:"untagged,omitempty"`
}

// EffectiveInject resolves injection eligibility, defaulting to none.
func (p RootContextPolicy) EffectiveInject() string {
	if p.Inject == "" {
		return RootInjectNone
	}
	return p.Inject
}

// EffectiveUntagged resolves what a tagless document means in this root,
// defaulting to skipping it — which is what every injecting root has
// always done.
func (p RootContextPolicy) EffectiveUntagged() string {
	if p.Untagged == "" {
		return RootUntaggedIgnore
	}
	return p.Untagged
}

// EffectiveSearch resolves search visibility, defaulting to full.
func (p RootContextPolicy) EffectiveSearch() string {
	if p.Search == "" {
		return RootSearchDefault
	}
	return p.Search
}

// RootGitPolicy describes git-backed provenance policy for a managed
// document root.
type RootGitPolicy struct {
	Enabled          bool             `json:"enabled"`
	SignCommits      bool             `json:"sign_commits,omitempty"`
	VerifySignatures VerificationMode `json:"verify_signatures,omitempty"`
	RepoPath         string           `json:"-"`
}

// RootPolicySummary is the model-facing form of [RootPolicy]. It omits
// local filesystem paths and key material.
type RootPolicySummary struct {
	Indexing  bool                 `json:"indexing"`
	Authoring AuthoringMode        `json:"authoring"`
	Git       RootGitPolicySummary `json:"git"`
	// Context tells the model how this corpus reaches it: whether
	// documents here can appear in a prompt unbidden, and whether an
	// unscoped search will look here. Always emitted so the model can
	// tell "no results" from "not searched by default".
	Context RootContextSummary `json:"context"`
}

// RootContextSummary is the model-facing form of [RootContextPolicy].
type RootContextSummary struct {
	Inject      string `json:"inject"`
	Search      string `json:"search"`
	RequiresTag string `json:"requires_tag,omitempty"`
	// Untagged says what a document carrying no tags means here. It is
	// emitted always rather than only when refusing, because it changes
	// what counts as a valid write: authoring a tagless document into a
	// refusing root produces an instance that declines to start. A rule
	// the model is held to but cannot see is one it will break.
	Untagged string `json:"untagged"`
}

// RootGitPolicySummary is the model-facing form of [RootGitPolicy].
type RootGitPolicySummary struct {
	Enabled          bool             `json:"enabled"`
	SignCommits      bool             `json:"sign_commits,omitempty"`
	VerifySignatures VerificationMode `json:"verify_signatures,omitempty"`
	// Revisions reports whether this root exposes revision history
	// (doc_history / doc_diff / doc_at). Always emitted so the model can
	// rely on it to decide whether to reach for those tools.
	Revisions bool `json:"revisions"`
}

// SignatureStatus describes the last known signature verification state
// for a root or document.
type SignatureStatus string

const (
	// SignatureTrusted means the checked content is clean and covered by
	// trusted signed git history.
	SignatureTrusted SignatureStatus = "trusted"
	// SignatureFailed means verification ran and returned a verdict the
	// policy rejects: an absent or untrusted signature, a dirty
	// worktree, content not covered by signed history. The check
	// completed; the content did not pass it.
	SignatureFailed SignatureStatus = "failed"
	// SignatureUnavailable means verification could not reach a verdict.
	// That covers both configuration (verification is required but no
	// verifier could be built) and execution (git was killed, timed
	// out, or the caller's context expired mid-check).
	//
	// It is deliberately distinct from [SignatureFailed]. Both refuse
	// the content — failing closed is the point of a trust boundary —
	// but they mean opposite things about the content itself, and a
	// reader told "failed" for a killed subprocess goes hunting a trust
	// problem that may not exist.
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
	// WriteIfRevision compares, commits, and returns the exact new revision
	// atomically. expectedRevision may be the reserved creation token absent.
	WriteIfRevision(ctx context.Context, filename, content, message, expectedRevision string) (revision string, err error)
	Delete(ctx context.Context, filename, message string) error
}

// RootRevisionConflictError reports a failed conditional root write without
// coupling the documents package to the backing revision system. Expected and
// Actual are internal coordination tokens; model-facing tools translate this
// into a bounded narrative conflict instead of exposing them.
type RootRevisionConflictError struct {
	// Expected is the hidden revision receipt used by the attempted write.
	Expected string `json:"-"`
	// Actual is the current revision or a named worktree state.
	Actual string `json:"-"`
}

// Error implements error.
func (e *RootRevisionConflictError) Error() string {
	return "document changed since the caller's read"
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
	RootRevisers  map[string]RootReviser
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

func normalizeRootRevisers(roots map[string]string, revisers map[string]RootReviser) map[string]RootReviser {
	if len(revisers) == 0 {
		return nil
	}
	out := make(map[string]RootReviser, len(revisers))
	for root, reviser := range revisers {
		root = normalizeRootName(root)
		if root == "" || reviser == nil {
			continue
		}
		if _, ok := roots[root]; !ok {
			continue
		}
		out[root] = reviser
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
	root = normalizeRootName(root)
	policy := s.rootPolicy(root)
	return RootPolicySummary{
		Indexing:  policy.Indexing,
		Authoring: policy.Authoring,
		Git: RootGitPolicySummary{
			Enabled:          policy.Git.Enabled,
			SignCommits:      policy.Git.SignCommits,
			VerifySignatures: policy.Git.VerifySignatures,
			Revisions:        s.rootReviser(root) != nil,
		},
		Context: RootContextSummary{
			Inject:      policy.Context.EffectiveInject(),
			Search:      policy.Context.EffectiveSearch(),
			RequiresTag: policy.Context.RequiresTag,
			Untagged:    policy.Context.EffectiveUntagged(),
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

func (s *Store) rootReviser(root string) RootReviser {
	root = normalizeRootName(root)
	if s == nil || len(s.rootRevisers) == 0 {
		return nil
	}
	return s.rootRevisers[root]
}
