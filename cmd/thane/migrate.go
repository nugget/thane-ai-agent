package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// sourcesThaneLegacyReadmeMD documents the pre-cutover Thane artifacts
// that the migration moves into sources/thane_legacy/.
//
//go:embed sources_thane_legacy_readme.md
var sourcesThaneLegacyReadmeMD []byte

// datasetRename maps an old top-level dataset directory name to the
// dataset name it occupies in the new layout. Datasets that did not
// change name map to themselves; "access" gets renamed to
// "http_access" to match the constant value rename in #937.
var datasetRename = map[string]string{
	"loops":         "loops",
	"events":        "events",
	"envelopes":     "envelopes",
	"requests":      "requests",
	"access":        "http_access",
	"http_access":   "http_access",
	"conversations": "conversations",
	"delegates":     "delegates",
}

// legacyFilenames are pre-cutover artifacts that move verbatim into
// sources/thane_legacy/ (preserving the original filename).
var legacyFilenames = map[string]bool{
	"thane.log":                 true,
	"stderr.log":                true,
	"original-thane-log.tar.gz": true,
	"final-thane-log.tar.gz":    true,
}

// legacyGzPattern matches the daily-rotated gzipped slog era
// (e.g. thane-2026-03-12.log.gz).
var legacyGzPattern = regexp.MustCompile(`^thane-\d{4}-\d{2}-\d{2}\.log\.gz$`)

// indexFilenames are the sqlite log index files that move to the new
// archive root verbatim (no path nesting, no rename). Their content
// retires in #940; this issue just relocates them.
var indexFilenames = map[string]bool{
	"logs.db":     true,
	"logs.db-wal": true,
	"logs.db-shm": true,
}

// oldHourPattern matches the legacy partition filename HH.jsonl
// (00.jsonl ... 23.jsonl). Combined with the parent directory name
// YYYY-MM-DD it identifies a dataset segment in the old layout.
var oldHourPattern = regexp.MustCompile(`^(\d{2})\.jsonl$`)

// oldDayPattern matches the legacy date-directory name YYYY-MM-DD.
var oldDayPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// runMigrate handles `thane migrate <old_root> <new_root>`. It walks
// the old log root and moves every recognized file into the #937
// archive layout under new_root. Idempotent: re-running on a
// partially-migrated tree resumes cleanly.
func runMigrate(w io.Writer, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: thane migrate <old_root> <new_root>")
	}
	oldRoot, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve old root: %w", err)
	}
	newRoot, err := filepath.Abs(args[1])
	if err != nil {
		return fmt.Errorf("resolve new root: %w", err)
	}
	return migrate(w, oldRoot, newRoot)
}

// migrate orchestrates the migration. It ensures the new archive
// skeleton exists, walks the old root, classifies each top-level
// entry, and routes each to the appropriate move handler.
func migrate(w io.Writer, oldRoot, newRoot string) error {
	if _, err := os.Stat(oldRoot); err != nil {
		return fmt.Errorf("old root %s: %w", oldRoot, err)
	}

	for _, sub := range []string{
		filepath.Join("sources", "thane"),
		filepath.Join("sources", "thane_legacy"),
		"interactions",
		filepath.Join("meta", "schema"),
	} {
		if err := os.MkdirAll(filepath.Join(newRoot, sub), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", sub, err)
		}
	}

	fmt.Fprintf(w, "Migrating %s → %s\n", oldRoot, newRoot)

	manifestPath := filepath.Join(newRoot, "meta", "migration.jsonl")
	manifest, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer manifest.Close()

	entries, err := os.ReadDir(oldRoot)
	if err != nil {
		return fmt.Errorf("read old root: %w", err)
	}

	mover := &mover{w: w, manifest: manifest, newRoot: newRoot}
	for _, entry := range entries {
		if err := mover.classifyAndMove(oldRoot, entry); err != nil {
			fmt.Fprintf(w, "  ! %s: %v\n", entry.Name(), err)
			mover.stats.errors++
		}
	}
	stats := mover.stats

	// Write the thane_legacy README if any pre-cutover artifacts arrived.
	if stats.legacyMoved > 0 {
		legacyReadme := filepath.Join(newRoot, "sources", "thane_legacy", "README.md")
		if err := writeIfMissing(w, legacyReadme, sourcesThaneLegacyReadmeMD, 0o644); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "\n%d moved, %d already in place, %d unknown (skipped), %d errors\n",
		stats.moved, stats.skipped, stats.unknown, stats.errors)
	if stats.errors > 0 {
		return fmt.Errorf("migration completed with %d errors — see output above", stats.errors)
	}
	return nil
}

type migrationStats struct {
	moved       int
	skipped     int
	unknown     int
	errors      int
	legacyMoved int
}

// mover bundles the migration runtime — output writer, manifest, the
// archive root for relative-path display, and running stats. Methods
// hang off this so the per-action handlers don't need a long
// parameter list.
type mover struct {
	w        io.Writer
	manifest io.Writer
	newRoot  string
	stats    migrationStats
}

// classifyAndMove routes one old-root entry to the right handler based
// on its name and shape.
func (m *mover) classifyAndMove(oldRoot string, entry fs.DirEntry) error {
	name := entry.Name()

	// SQLite index files — flat move into new root.
	if indexFilenames[name] {
		return m.moveAtomic(filepath.Join(oldRoot, name), filepath.Join(m.newRoot, name), "index_db")
	}

	// Legacy single-file artifacts — flat move into sources/thane_legacy/.
	if legacyFilenames[name] || legacyGzPattern.MatchString(name) {
		dst := filepath.Join(m.newRoot, "sources", "thane_legacy", name)
		if err := m.moveAtomic(filepath.Join(oldRoot, name), dst, "legacy"); err != nil {
			return err
		}
		m.stats.legacyMoved++
		return nil
	}

	// Datasets — recurse into the YYYY-MM-DD/HH.jsonl structure.
	if entry.IsDir() {
		if newDataset, ok := datasetRename[name]; ok {
			return m.migrateDataset(oldRoot, name, newDataset)
		}
		// archive/ in the old layout was the empty content-archive dir.
		// If it has content we don't recognize, warn and stash.
		if name == "archive" {
			return m.migrateLegacyArchiveDir(oldRoot)
		}
	}

	// Unknown entry — leave in place, warn.
	fmt.Fprintf(m.w, "  ? %s (unknown, left in place)\n", name)
	m.stats.unknown++
	return nil
}

// migrateDataset moves every YYYY-MM-DD/HH.jsonl segment under an old
// dataset directory to the new YYYY/MM/DD/<dataset>-YYYY-MM-DD-HH.jsonl
// layout.
func (m *mover) migrateDataset(oldRoot, oldDataset, newDataset string) error {
	oldDir := filepath.Join(oldRoot, oldDataset)
	dayEntries, err := os.ReadDir(oldDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", oldDataset, err)
	}

	for _, day := range dayEntries {
		if !day.IsDir() {
			continue
		}
		dm := oldDayPattern.FindStringSubmatch(day.Name())
		if dm == nil {
			fmt.Fprintf(m.w, "  ? %s/%s (not a YYYY-MM-DD dir, skipping)\n", oldDataset, day.Name())
			m.stats.unknown++
			continue
		}
		year, month, dayNum := dm[1], dm[2], dm[3]
		date := fmt.Sprintf("%s-%s-%s", year, month, dayNum)

		hourEntries, err := os.ReadDir(filepath.Join(oldDir, day.Name()))
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", oldDataset, day.Name(), err)
		}
		for _, hourEntry := range hourEntries {
			if hourEntry.IsDir() {
				continue
			}
			hm := oldHourPattern.FindStringSubmatch(hourEntry.Name())
			if hm == nil {
				fmt.Fprintf(m.w, "  ? %s/%s/%s (not HH.jsonl, skipping)\n", oldDataset, day.Name(), hourEntry.Name())
				m.stats.unknown++
				continue
			}
			hour := hm[1]
			oldPath := filepath.Join(oldDir, day.Name(), hourEntry.Name())
			newFilename := fmt.Sprintf("%s-%s-%s.jsonl", newDataset, date, hour)
			newPath := filepath.Join(m.newRoot, "sources", "thane", newDataset, year, month, dayNum, newFilename)
			if err := m.moveAtomic(oldPath, newPath, "dataset:"+newDataset); err != nil {
				return err
			}
		}

		// Try to remove the now-empty day dir. Ignore errors — leftover
		// files are tolerated.
		_ = os.Remove(filepath.Join(oldDir, day.Name()))
	}

	// Try to remove the now-empty dataset dir.
	_ = os.Remove(oldDir)
	return nil
}

// migrateLegacyArchiveDir handles the old empty `archive/`
// content-archive directory at the root of the old log tree. If it's
// empty, remove it (it's dead state from the never-fired archiver).
// If it has content, stash it under sources/thane_legacy/content_archive/
// so nothing is lost.
func (m *mover) migrateLegacyArchiveDir(oldRoot string) error {
	oldArchive := filepath.Join(oldRoot, "archive")
	entries, err := os.ReadDir(oldArchive)
	if err != nil {
		return fmt.Errorf("read old archive dir: %w", err)
	}
	if len(entries) == 0 {
		_ = os.Remove(oldArchive)
		fmt.Fprintf(m.w, "  · archive/ (empty, removed)\n")
		return nil
	}
	dst := filepath.Join(m.newRoot, "sources", "thane_legacy", "content_archive")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("create thane_legacy/content_archive: %w", err)
	}
	for _, entry := range entries {
		oldPath := filepath.Join(oldArchive, entry.Name())
		newPath := filepath.Join(dst, entry.Name())
		if err := m.moveAtomic(oldPath, newPath, "legacy_content_archive"); err != nil {
			return err
		}
		m.stats.legacyMoved++
	}
	_ = os.Remove(oldArchive)
	return nil
}

// moveAtomic renames src to dst if dst does not already exist. If dst
// exists with the same size as src, treat as idempotent (already
// migrated) and remove src. If dst exists with a different size,
// refuse — the operator must resolve manually.
func (m *mover) moveAtomic(src, dst, category string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if dstInfo, err := os.Stat(dst); err == nil {
		if dstInfo.Size() == srcInfo.Size() {
			// Idempotent re-run — dst already holds what src holds.
			// Remove src so the next run sees only the new layout.
			if err := os.Remove(src); err != nil {
				return fmt.Errorf("remove redundant src %s: %w", src, err)
			}
			m.stats.skipped++
			fmt.Fprintf(m.w, "  · %s (already migrated)\n", m.relDst(dst))
			m.recordManifest(src, dst, category, srcInfo.Size(), "already_migrated")
			return nil
		}
		return fmt.Errorf("destination %s exists with different size (src=%d, dst=%d); resolve manually", dst, srcInfo.Size(), dstInfo.Size())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dst, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", dst, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %s → %s: %w", src, dst, err)
	}
	m.stats.moved++
	fmt.Fprintf(m.w, "  ✓ %s\n", m.relDst(dst))
	m.recordManifest(src, dst, category, srcInfo.Size(), "moved")
	return nil
}

// relDst returns dst relative to the archive root, falling back to dst
// itself if the relative computation fails.
func (m *mover) relDst(dst string) string {
	rel, err := filepath.Rel(m.newRoot, dst)
	if err != nil {
		return dst
	}
	return rel
}

// recordManifest appends one migration action as a JSONL line so the
// move history is auditable after the fact.
func (m *mover) recordManifest(src, dst, category string, size int64, status string) {
	entry := map[string]any{
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
		"src":      src,
		"dst":      dst,
		"category": category,
		"size":     size,
		"status":   status,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return // best-effort manifest write — don't fail the migration
	}
	_, _ = m.manifest.Write(append(line, '\n'))
}
