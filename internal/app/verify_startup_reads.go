package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// verifyStartupReads closes the doc-root verification bypass paths
// surfaced by issue #788. The model's file tools are gated at call
// time once the verifier is installed; inject-files and talents are
// loaded once at startup and bypass the doc store entirely. Run them
// through Store.VerifyPath here so each root's verify_signatures
// policy applies.
//
// Behavior matches the policy mode of the enclosing root:
//   - none: no check (VerifyPath returns nil)
//   - warn: VerifyPath logs the failure and returns nil; we continue
//   - required: VerifyPath returns an error; we abort startup with
//     a wrapped error identifying the consumer site
//
// Paths outside any managed root are no-ops inside VerifyPath, so
// non-managed inject-files / talent dirs keep their original
// unverified behavior.
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

	if dir := strings.TrimSpace(a.cfg.TalentsDir); dir != "" {
		paths, err := listTalentFiles(dir)
		if err != nil {
			return fmt.Errorf("talents verification: %w", err)
		}
		for _, p := range paths {
			if err := store.VerifyPath(ctx, p, "talents"); err != nil {
				return fmt.Errorf("talents verification: %w", err)
			}
		}
	}

	return nil
}

// listTalentFiles returns paths of .md files in dir (joined with dir
// as-is, so the result is absolute when dir is absolute and relative
// when dir is relative — [documents.Store.VerifyPath] handles both
// via [filepath.Abs]). Sorted for deterministic verification order.
// Returns nil (no error) when dir does not exist, matching
// [talents.Loader] which silently ignores a missing talents
// directory.
func listTalentFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read talents dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}
