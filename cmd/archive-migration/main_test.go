package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedOldLayout writes a representative legacy log tree under root and
// returns a map of relative path → sha256 hex for later content
// verification.
func seedOldLayout(t *testing.T, root string) map[string]string {
	t.Helper()

	files := map[string]string{
		"loops/2026-04-22/18.jsonl":     `{"event_id":"a","kind":"loop_start"}`,
		"loops/2026-04-23/05.jsonl":     `{"event_id":"b","kind":"loop_tick"}`,
		"events/2026-05-26/14.jsonl":    `{"event_id":"c","kind":"x","ts":"2026-05-26T14:00:00Z"}`,
		"access/2026-05-26/05.jsonl":    `{"event_id":"d","kind":"http_access"}`,
		"requests/2026-05-26/05.jsonl":  `{"event_id":"e","kind":"request_start"}`,
		"envelopes/2026-05-26/02.jsonl": `{"event_id":"f","kind":"delivery_queued"}`,
		"thane.log":                     "deprecated final line",
		"stderr.log":                    "stderr from old era",
		"thane-2026-03-12.log.gz":       "fake gzipped legacy slog",
		"original-thane-log.tar.gz":     "fake legacy tarball",
		"final-thane-log.tar.gz":        "fake legacy tarball 2",
		"logs.db":                       "fake sqlite",
		"logs.db-wal":                   "fake wal",
		"logs.db-shm":                   "fake shm",
	}

	checksums := make(map[string]string, len(files))
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("setup mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("setup write %s: %v", abs, err)
		}
		sum := sha256.Sum256([]byte(body))
		checksums[rel] = hex.EncodeToString(sum[:])
	}
	// Empty archive/ directory — exercises the empty-archive removal path.
	if err := os.MkdirAll(filepath.Join(root, "archive"), 0o755); err != nil {
		t.Fatalf("setup mkdir archive/: %v", err)
	}
	return checksums
}

// assertFileWithChecksum verifies path exists with the expected sha256.
func assertFileWithChecksum(t *testing.T, path, wantHex string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != wantHex {
		t.Errorf("%s checksum mismatch: got %s, want %s", path, hex.EncodeToString(got[:]), wantHex)
	}
}

func TestRunMigrate_RelocatesEverythingLosslessly(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")

	checksums := seedOldLayout(t, old)

	var buf bytes.Buffer
	if err := run(&buf, []string{old, newRoot}); err != nil {
		t.Fatalf("run: %v\noutput:\n%s", err, buf.String())
	}

	// Datasets land at sources/thane/<dataset>/YYYY/MM/DD/<dataset>-YYYY-MM-DD-HH.jsonl.
	// Note: access/ becomes http_access/ per the #937 rename.
	wantDest := map[string]string{
		"loops/2026-04-22/18.jsonl":     "sources/thane/loops/2026/04/22/loops-2026-04-22-18.jsonl",
		"loops/2026-04-23/05.jsonl":     "sources/thane/loops/2026/04/23/loops-2026-04-23-05.jsonl",
		"events/2026-05-26/14.jsonl":    "sources/thane/events/2026/05/26/events-2026-05-26-14.jsonl",
		"access/2026-05-26/05.jsonl":    "sources/thane/http_access/2026/05/26/http_access-2026-05-26-05.jsonl",
		"requests/2026-05-26/05.jsonl":  "sources/thane/requests/2026/05/26/requests-2026-05-26-05.jsonl",
		"envelopes/2026-05-26/02.jsonl": "sources/thane/envelopes/2026/05/26/envelopes-2026-05-26-02.jsonl",
		"thane.log":                     "sources/thane_legacy/thane.log",
		"stderr.log":                    "sources/thane_legacy/stderr.log",
		"thane-2026-03-12.log.gz":       "sources/thane_legacy/thane-2026-03-12.log.gz",
		"original-thane-log.tar.gz":     "sources/thane_legacy/original-thane-log.tar.gz",
		"final-thane-log.tar.gz":        "sources/thane_legacy/final-thane-log.tar.gz",
		"logs.db":                       "logs.db",
		"logs.db-wal":                   "logs.db-wal",
		"logs.db-shm":                   "logs.db-shm",
	}

	for srcRel, dstRel := range wantDest {
		assertFileWithChecksum(t, filepath.Join(newRoot, dstRel), checksums[srcRel])
	}

	// Empty archive/ dir should be removed.
	if _, err := os.Stat(filepath.Join(old, "archive")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old archive/ dir should be removed (empty), got err=%v", err)
	}

	// Legacy README dropped because we moved legacy artifacts.
	legacyReadme := filepath.Join(newRoot, "sources", "thane_legacy", "README.md")
	if _, err := os.Stat(legacyReadme); err != nil {
		t.Errorf("expected sources/thane_legacy/README.md, got %v", err)
	}

	// Manifest exists and has one line per moved file, each carrying
	// the recorded sha256 of the migrated file content.
	manifest, err := os.ReadFile(filepath.Join(newRoot, "meta", "migration.jsonl"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(manifest)), "\n")
	if len(lines) != len(wantDest) {
		t.Errorf("manifest line count = %d, want %d", len(lines), len(wantDest))
	}
	manifestBySrc := make(map[string]map[string]any, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("manifest line is not JSON: %v\n%s", err, line)
		}
		src, _ := entry["src"].(string)
		manifestBySrc[src] = entry
	}
	for srcRel, sum := range checksums {
		entry, ok := manifestBySrc[filepath.Join(old, srcRel)]
		if !ok {
			t.Errorf("no manifest entry for src %s", srcRel)
			continue
		}
		gotSum, _ := entry["sha256"].(string)
		if gotSum != sum {
			t.Errorf("manifest sha256 for %s = %q, want %q", srcRel, gotSum, sum)
		}
	}
}

// TestRunMigrate_RefusesSameSizeDifferentContent guards against the
// data-loss path Copilot flagged on #952: a size-only idempotency
// check would silently delete src when a dst happened to have the
// same byte count but different content. moveAtomic must verify
// SHA-256 before declaring "already migrated".
func TestRunMigrate_RefusesSameSizeDifferentContent(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")
	if err := os.MkdirAll(filepath.Join(old, "loops", "2026-04-22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	srcPath := filepath.Join(old, "loops", "2026-04-22", "18.jsonl")
	srcBody := []byte("aaaaaa")
	if err := os.WriteFile(srcPath, srcBody, 0o644); err != nil {
		t.Fatalf("setup src: %v", err)
	}

	// Pre-place a conflicting destination with same size, different bytes.
	dst := filepath.Join(newRoot, "sources", "thane", "loops", "2026", "04", "22", "loops-2026-04-22-18.jsonl")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("setup dst dir: %v", err)
	}
	dstBody := []byte("bbbbbb")
	if len(dstBody) != len(srcBody) {
		t.Fatalf("test invariant: dstBody must equal srcBody in size")
	}
	if err := os.WriteFile(dst, dstBody, 0o644); err != nil {
		t.Fatalf("setup dst: %v", err)
	}

	var buf bytes.Buffer
	err := run(&buf, []string{old, newRoot})
	if err == nil {
		t.Fatalf("expected error for same-size-different-content collision, got nil\noutput:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "same size but different content") {
		t.Errorf("expected sha256-mismatch message, got:\n%s", buf.String())
	}
	// Critical: src must still exist. A regression to the size-only
	// check would have deleted it.
	if _, err := os.Stat(srcPath); err != nil {
		t.Errorf("src must remain on disk after rejected migration, got %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, dstBody) {
		t.Errorf("dst must remain untouched, got %q err=%v", got, err)
	}
}

func TestRunMigrate_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")
	seedOldLayout(t, old)

	var buf1 bytes.Buffer
	if err := run(&buf1, []string{old, newRoot}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Re-running on the fully migrated tree should be a no-op.
	var buf2 bytes.Buffer
	if err := run(&buf2, []string{old, newRoot}); err != nil {
		t.Fatalf("second run (idempotent): %v\noutput:\n%s", err, buf2.String())
	}
	out := buf2.String()
	if !strings.Contains(out, "0 moved") {
		t.Errorf("second run should report 0 moved, got:\n%s", out)
	}
}

func TestRunMigrate_UnknownEntriesAreLeftInPlace(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// An unknown top-level file should NOT be moved or deleted.
	mystery := filepath.Join(old, "mystery.txt")
	if err := os.WriteFile(mystery, []byte("???"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	if err := run(&buf, []string{old, newRoot}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(mystery); err != nil {
		t.Errorf("unknown file %s should remain in place, got %v", mystery, err)
	}
	if !strings.Contains(buf.String(), "mystery.txt (unknown") {
		t.Errorf("expected warning about unknown file, got:\n%s", buf.String())
	}
}

func TestRunMigrate_RefusesConflictingDestination(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")
	if err := os.MkdirAll(filepath.Join(old, "loops", "2026-04-22"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, "loops", "2026-04-22", "18.jsonl"), []byte("source"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Pre-place a conflicting destination with different content.
	dst := filepath.Join(newRoot, "sources", "thane", "loops", "2026", "04", "22", "loops-2026-04-22-18.jsonl")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(dst, []byte("DIFFERENT CONTENT"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	err := run(&buf, []string{old, newRoot})
	if err == nil {
		t.Fatalf("expected error for conflicting destination, got nil\noutput:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "different size") {
		t.Errorf("expected conflict message, got:\n%s", buf.String())
	}
}

func TestRunMigrate_CopyModeLeavesSourceIntact(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")

	checksums := seedOldLayout(t, old)

	var buf bytes.Buffer
	if err := run(&buf, []string{"--copy", old, newRoot}); err != nil {
		t.Fatalf("run --copy: %v\noutput:\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "mode: copy") {
		t.Errorf("expected mode banner to say copy, got:\n%s", buf.String())
	}

	// Every original file still present with its original content.
	for srcRel, sum := range checksums {
		assertFileWithChecksum(t, filepath.Join(old, srcRel), sum)
	}
	// And the new tree has the same content under the new layout.
	wantDest := map[string]string{
		"loops/2026-04-22/18.jsonl":  "sources/thane/loops/2026/04/22/loops-2026-04-22-18.jsonl",
		"access/2026-05-26/05.jsonl": "sources/thane/http_access/2026/05/26/http_access-2026-05-26-05.jsonl",
		"thane.log":                  "sources/thane_legacy/thane.log",
		"logs.db":                    "logs.db",
	}
	for srcRel, dstRel := range wantDest {
		assertFileWithChecksum(t, filepath.Join(newRoot, dstRel), checksums[srcRel])
	}

	// Empty archive/ dir is preserved in copy mode (source untouched).
	if _, err := os.Stat(filepath.Join(old, "archive")); err != nil {
		t.Errorf("empty archive/ should be preserved in copy mode, got %v", err)
	}
}

func TestRunMigrate_CopyModeIdempotent(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "old")
	newRoot := filepath.Join(tmp, "new")
	seedOldLayout(t, old)

	var first bytes.Buffer
	if err := run(&first, []string{"--copy", old, newRoot}); err != nil {
		t.Fatalf("first --copy: %v", err)
	}
	var second bytes.Buffer
	if err := run(&second, []string{"--copy", old, newRoot}); err != nil {
		t.Fatalf("second --copy: %v\noutput:\n%s", err, second.String())
	}
	if !strings.Contains(second.String(), "0 moved") {
		t.Errorf("second --copy run should report 0 moved, got:\n%s", second.String())
	}
}
