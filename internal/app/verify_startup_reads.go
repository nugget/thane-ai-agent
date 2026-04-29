package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// verifyStartupReads closes the doc-root verification bypass paths
// surfaced by issue #788. The model's file tools are gated at call
// time once the verifier is installed; inject-files are model-facing
// content that bypass the doc store. Run them through Store.VerifyPath
// here so each root's verify_signatures policy fails fast during
// startup. The agent loop also verifies inject-files again each time
// they are read into the prompt.
//
// Behavior matches the policy mode of the enclosing root:
//   - none: no check (VerifyPath returns nil)
//   - warn: VerifyPath logs the failure and returns nil; we continue
//   - required: VerifyPath returns an error; we abort startup with
//     a wrapped error identifying the consumer site
//
// Paths outside any managed root are no-ops inside VerifyPath, so
// non-managed inject-files keep their original unverified behavior.
func (a *App) verifyStartupReads(ctx context.Context, store *documents.Store, injectFiles []string) error {
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for _, p := range injectFiles {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := store.VerifyPath(ctx, p, "inject_files"); err != nil {
			return fmt.Errorf("inject_files verification: %w", err)
		}
	}

	return nil
}

func (a *App) loadTalents(ctx context.Context, verifier talents.VerifyPathFunc) ([]talents.Talent, error) {
	if a == nil || a.cfg == nil {
		return nil, nil
	}
	loader := talents.NewLoader(a.cfg.TalentsDir)
	parsedTalents, err := loader.TalentsVerified(ctx, verifier, "talents")
	if err != nil {
		return nil, fmt.Errorf("load talents: %w", err)
	}
	if len(parsedTalents) > 0 {
		names := make([]string, 0, len(parsedTalents))
		for _, talent := range parsedTalents {
			names = append(names, talent.Name)
		}
		logger := a.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Info("talents loaded", "dir", a.cfg.TalentsDir, "count", len(parsedTalents), "talents", names)
	}
	return parsedTalents, nil
}
