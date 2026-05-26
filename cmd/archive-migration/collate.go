package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// runLegacyCollate is the entry point for the `legacy-collate` subcommand.
// It reads every legacy log source under <archive_root>/sources/thane_legacy/
// (the deprecated monolithic thane.log, any .tar.gz snapshots, and the
// existing daily thane-YYYY-MM-DD.log.gz files), deduplicates lines across
// sources, re-buckets by UTC date, and writes a single canonical
// thane-YYYY-MM-DD.log.gz file per date.
//
// Original source files move to <legacy>/collated-sources/ for forensic
// preservation. The operation refuses to run if collated-sources/ already
// exists — operator removes it to force a re-run.
func runLegacyCollate(w io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: archive-migration legacy-collate <archive_root>")
	}
	archiveRoot, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve archive root: %w", err)
	}
	return collateLegacy(w, archiveRoot)
}

// collateLegacy orchestrates the dedup + re-bucket. See [runLegacyCollate]
// for the full contract.
func collateLegacy(w io.Writer, archiveRoot string) error {
	legacyDir := filepath.Join(archiveRoot, "sources", "thane_legacy")
	if info, err := os.Stat(legacyDir); err != nil {
		return fmt.Errorf("legacy dir %s: %w", legacyDir, err)
	} else if !info.IsDir() {
		return fmt.Errorf("legacy path is not a directory: %s", legacyDir)
	}

	consumedDir := filepath.Join(legacyDir, "collated-sources")
	if _, err := os.Stat(consumedDir); err == nil {
		return fmt.Errorf("collated-sources/ already exists at %s — remove it to force a re-run", consumedDir)
	}

	sources, err := findLegacySources(legacyDir)
	if err != nil {
		return fmt.Errorf("scan sources: %w", err)
	}
	if len(sources) == 0 {
		fmt.Fprintln(w, "no legacy sources to collate")
		return nil
	}
	fmt.Fprintf(w, "Collating %d legacy source files\n", len(sources))

	seen := make(map[[32]byte]bool, 1<<20)
	perDate := make(map[string][]dateLine)
	var stats collateStats

	for _, src := range sources {
		rel, _ := filepath.Rel(archiveRoot, src)
		fmt.Fprintf(w, "  reading %s\n", rel)
		if err := readSourceLines(src, &stats, seen, perDate); err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
	}

	// Write daily files to a staging subdir first so a crash mid-write
	// can't leave a partial canonical set in place.
	stagingDir := filepath.Join(legacyDir, ".collate-staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	for date, entries := range perDate {
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].ts.Before(entries[j].ts)
		})
		outPath := filepath.Join(stagingDir, fmt.Sprintf("thane-%s.log.gz", date))
		if err := writeGzippedLines(outPath, entries); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		fmt.Fprintf(w, "  ✓ %s (%d lines)\n", filepath.Base(outPath), len(entries))
	}

	// Move originals into collated-sources/ to preserve them as forensic
	// evidence while keeping them out of the canonical legacy view.
	if err := os.MkdirAll(consumedDir, 0o755); err != nil {
		return fmt.Errorf("create collated-sources: %w", err)
	}
	for _, src := range sources {
		dst := filepath.Join(consumedDir, filepath.Base(src))
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %s → %s: %w", src, dst, err)
		}
	}

	// Promote the staged daily files into place.
	stagedEntries, err := os.ReadDir(stagingDir)
	if err != nil {
		return fmt.Errorf("read staging dir: %w", err)
	}
	for _, entry := range stagedEntries {
		srcPath := filepath.Join(stagingDir, entry.Name())
		dstPath := filepath.Join(legacyDir, entry.Name())
		if err := os.Rename(srcPath, dstPath); err != nil {
			return fmt.Errorf("promote %s: %w", entry.Name(), err)
		}
	}
	_ = os.Remove(stagingDir)

	fmt.Fprintf(w, "\n%d daily files written, %d unique lines, %d duplicates dropped, %d malformed skipped, %d unparseable-timestamp lines bucketed under thane-unknown.log.gz\n",
		len(perDate), stats.unique, stats.duplicates, stats.malformed, stats.unparseable,
	)
	return nil
}

type collateStats struct {
	unique      int
	duplicates  int
	malformed   int
	unparseable int
}

// dateLine pairs a log line with the parsed UTC timestamp used for
// chronological ordering within each daily output file.
type dateLine struct {
	ts   time.Time
	line string
}

// dailyGzPattern matches the canonical daily filename so the collator
// knows to read existing thane-YYYY-MM-DD.log.gz files alongside the
// deprecated monolithic sources.
var dailyGzPattern = regexp.MustCompile(`^thane-\d{4}-\d{2}-\d{2}\.log\.gz$`)

// findLegacySources walks the legacy dir and returns the absolute paths
// of every readable source: the deprecated thane.log, any *.tar.gz
// snapshots, and the existing daily thane-YYYY-MM-DD.log.gz files. The
// thane_legacy README.md, the stderr.log, and the previously-existing
// collated-sources/ subdir are explicitly excluded.
func findLegacySources(legacyDir string) ([]string, error) {
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return nil, err
	}
	var sources []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case name == "thane.log":
			sources = append(sources, filepath.Join(legacyDir, name))
		case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
			sources = append(sources, filepath.Join(legacyDir, name))
		case dailyGzPattern.MatchString(name):
			sources = append(sources, filepath.Join(legacyDir, name))
		}
	}
	sort.Strings(sources)
	return sources, nil
}

// readSourceLines opens a single source file (plain, gzip, or tarball)
// and feeds each meaningful line through the dedup + bucketing pipeline.
// Malformed lines and unparseable timestamps are counted but do not abort
// the run.
func readSourceLines(path string, stats *collateStats, seen map[[32]byte]bool, perDate map[string][]dateLine) error {
	r, closers, err := openSource(path)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	return scanLines(r, stats, seen, perDate)
}

// openSource returns an io.Reader yielding plain-text log lines from the
// path. The returned closers must be closed in reverse order by the
// caller. Tarballs are required to contain a single top-level "thane.log"
// entry; anything else is treated as a malformed archive.
func openSource(path string) (io.Reader, []io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	closers := []io.Closer{f}

	switch {
	case strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, closers, fmt.Errorf("gzip open tarball: %w", err)
		}
		closers = append(closers, gz)
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return nil, closers, fmt.Errorf("tarball %s has no thane.log entry", path)
			}
			if err != nil {
				return nil, closers, fmt.Errorf("tarball read: %w", err)
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			if filepath.Base(h.Name) == "thane.log" {
				return tr, closers, nil
			}
		}
	case strings.HasSuffix(path, ".gz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, closers, fmt.Errorf("gzip open: %w", err)
		}
		closers = append(closers, gz)
		return gz, closers, nil
	default:
		return f, closers, nil
	}
}

// scanLines reads newline-delimited records from r, hashes each one for
// dedup, parses the timestamp to a UTC date bucket, and appends to
// perDate. The buffer is sized generously because individual JSON log
// lines can exceed 64 KiB when payload fields are large.
func scanLines(r io.Reader, stats *collateStats, seen map[[32]byte]bool, perDate map[string][]dateLine) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 16*1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		hash := sha256.Sum256([]byte(line))
		if seen[hash] {
			stats.duplicates++
			continue
		}
		seen[hash] = true
		stats.unique++

		ts, ok := extractTimestamp(line)
		if !ok {
			stats.unparseable++
			perDate["unknown"] = append(perDate["unknown"], dateLine{ts: time.Time{}, line: line})
			continue
		}
		bucket := ts.UTC().Format("2006-01-02")
		perDate[bucket] = append(perDate[bucket], dateLine{ts: ts, line: line})
	}
	if err := scanner.Err(); err != nil {
		stats.malformed++
		return err
	}
	return nil
}

// jsonTimePattern matches the "time":"..." field in JSON slog records.
// We use a regex rather than json.Unmarshal because the lines may have
// payload fields with embedded quotes, and a full unmarshal of every
// line is wasteful when all we need is the timestamp.
var jsonTimePattern = regexp.MustCompile(`"time"\s*:\s*"([^"]+)"`)

// logfmtTimePattern matches the time=... key in the pre-JSON logfmt era
// (Feb 2026 tarballs). The leading boundary excludes substrings that
// happen to contain "time=" mid-field.
var logfmtTimePattern = regexp.MustCompile(`(?:^|\s)time=(\S+)`)

// timestampLayouts is the ordered list of layouts attempted when parsing
// the extracted timestamp string. RFC3339Nano covers the JSON slog
// records; the explicit layout covers logfmt records where Go's
// canonical layouts don't always round-trip cleanly.
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999-07:00",
	"2006-01-02T15:04:05.999-07:00",
}

// extractTimestamp pulls the record's timestamp from either format
// (JSON's "time" field or logfmt's time= key) and parses it. Returns
// false when neither pattern matches or the value cannot be parsed.
func extractTimestamp(line string) (time.Time, bool) {
	var raw string
	if m := jsonTimePattern.FindStringSubmatch(line); m != nil {
		raw = m[1]
	} else if m := logfmtTimePattern.FindStringSubmatch(line); m != nil {
		raw = m[1]
	} else {
		return time.Time{}, false
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// writeGzippedLines writes the sorted dateLine entries to a gzipped file.
// The file is created with O_CREATE|O_EXCL because the caller drove the
// staging dir; an existing destination indicates a programming error
// (two writers for the same date) rather than a recoverable retry.
func writeGzippedLines(path string, entries []dateLine) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	bw := bufio.NewWriter(gz)
	for _, e := range entries {
		if _, err := bw.WriteString(e.line); err != nil {
			gz.Close()
			f.Close()
			_ = os.Remove(path)
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			gz.Close()
			f.Close()
			_ = os.Remove(path)
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		gz.Close()
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}
