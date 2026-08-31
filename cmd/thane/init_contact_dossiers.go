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
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"gopkg.in/yaml.v3"
)

func bootstrapContactDossierRoot(ctx context.Context, configPath, workspace string, logger *slog.Logger) (bool, bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false, false, fmt.Errorf("read core config for contact dossier root: %w", err)
	}
	var cfg struct {
		Roots map[string]platformconfig.RootEntry `yaml:"roots"`
	}
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(data))), &cfg); err != nil {
		return false, false, fmt.Errorf("parse core config for contact dossier root: %w", err)
	}
	var entry platformconfig.RootEntry
	found := false
	for name, candidate := range cfg.Roots {
		if strings.TrimSuffix(strings.TrimSpace(name), ":") == platformconfig.ContactsRootName {
			entry = candidate
			found = true
			break
		}
	}
	if !found {
		return false, false, nil
	}
	if !entry.Git.Enabled || !entry.Git.SignCommits {
		return true, false, fmt.Errorf("roots.%s must enable signed commits so thane init can establish its dossier history", platformconfig.ContactsRootName)
	}
	if len(entry.SeedSigners) == 0 {
		return true, false, fmt.Errorf("roots.%s must declare seed_signers before thane init can establish it", platformconfig.ContactsRootName)
	}

	signingKey, err := resolveInitSigningKey(workspace, entry.Git.SigningKey)
	if err != nil {
		return true, false, fmt.Errorf("resolve roots.%s.git.signing_key: %w", platformconfig.ContactsRootName, err)
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

	rootPath := filepath.Join(workspace, platformconfig.ContactsRootName)
	_, statErr := os.Stat(filepath.Join(rootPath, ".git"))
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return true, false, fmt.Errorf("stat contact dossier repository: %w", statErr)
	}
	signed, err := checkout.OpenSigned(ctx, checkout.SignedSpec{
		Name:           "contacts.dossiers",
		WorktreePath:   rootPath,
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
	if _, err := provenance.VerifyAdmission(ctx, rootPath, seeds); err != nil {
		return true, false, fmt.Errorf("verify contact dossier root admission: %w", err)
	}
	return true, created, nil
}

func resolveInitSigningKey(workspace, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("signing key is empty")
	}
	if strings.HasPrefix(raw, platformconfig.CoreRootName+":") {
		rel := strings.TrimPrefix(raw, platformconfig.CoreRootName+":")
		rel = filepath.Clean(filepath.FromSlash(rel))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("invalid core-relative signing key %q", raw)
		}
		return filepath.Join(workspace, platformconfig.CoreRootName, rel), nil
	}
	if strings.HasPrefix(raw, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		switch {
		case raw == "~":
			return home, nil
		case strings.HasPrefix(raw, "~/"):
			return filepath.Join(home, raw[2:]), nil
		}
	}
	return filepath.Abs(raw)
}
