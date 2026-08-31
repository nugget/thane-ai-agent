package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

type documentRootProvenanceWriter struct {
	checkout *checkout.Signed
	// root is the document root's configured name, used to suppress the core
	// HEAD trailer on core's own writes, where it would only restate the
	// parent commit git already records.
	root string
	// corePath is the core root's repository, read at write time to record
	// which identity snapshot was in force. Empty disables the trailer.
	corePath string
}

type documentRootProvenanceVerifier struct {
	verifier *provenance.Verifier
	prefix   string
}

func (w *documentRootProvenanceWriter) Write(ctx context.Context, filename, content, message string) error {
	store, err := w.store()
	if err != nil {
		return err
	}
	return store.Write(ctx, w.storeFilename(filename), content, w.withTurnProvenance(ctx, message))
}

func (w *documentRootProvenanceWriter) Delete(ctx context.Context, filename, message string) error {
	store, err := w.store()
	if err != nil {
		return err
	}
	return store.Delete(ctx, w.storeFilename(filename), w.withTurnProvenance(ctx, message))
}

func (w *documentRootProvenanceWriter) storeFilename(filename string) string {
	return w.checkout.RepoFilename(filename)
}

func (w *documentRootProvenanceWriter) store() (*provenance.Store, error) {
	if w == nil || w.checkout == nil || w.checkout.Store == nil {
		return nil, fmt.Errorf("document root signed checkout is not configured")
	}
	return w.checkout.Store, nil
}

func (v *documentRootProvenanceVerifier) Verify(ctx context.Context, filename string) (documents.SignatureVerification, error) {
	result, err := v.verifier.VerifyFile(ctx, v.storeFilename(filename))
	return documentSignatureVerificationFromProvenance(result), err
}

func (v *documentRootProvenanceVerifier) VerifyRoot(ctx context.Context) (documents.SignatureVerification, error) {
	result, err := v.verifier.VerifyTree(ctx, v.prefix)
	return documentSignatureVerificationFromProvenance(result), err
}

func (v *documentRootProvenanceVerifier) storeFilename(filename string) string {
	return checkout.RepoFilename(v.prefix, filename)
}

func documentSignatureVerificationFromProvenance(result provenance.VerificationResult) documents.SignatureVerification {
	// Three outcomes, not two. Collapsing "could not check" into
	// "failed" is what made a killed git subprocess read as a signature
	// problem, and the document layer words its refusal differently for
	// each — so the distinction has to survive this boundary.
	status := documents.SignatureFailed
	switch result.Status {
	case provenance.VerificationTrusted:
		status = documents.SignatureTrusted
	case provenance.VerificationUnavailable:
		status = documents.SignatureUnavailable
	}
	return documents.SignatureVerification{
		Status:  status,
		Commit:  result.Commit,
		Message: result.Message,
	}
}

func buildDocumentRoots(resolver *paths.Resolver) map[string]string {
	if resolver == nil {
		return nil
	}
	documentRoots := make(map[string]string)
	for _, root := range resolver.Prefixes() {
		rootEntry, ok := resolver.Root(root)
		if ok && rootEntry.Kind == paths.RootKindRepository {
			// Repository roots stay outside the document index: their assertion is
			// remote, branch, commit, and sync freshness—not document signatures.
			continue
		}
		rootPath, err := resolver.Resolve(root + ":")
		if err != nil {
			continue
		}
		info, err := os.Stat(rootPath)
		if err != nil || !info.IsDir() {
			continue
		}
		absPath, err := filepath.Abs(rootPath)
		if err != nil {
			continue
		}
		documentRoots[root] = absPath
	}
	if len(documentRoots) == 0 {
		return nil
	}
	return documentRoots
}

func (a *App) buildDocumentStoreOptions(documentRoots map[string]string, resolver *paths.Resolver) (documents.StoreOptions, error) {
	if a == nil || a.cfg == nil {
		return documents.StoreOptions{}, nil
	}
	opts := documents.StoreOptions{
		RootPolicies: make(map[string]documents.RootPolicy, len(a.cfg.DocRoots)),
	}
	if len(a.cfg.DocRoots) == 0 {
		return opts, nil
	}
	if documentRoots == nil {
		// The loop below mutates this map when bootstrapping a
		// missing directory; ensure it is non-nil so we don't
		// panic on assignment.
		documentRoots = make(map[string]string)
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	// Resolve injection eligibility once, here, so the assembler and the
	// document store report the same answer. Without this the store
	// would derive its own from an empty policy and tell the model
	// "inject: none" about the very root that injects under the legacy
	// fallback — a description that contradicts runtime behavior.
	legacyInject := legacyInjectRoot(a.cfg)

	for root, rootCfg := range a.cfg.DocRoots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		if root == "" {
			continue
		}
		policy := documentRootPolicyFromConfig(rootCfg)
		if root == legacyInject {
			policy.Context.Inject = documents.RootInjectTagged
		}

		rootPath, ok := documentRoots[root]
		if !ok {
			// A git-managed signing root is allowed to bootstrap
			// from a missing directory — we will mkdir, git init,
			// and birth-commit below. Non-bootstrap configs still
			// require the directory to already exist.
			if !policy.Git.Enabled || !policy.Git.SignCommits {
				return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s references a document root that is not configured in paths or does not exist on disk", root)
			}
			created, err := bootstrapMissingRootDirectory(root, resolver, logger)
			if err != nil {
				return documents.StoreOptions{}, err
			}
			documentRoots[root] = created
			rootPath = created
		}

		opts.RootPolicies[root] = policy
		if root == contacts.DossierRootName {
			if opts.RootValidators == nil {
				opts.RootValidators = make(map[string]documents.RootWriteValidator)
			}
			opts.RootValidators[root] = contacts.NewDossierWriteValidator(a.resolveDossierContactName)
		}

		// Construct the writer first when this root signs commits.
		// provenance.NewWithOptions runs ensureRepo (mkdir + git
		// init + .allowed_signers); BootstrapBirthCommit then makes
		// HEAD exist if the repo was empty. Doing this before the
		// verifier means the verifier always sees a fully prepared
		// repo and never silently no-ops because the repo wasn't
		// ready yet.
		var writer *documentRootProvenanceWriter
		if policy.Git.Enabled && policy.Git.SignCommits {
			w, err := a.newDocumentRootProvenanceWriter(root, rootPath, rootCfg, resolver)
			if err != nil {
				return documents.StoreOptions{}, err
			}
			writer = w
		}

		// Admission asks whether this root's history was ever entitled to
		// exist. That is prior to anything either the writer or the verifier
		// does with it, and it does not depend on whether this instance
		// writes to the root — a corpus Thane only reads, pulled from a
		// remote, carries entirely foreign history and is precisely where an
		// unattributable birth matters most. So it runs here, once per root,
		// rather than inside whichever constructor happens to be built.
		//
		// It follows the writer because a root Thane creates has no history
		// to judge until BootstrapBirthCommit has made one.
		if err := a.verifyRootAdmission(root, rootPath, rootCfg, policy.Git.VerifySignatures, resolver, logger); err != nil {
			return documents.StoreOptions{}, err
		}

		var verifier *documentRootProvenanceVerifier
		if policy.Git.Enabled && policy.Git.VerifySignatures != documents.VerificationNone {
			v, err := a.newDocumentRootProvenanceVerifier(root, rootPath, rootCfg, resolver)
			if err != nil {
				if policy.Git.VerifySignatures == documents.VerificationRequired {
					return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s verify_signatures=required but verifier unavailable: %w", root, err)
				}
				logger.Warn("document root signature verifier unavailable",
					"root", root,
					"mode", policy.Git.VerifySignatures,
					"error", err,
				)
			} else {
				verifier = v
				if opts.RootVerifiers == nil {
					opts.RootVerifiers = make(map[string]documents.RootVerifier)
				}
				opts.RootVerifiers[root] = verifier
			}
		}

		if writer != nil {
			if opts.RootWriters == nil {
				opts.RootWriters = make(map[string]documents.RootWriter)
			}
			opts.RootWriters[root] = writer
		}

		// Expose revision history for any git-backed root: prefer the signing
		// store, otherwise the verify-only verifier. Both satisfy
		// provenance.Reader, so a required verify-only root is inspectable too.
		var reviser *documentRootProvenanceReviser
		switch {
		case writer != nil:
			reviser = &documentRootProvenanceReviser{reader: writer.checkout.Reader(), prefix: writer.checkout.Prefix}
		case verifier != nil:
			reviser = &documentRootProvenanceReviser{reader: verifier.verifier, prefix: verifier.prefix}
		}
		if reviser != nil {
			if opts.RootRevisers == nil {
				opts.RootRevisers = make(map[string]documents.RootReviser)
			}
			opts.RootRevisers[root] = reviser
		}

		// A remote-backed root gets a syncer driving the fast-forward-only
		// engine on the writer's store — the same store the writer and reviser
		// use, so its lock serializes sync against local writes. Sync needs the
		// signing store, so a remote requires sign_commits.
		if rootCfg.Git.Remote != nil {
			if writer == nil {
				return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s.git.remote requires sign_commits (the sync engine needs the signing store)", root)
			}
			if a.syncRegistry == nil {
				a.syncRegistry = checkout.NewSyncStateRegistry()
			}
			resolve := func(p string) string { return resolvePath(p, resolver) }
			syncer, err := buildDocRootSyncer(root, rootCfg.Git, writer.checkout, a.syncRegistry, resolve, logger)
			if err != nil {
				return documents.StoreOptions{}, fmt.Errorf("doc_roots.%s.git.remote: %w", root, err)
			}
			syncer.notifyTransition = a.docRootSyncAttentionNotifier()
			a.docRootSyncers = append(a.docRootSyncers, syncer)
		}
	}
	return opts, nil
}

// resolveDossierContactName deliberately reads a.contactStore at validation
// time. Document roots are assembled before initChannels opens the contact
// store, so capturing the field's startup value would permanently capture nil.
func (a *App) resolveDossierContactName(id uuid.UUID) (string, error) {
	if a == nil || a.contactStore == nil {
		return "", fmt.Errorf("contact directory is not configured")
	}
	contact, err := a.contactStore.Get(id)
	if err != nil {
		return "", fmt.Errorf("load contact: %w", err)
	}
	return contact.FormattedName, nil
}

// bootstrapMissingRootDirectory creates the directory for a git-managed
// document root that was declared in doc_roots: but has no entry in
// paths or does not exist on disk. Returns the absolute path. Only
// callers that are about to construct a signing writer should use this
// — for non-bootstrap roots the existing "does not exist on disk" error
// is preserved.
func bootstrapMissingRootDirectory(root string, resolver *paths.Resolver, logger *slog.Logger) (string, error) {
	if resolver == nil {
		return "", fmt.Errorf("doc_roots.%s has no path configured (paths: missing entry for %q)", root, root)
	}
	resolved, err := resolver.Resolve(root + ":")
	if err != nil {
		return "", fmt.Errorf("doc_roots.%s: %w", root, err)
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return "", fmt.Errorf("doc_roots.%s create directory: %w", root, err)
	}
	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("doc_roots.%s resolve absolute path: %w", root, err)
	}
	logger.Info("bootstrapping new document root", "root", root, "path", absPath)
	return absPath, nil
}

// legacyInjectRoot returns the root that injects by historical default
// when no root declares a context policy, or empty once any policy is
// declared. Before roots could declare eligibility, tagged articles in
// the kb root were scanned into every prompt; that behavior is preserved
// until an operator states an intent, and naming it here keeps the
// preserved behavior visible to the model rather than implicit in the
// assembler's wiring.
func legacyInjectRoot(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, policy := range cfg.DocRoots {
		if policy.Context.Declared() {
			return ""
		}
	}
	return "kb"
}

func (a *App) newDocumentRootProvenanceWriter(root, rootPath string, rootCfg config.DocumentRootConfig, resolver *paths.Resolver) (*documentRootProvenanceWriter, error) {
	gitCfg := rootCfg.Git
	signingKey := strings.TrimSpace(gitCfg.SigningKey)
	if signingKey == "" {
		return nil, fmt.Errorf("doc_roots.%s.git.signing_key is required for signed document root commits", root)
	}
	signingKey = resolvePath(signingKey, resolver)

	absRepoPath, absRootPath, err := resolveRootPaths(root, rootPath, gitCfg, resolver)
	if err != nil {
		return nil, err
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	signed, err := checkout.OpenSigned(context.Background(), checkout.SignedSpec{
		Name:           "doc_roots." + root + ".git",
		WorktreePath:   absRootPath,
		RepoPath:       absRepoPath,
		SigningKeyPath: signingKey,
		SeedSigners:    buildTrustedSigners(rootCfg.SeedSigners, gitCfg.AllowedSigners),
		Logger:         logger.With("component", "document_root_provenance", "root", root),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize git provenance for document root %s: %w", root, err)
	}

	// Boot-time round-trip: confirm HEAD actually verifies against the trust
	// file we just rendered, so a malformed signer line or an OpenSSH version
	// that can't parse a rendered option fails loudly now instead of silently
	// blocking reads later. Only worth running where verification is actually
	// consumed; the policy mapping (fail vs. warn) lives in applyBootVerification.
	mode := documents.VerificationMode(strings.TrimSpace(gitCfg.VerifySignatures))
	switch mode {
	case documents.VerificationRequired, documents.VerificationWarn:
		if err := applyBootVerification(mode, root, "allowed_signers", signed.VerifyHead(context.Background()), logger); err != nil {
			return nil, err
		}
	}

	logger.Info("document root provenance enabled",
		"root", root,
		"repo", signed.Store.Path(),
		"prefix", signed.Prefix,
	)
	return &documentRootProvenanceWriter{
		checkout: signed,
		root:     root,
		corePath: a.cfg.CoreRoot(),
	}, nil
}

func (a *App) newDocumentRootProvenanceVerifier(root, rootPath string, rootCfg config.DocumentRootConfig, resolver *paths.Resolver) (*documentRootProvenanceVerifier, error) {
	gitCfg := rootCfg.Git
	absRepoPath, absRootPath, err := resolveRootPaths(root, rootPath, gitCfg, resolver)
	if err != nil {
		return nil, err
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	verified, err := checkout.OpenVerified(context.Background(), checkout.VerifySpec{
		Name:         "doc_roots." + root + ".git",
		WorktreePath: absRootPath,
		RepoPath:     absRepoPath,
		SeedSigners:  buildTrustedSigners(rootCfg.SeedSigners, gitCfg.AllowedSigners),
		Logger:       logger.With("component", "document_root_verifier", "root", root),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize git verifier for document root %s: %w", root, err)
	}
	return &documentRootProvenanceVerifier{verifier: verified.Verifier, prefix: verified.Prefix}, nil
}

// applyBootVerification maps one boot-time check's result onto the root's
// verification policy: a required root fails to construct, a warn root logs and
// continues, and any other mode is a no-op. A nil verifyErr is always a no-op,
// so callers can pass a check's result directly.
//
// check names the requirement that failed ("admission", "allowed_signers"), so
// an operator reading the refusal knows which question came back wrong — the
// two ask different things and are repaired differently.
func applyBootVerification(mode documents.VerificationMode, root, check string, verifyErr error, logger *slog.Logger) error {
	if verifyErr == nil {
		return nil
	}
	switch mode {
	case documents.VerificationRequired:
		return fmt.Errorf("doc_roots.%s %s boot verification: %w", root, check, verifyErr)
	case documents.VerificationWarn:
		logger.Warn("document root boot verification failed",
			"root", root, "check", check, "error", verifyErr)
		return nil
	default:
		return nil
	}
}

// buildTrustedSigners flattens the shared and per-root operator allowed-signer
// config into provenance.TrustedSigner values for rendering. Order does not
// matter — the renderer canonicalizes, deduplicates, and sorts — so the two
// lists are simply concatenated (shared first).
func buildTrustedSigners(shared, perRoot []config.AllowedSigner) []provenance.TrustedSigner {
	out := make([]provenance.TrustedSigner, 0, len(shared)+len(perRoot))
	for _, list := range [][]config.AllowedSigner{shared, perRoot} {
		for _, s := range list {
			out = append(out, provenance.TrustedSigner{
				Principal:   s.Principal,
				PublicKey:   s.Key,
				Comment:     s.Label,
				ValidAfter:  s.ValidAfter,
				ValidBefore: s.ValidBefore,
			})
		}
	}
	return out
}

func sortedDocumentRootNames(documentRoots map[string]string) []string {
	roots := make([]string, 0, len(documentRoots))
	for root := range documentRoots {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func documentRootPolicyAttrs(opts documents.StoreOptions, roots []string) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(roots))
	for _, root := range roots {
		policy, ok := opts.RootPolicies[root]
		if !ok {
			continue
		}
		attrs = append(attrs, slog.Group(root,
			slog.Bool("indexing", policy.Indexing),
			slog.String("authoring", string(policy.Authoring)),
			slog.Bool("git", policy.Git.Enabled),
			slog.Bool("sign_commits", policy.Git.SignCommits),
			slog.String("verify_signatures", string(policy.Git.VerifySignatures)),
		))
	}
	return attrs
}
