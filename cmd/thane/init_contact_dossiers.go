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
	cfg, err := platformconfig.LoadWithWorkspace(configPath, workspace)
	if err != nil {
		return false, false, fmt.Errorf("load core config for contact dossier root: %w", err)
	}
	entry, found := cfg.DocRoots[platformconfig.ContactsRootName]
	if !found {
		return false, false, nil
	}
	if !entry.Git.Enabled || !entry.Git.SignCommits {
		return true, false, fmt.Errorf("roots.%s must enable signed commits so thane init can establish its dossier history", platformconfig.ContactsRootName)
	}
	if len(entry.SeedSigners) == 0 {
		return true, false, fmt.Errorf("roots.%s must declare seed_signers before thane init can establish it", platformconfig.ContactsRootName)
	}

	rootPaths := make(map[string]string, len(cfg.Paths)+3)
	for name, path := range cfg.Paths {
		rootPaths[name] = path
	}
	rootPaths[platformconfig.CoreRootName] = cfg.CoreRoot()
	rootPaths[platformconfig.SelfRootName] = cfg.SelfRoot()
	rootPaths[platformconfig.ContactsRootName] = cfg.ContactsRoot()
	resolver := paths.New(rootPaths)

	signingKey, err := resolver.Resolve(entry.Git.SigningKey)
	if err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.signing_key: %w", platformconfig.ContactsRootName, err)
	}
	repoPath := strings.TrimSpace(entry.Git.RepoPath)
	if repoPath == "" {
		repoPath = cfg.ContactsRoot()
	} else if repoPath, err = resolver.Resolve(repoPath); err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.repo_path: %w", platformconfig.ContactsRootName, err)
	}
	resolvedRoot, err := checkout.ResolveRoot(repoPath, cfg.ContactsRoot())
	if err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.repo_path: %w", platformconfig.ContactsRootName, err)
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
		return true, false, fmt.Errorf("stat contact dossier repository: %w", statErr)
	}
	signed, err := checkout.OpenSigned(ctx, checkout.SignedSpec{
		Name:           "contacts.dossiers",
		WorktreePath:   resolvedRoot.WorktreePath,
		RepoPath:       resolvedRoot.RepoPath,
		SigningKeyPath: signingKey,
		SeedSigners:    seeds,
		Logger:         logger,
	})
	if err != nil {
		return true, false, fmt.Errorf("bootstrap contact dossier root: %w", err)
	}
	if err := signed.VerifyHead(ctx); err != nil {
		return true, false, fmt.Errorf("verify contact dossier root birth: %w", err)
	}
	if _, err := provenance.VerifyAdmission(ctx, resolvedRoot.RepoPath, seeds); err != nil {
		return true, false, fmt.Errorf("verify contact dossier root admission: %w", err)
	}
	return true, created, nil
}
