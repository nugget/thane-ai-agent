package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

func bootstrapContactDossierRoot(ctx context.Context, configPath, workspace string, logger *slog.Logger) (bool, bool, error) {
	return bootstrapDossierRoot(ctx, configPath, workspace, platformconfig.ContactsRootName, logger)
}

func bootstrapSubjectDossierRoot(ctx context.Context, configPath, workspace string, logger *slog.Logger) (bool, bool, error) {
	return bootstrapDossierRoot(ctx, configPath, workspace, platformconfig.DossiersRootName, logger)
}

func bootstrapDossierRoot(ctx context.Context, configPath, workspace, rootName string, logger *slog.Logger) (bool, bool, error) {
	cfg, err := platformconfig.LoadWithWorkspace(configPath, workspace)
	if err != nil {
		return false, false, fmt.Errorf("load core config for %s root: %w", rootName, err)
	}
	entry, found := cfg.DocRoots[rootName]
	if !found {
		return false, false, nil
	}
	if !entry.Git.Enabled || !entry.Git.SignCommits {
		return true, false, fmt.Errorf("roots.%s must enable signed commits so thane init can establish its dossier history", rootName)
	}
	if len(entry.SeedSigners) == 0 {
		return true, false, fmt.Errorf("roots.%s must declare seed_signers before thane init can establish it", rootName)
	}
	rootPath, err := dossierRootPath(cfg, rootName)
	if err != nil {
		return true, false, err
	}

	rootPaths := make(map[string]string, len(cfg.Paths)+4)
	for name, path := range cfg.Paths {
		rootPaths[name] = path
	}
	rootPaths[platformconfig.CoreRootName] = cfg.CoreRoot()
	rootPaths[platformconfig.SelfRootName] = cfg.SelfRoot()
	rootPaths[platformconfig.ContactsRootName] = cfg.ContactsRoot()
	rootPaths[platformconfig.DossiersRootName] = cfg.DossiersRoot()
	resolver := paths.New(rootPaths)

	signingKey, err := resolver.Resolve(entry.Git.SigningKey)
	if err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.signing_key: %w", rootName, err)
	}
	repoPath := strings.TrimSpace(entry.Git.RepoPath)
	if repoPath == "" {
		repoPath = rootPath
	} else if repoPath, err = resolver.Resolve(repoPath); err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.repo_path: %w", rootName, err)
	}
	resolvedRoot, err := checkout.ResolveRoot(repoPath, rootPath)
	if err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.repo_path: %w", rootName, err)
	}
	seeds := make([]provenance.TrustedSigner, 0, len(entry.SeedSigners))
	for _, seed := range entry.SeedSigners {
		seeds = append(seeds, provenance.TrustedSigner{
			Principal:   seed.Principal,
			PublicKey:   seed.Key,
			Comment:     seed.Label,
			ValidAfter:  seed.ValidAfter,
			ValidBefore: seed.ValidBefore,
		})
	}

	_, statErr := os.Stat(filepath.Join(resolvedRoot.RepoPath, ".git"))
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return true, false, fmt.Errorf("stat %s repository: %w", rootName, statErr)
	}
	signed, err := checkout.OpenSigned(ctx, checkout.SignedSpec{
		Name:           rootName,
		WorktreePath:   resolvedRoot.WorktreePath,
		RepoPath:       resolvedRoot.RepoPath,
		SigningKeyPath: signingKey,
		SeedSigners:    seeds,
		Logger:         logger,
	})
	if err != nil {
		return true, false, fmt.Errorf("bootstrap %s root: %w", rootName, err)
	}
	if err := signed.VerifyHead(ctx); err != nil {
		return true, false, fmt.Errorf("verify %s root birth: %w", rootName, err)
	}
	if _, err := provenance.VerifyAdmission(ctx, resolvedRoot.RepoPath, seeds); err != nil {
		return true, false, fmt.Errorf("verify %s root admission: %w", rootName, err)
	}
	return true, created, nil
}

func dossierRootPath(cfg *platformconfig.Config, rootName string) (string, error) {
	switch rootName {
	case platformconfig.ContactsRootName:
		return cfg.ContactsRoot(), nil
	case platformconfig.DossiersRootName:
		return cfg.DossiersRoot(), nil
	default:
		return "", fmt.Errorf("%q is not a dossier root", rootName)
	}
}
