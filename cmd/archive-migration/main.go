// Command archive-migration relocates a legacy Thane log tree into
// the archive/ layout established in #937 and performs follow-on
// transformations on the migrated tree.
//
// This is a one-shot tool — once the migration has run against every
// known data source and #940 has retired the sqlite content tables,
// this binary can be deleted with no impact on the main thane binary.
//
// Subcommands:
//
//	archive-migration [--copy] <old_root> <new_root>
//	    Move (or copy with --copy) a legacy log tree into the archive
//	    layout. Idempotent. Manifest at <new_root>/meta/migration.jsonl.
//
//	archive-migration legacy-collate <archive_root>
//	    Collate every legacy log source under
//	    <archive_root>/sources/thane_legacy/ — the deprecated
//	    monolithic thane.log, any *.tar.gz snapshots, and the existing
//	    daily thane-YYYY-MM-DD.log.gz files — into one canonical
//	    thane-YYYY-MM-DD.log.gz per UTC date. Lines are deduplicated
//	    across sources. Originals move to collated-sources/ for
//	    forensic preservation.
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
// retires in #940; this tool just relocates them.
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

func main() {
	if err := run(os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run parses CLI arguments and dispatches to the right subcommand.
// Keeping it separate from main() lets tests drive the full code path
// against a bytes.Buffer rather than spawning a subprocess.
func run(w io.Writer, args []string) error {
	if len(args) > 0 && args[0] == "legacy-collate" {
		return runLegacyCollate(w, args[1:])
	}
	copyMode := false
	positional := args[:0]
	for _, a := range args {
		switch a {
		case "--copy", "-c":
			copyMode = true
		case "-h", "--help":
			fmt.Fprintln(w, "Usage:")
			fmt.Fprintln(w, "  archive-migration [--copy] <old_root> <new_root>")
			fmt.Fprintln(w, "  archive-migration legacy-collate <archive_root>")
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Default form moves a legacy Thane log tree into the #937 archive")
			fmt.Fprintln(w, "layout. Move mode renames (destructive). --copy reads and writes,")
			fmt.Fprintln(w, "leaving the source intact — required for cross-filesystem staging.")
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "legacy-collate dedups + re-buckets the monolithic legacy sources")
			fmt.Fprintln(w, "(thane.log, *.tar.gz, existing thane-YYYY-MM-DD.log.gz) into one")
			fmt.Fprintln(w, "canonical gzipped file per UTC date. Originals move to")
			fmt.Fprintln(w, "collated-sources/ for forensic preservation.")
			return nil
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) < 2 {
		return fmt.Errorf("usage: archive-migration [--copy] <old_root> <new_root>")
	}
	oldRoot, err := filepath.Abs(positional[0])
	if err != nil {
		return fmt.Errorf("resolve old root: %w", err)
	}
	newRoot, err := filepath.Abs(positional[1])
	if err != nil {
		return fmt.Errorf("resolve new root: %w", err)
	}
	return migrate(w, oldRoot, newRoot, copyMode)
}

// migrate orchestrates the migration. It ensures the new archive
// skeleton exists, walks the old root, classifies each top-level
// entry, and routes each to the appropriate move handler.
//
// When copyMode is true, files are duplicated via io.Copy and the
// source tree is left untouched; otherwise files are renamed
// (destructive). Idempotency holds in both modes.
func migrate(w io.Writer, oldRoot, newRoot string, copyMode bool) error {
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

	mode := "move"
	if copyMode {
		mode = "copy"
	}
	fmt.Fprintf(w, "Migrating %s → %s (mode: %s)\n", oldRoot, newRoot, mode)

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

	mover := &mover{w: w, manifest: manifest, newRoot: newRoot, copyMode: copyMode}
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
// archive root for relative-path display, copy/move mode, and running
// stats. Methods hang off this so the per-action handlers don't need
// a long parameter list.
type mover struct {
	w        io.Writer
	manifest io.Writer
	newRoot  string
	copyMode bool
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

		// In move mode, prune the now-empty day dir so the next pass
		// finds a clean source. In copy mode the source stays intact.
		if !m.copyMode {
			_ = os.Remove(filepath.Join(oldDir, day.Name()))
		}
	}

	if !m.copyMode {
		_ = os.Remove(oldDir)
	}
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
		if !m.copyMode {
			_ = os.Remove(oldArchive)
			fmt.Fprintf(m.w, "  · archive/ (empty, removed)\n")
		} else {
			fmt.Fprintf(m.w, "  · archive/ (empty, source untouched in copy mode)\n")
		}
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
	if !m.copyMode {
		_ = os.Remove(oldArchive)
	}
	return nil
}

// moveAtomic relocates src to dst exactly once. The semantics depend
// on the mover's mode:
//
//   - Move mode (default): os.Rename; src is consumed on success.
//     Fast and atomic when src and dst live on the same filesystem.
//   - Copy mode: io.Copy with parent mkdirs; src is left untouched.
//     Required for cross-filesystem migration (e.g. SMB → local).
//
// SHA-256 is computed for every action and recorded in the manifest so
// the migration is auditable end-to-end. On collision (dst exists with
// matching size), both files are hashed and the call short-circuits
// only if they are byte-identical — a same-size-different-content
// collision is a hard error rather than silent data loss. A dst with
// different size also refuses rather than overwriting; the operator
// resolves manually.
func (m *mover) moveAtomic(src, dst, category string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if dstInfo, err := os.Stat(dst); err == nil {
		if dstInfo.Size() != srcInfo.Size() {
			return fmt.Errorf("destination %s exists with different size (src=%d, dst=%d); resolve manually", dst, srcInfo.Size(), dstInfo.Size())
		}
		// Sizes match — verify with SHA-256 before declaring
		// "already migrated", because two different files can share
		// a size and a size-only check would risk deleting the src
		// in move mode against a mismatched destination.
		srcSum, err := sha256File(src)
		if err != nil {
			return fmt.Errorf("checksum src %s: %w", src, err)
		}
		dstSum, err := sha256File(dst)
		if err != nil {
			return fmt.Errorf("checksum dst %s: %w", dst, err)
		}
		if srcSum != dstSum {
			return fmt.Errorf("destination %s exists with same size but different content (src sha256=%s, dst sha256=%s); resolve manually", dst, srcSum, dstSum)
		}
		// Idempotent re-run. In move mode, the redundant src is
		// pruned so the next pass sees only the new layout. In
		// copy mode the source stays put.
		if !m.copyMode {
			if err := os.Remove(src); err != nil {
				return fmt.Errorf("remove redundant src %s: %w", src, err)
			}
		}
		m.stats.skipped++
		fmt.Fprintf(m.w, "  · %s (already migrated)\n", m.relDst(dst))
		m.recordManifest(src, dst, category, srcInfo.Size(), srcSum, "already_migrated")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dst, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", dst, err)
	}

	if m.copyMode {
		sum, err := copyFile(src, dst)
		if err != nil {
			return fmt.Errorf("copy %s → %s: %w", src, dst, err)
		}
		m.stats.moved++
		fmt.Fprintf(m.w, "  ✓ %s\n", m.relDst(dst))
		m.recordManifest(src, dst, category, srcInfo.Size(), sum, "copied")
		return nil
	}

	// Hash src before the rename so the manifest captures the
	// content fingerprint even though the path moves. Rename is
	// atomic and content-preserving on the same filesystem, so the
	// pre-rename hash equals the post-rename hash on disk.
	sum, err := sha256File(src)
	if err != nil {
		return fmt.Errorf("checksum src %s: %w", src, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %s → %s: %w", src, dst, err)
	}
	m.stats.moved++
	fmt.Fprintf(m.w, "  ✓ %s\n", m.relDst(dst))
	m.recordManifest(src, dst, category, srcInfo.Size(), sum, "moved")
	return nil
}

// copyFile duplicates src to dst with the source's permissions and
// returns the SHA-256 of the bytes copied. dst is opened with
// O_CREATE|O_EXCL so an existing file at the path is never silently
// overwritten — the caller is responsible for pre-flight existence
// checks (moveAtomic does this). The hash is computed in flight via
// an [io.MultiWriter] so the copy and the digest are a single pass
// over the bytes.
func copyFile(src, dst string) (string, error) {
	srcF, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer srcF.Close()

	srcInfo, err := srcF.Stat()
	if err != nil {
		return "", err
	}

	dstF, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, srcInfo.Mode().Perm())
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dstF, h), srcF); err != nil {
		dstF.Close()
		_ = os.Remove(dst)
		return "", err
	}
	if err := dstF.Sync(); err != nil {
		dstF.Close()
		return "", err
	}
	if err := dstF.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sha256File returns the hex-encoded SHA-256 of the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
// move history is auditable after the fact. sha256 is the hex-encoded
// SHA-256 of the file content (post-move/copy, or the verified shared
// hash for an already_migrated entry).
func (m *mover) recordManifest(src, dst, category string, size int64, sha256 string, status string) {
	entry := map[string]any{
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
		"src":      src,
		"dst":      dst,
		"category": category,
		"size":     size,
		"sha256":   sha256,
		"status":   status,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return // best-effort manifest write — don't fail the migration
	}
	_, _ = m.manifest.Write(append(line, '\n'))
}

// writeIfMissing atomically creates path with the given permissions and
// writes data to it. If the file already exists it is left untouched.
// The create uses O_CREATE|O_EXCL so there is no race between checking
// and writing.
func writeIfMissing(w io.Writer, path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(w, "  · %s (exists, skipping)\n", path)
			return nil
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", path, closeErr)
	}
	fmt.Fprintf(w, "  ✓ %s\n", path)
	return nil
}
