package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// resolveRootPaths resolves a document root's worktree and its backing
// repository to absolute paths, and confirms the repository actually contains
// the worktree.
//
// It lives beside admission because admission made it the third caller. The
// writer, the verifier, and admission must all name the same repository, and
// three copies of this were three chances for one of them to judge a different
// one than the others.
func resolveRootPaths(root, rootPath string, gitCfg config.DocumentRootGitConfig, resolver *paths.Resolver) (repo, worktree string, err error) {
	repoPath := strings.TrimSpace(gitCfg.RepoPath)
	if repoPath == "" {
		repoPath = rootPath
	} else {
		repoPath = resolvePath(repoPath, resolver)
	}
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve doc_roots.%s.git.repo_path: %w", root, err)
	}
	absRootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve document root %s path: %w", root, err)
	}
	if _, err := checkout.ResolveRoot(absRepoPath, absRootPath); err != nil {
		return "", "", fmt.Errorf("doc_roots.%s.git.repo_path: %w", root, err)
	}
	return absRepoPath, absRootPath, nil
}

// documentRootPaths returns the path prefixes for this instance's document
// roots, with the reserved derived roots — core and self — forced to the
// locations derived from workspace.path.
//
// The derived roots are what make this worth centralizing. They carry
// policy under roots.core / roots.self but never a path, so they are
// simply absent from cfg.Paths as loaded and appear only once derived.
// Any caller that enumerates roots without this step silently omits them
// — which for a per-root check means reporting on every root except the
// one holding the config that decides what the instance trusts. Boot
// derives them before building its resolver; anything that wants boot's
// answer has to derive them the same way, which is why this is a
// function rather than a step each caller remembers.
//
// The returned map is a copy. Creating the directories is left to the
// caller, so read-only callers stay read-only.
func documentRootPaths(cfg *config.Config, logger *slog.Logger) map[string]string {
	out := make(map[string]string, len(cfg.Paths)+2)
	for name, path := range cfg.Paths {
		out[name] = path
	}
	if cfg.Workspace.Path == "" {
		return out
	}
	// Both derived roots get the same treatment, so adding one is an
	// entry here rather than a second copy of this loop. A slice, not a
	// map: iteration order decides log order, and a diagnostic that
	// shuffles between runs reads as two different problems.
	for _, derivedRoot := range []struct {
		name string
		path string
	}{
		{config.CoreRootName, cfg.CoreRoot()},
		{config.SelfRootName, cfg.SelfRoot()},
	} {
		rootName, derived := derivedRoot.name, derivedRoot.path
		if derived == "" {
			continue
		}
		for name, path := range out {
			if strings.TrimSuffix(name, ":") != rootName {
				continue
			}
			if strings.TrimSpace(path) != derived && logger != nil {
				logger.Info("ignoring configured root path; this root is derived from workspace.path",
					"root", rootName,
					"configured_key", name,
					"configured_path", path,
					"derived_path", derived,
				)
			}
			delete(out, name)
		}
		out[rootName] = derived
	}
	return out
}

// RootAdmission is one root's admission outcome.
type RootAdmission struct {
	// Root is the configured root name, without its trailing colon.
	Root string
	// RepoPath is the repository admission judged, empty when it never ran.
	RepoPath string
	// Mode is the root's verify_signatures policy, which decides whether a
	// failure refuses the instance or only logs.
	Mode documents.VerificationMode
	// Applicable is false for roots admission does not govern: those without
	// git, without signature verification, or declaring no seed signers.
	Applicable bool
	// Err is the admission failure, nil when the root is admitted.
	Err error
}

// Admitted reports whether this root passed, treating roots admission does
// not govern as passing.
func (r RootAdmission) Admitted() bool { return r.Err == nil }

// Fatal reports whether this outcome is one `thane serve` refuses to start
// over — a failure under a root that requires verification.
func (r RootAdmission) Fatal() bool {
	return r.Err != nil && r.Mode == documents.VerificationRequired
}

// admissionSeeds returns the seed set a root must be admitted against and
// whether admission governs it at all.
//
// This predicate has exactly one definition on purpose. It decides both what
// boot refuses over and what `thane validate` reports, and those two answering
// differently is the specific way this feature would rot: an operator would
// see a clean report and then watch the instance refuse to start, or worse,
// see a clean report because the reporting path quietly considered fewer roots
// than the gate does.
func admissionSeeds(rootCfg config.DocumentRootConfig, mode documents.VerificationMode) ([]provenance.TrustedSigner, bool) {
	if !rootCfg.Git.Enabled {
		return nil, false
	}
	switch mode {
	case documents.VerificationRequired, documents.VerificationWarn:
	default:
		return nil, false
	}
	seeds := buildTrustedSigners(rootCfg.SeedSigners, rootCfg.Git.AllowedSigners)
	if len(seeds) == 0 {
		return nil, false
	}
	return seeds, true
}

// checkRootAdmission is the single evaluation of one root's admission. Boot
// calls it and maps the result onto policy; `thane validate` calls it and
// prints the result. Neither re-derives the question.
func checkRootAdmission(ctx context.Context, root, rootPath string, rootCfg config.DocumentRootConfig, mode documents.VerificationMode, resolver *paths.Resolver) RootAdmission {
	out := RootAdmission{Root: root, Mode: mode}
	seeds, applicable := admissionSeeds(rootCfg, mode)
	if !applicable {
		return out
	}
	out.Applicable = true

	repoPath, _, err := resolveRootPaths(root, rootPath, rootCfg.Git, resolver)
	if err != nil {
		out.Err = err
		return out
	}
	out.RepoPath = repoPath
	_, out.Err = provenance.VerifyAdmission(ctx, repoPath, seeds)
	return out
}

// verifyRootAdmission checks that a git-backed root's history is admitted by
// the seed signers config declares for it, then maps the outcome onto the
// root's own verification policy — the same mapping the other boot checks use,
// so a root has one answer to "how strict is this".
//
// A root declaring no seed signers is neither admitted nor refused: there is
// nothing to check against. Config already refuses the combination where that
// silence would matter, a root that signs commits while naming no one entitled
// to establish it.
func (a *App) verifyRootAdmission(root, rootPath string, rootCfg config.DocumentRootConfig, mode documents.VerificationMode, resolver *paths.Resolver, logger *slog.Logger) error {
	result := checkRootAdmission(context.Background(), root, rootPath, rootCfg, mode, resolver)
	return applyBootVerification(mode, root, "admission", result.Err, logger)
}

// CheckRootAdmission evaluates admission for every configured document root,
// for `thane validate` to report before an operator deploys.
//
// It reaches boot's answer by walking boot's path: the same root enumeration
// (buildDocumentRoots), the same policy derivation
// (documentRootPolicyFromConfig), the same predicate (admissionSeeds), and the
// same check (checkRootAdmission). The one deliberate difference is that
// validate never creates anything, so a signing root whose directory does not
// exist yet is absent from the enumeration rather than bootstrapped — serve
// would create and birth-commit it, so reporting it as unadmitted would be a
// false alarm about a root that does not exist to judge.
func CheckRootAdmission(ctx context.Context, cfg *config.Config) []RootAdmission {
	if cfg == nil || len(cfg.DocRoots) == 0 {
		return nil
	}
	resolver := paths.New(documentRootPaths(cfg, nil))
	documentRoots := buildDocumentRoots(resolver)

	out := make([]RootAdmission, 0, len(cfg.DocRoots))
	for root, rootCfg := range cfg.DocRoots {
		root = strings.TrimSuffix(strings.TrimSpace(root), ":")
		if root == "" {
			continue
		}
		mode := documentRootPolicyFromConfig(rootCfg).Git.VerifySignatures
		if _, applicable := admissionSeeds(rootCfg, mode); !applicable {
			continue
		}
		rootPath, ok := documentRoots[root]
		if !ok {
			continue
		}
		out = append(out, checkRootAdmission(ctx, root, rootPath, rootCfg, mode, resolver))
	}
	return out
}

// CoreSeedSigners returns the seed set declared for the core root.
//
// Core integrity is checked before — and sometimes instead of — building
// document roots, so it cannot reach the per-root wiring that hands every
// other root its seeds. Without this the seed floor would hold for every root
// except the one holding the config, which is the root an operator most needs
// to be able to repair.
func CoreSeedSigners(cfg *config.Config) []provenance.TrustedSigner {
	if cfg == nil {
		return nil
	}
	for name, rootCfg := range cfg.DocRoots {
		if strings.TrimSuffix(strings.TrimSpace(name), ":") != "core" {
			continue
		}
		return buildTrustedSigners(rootCfg.SeedSigners, rootCfg.Git.AllowedSigners)
	}
	return nil
}
