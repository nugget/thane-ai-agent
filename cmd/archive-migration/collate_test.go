package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGz is a test helper: gzipped plain-text file with the given lines.
func writeGz(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("setup create %s: %v", path, err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	for _, line := range lines {
		gz.Write([]byte(line + "\n"))
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("setup gz close: %v", err)
	}
}

// writeTarGz is a test helper: a tar.gz containing a single thane.log
// entry with the given lines.
func writeTarGz(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("setup create %s: %v", path, err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	var body bytes.Buffer
	for _, line := range lines {
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "thane.log",
		Mode:     0o644,
		Size:     int64(body.Len()),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body.Bytes()); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
}

// readGzLines is a test helper: returns the newline-split contents of
// a gzipped file as a slice (no trailing empty strings).
func readGzLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	defer gz.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(gz); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	return out
}

func TestExtractTimestamp_ParsesBothFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		line        string
		wantUTCDate string
		wantOK      bool
	}{
		{
			name:        "json with offset",
			line:        `{"time":"2026-04-22T13:57:13.316346-05:00","level":"DEBUG","msg":"closing"}`,
			wantUTCDate: "2026-04-22",
			wantOK:      true,
		},
		{
			name:        "logfmt with offset",
			line:        `time=2026-02-15T21:49:29.802-06:00 level=INFO msg="starting Thane"`,
			wantUTCDate: "2026-02-16", // 21:49 CST = 03:49 next day UTC
			wantOK:      true,
		},
		{
			name:        "json UTC zulu",
			line:        `{"time":"2026-05-01T12:00:00.000Z","level":"INFO"}`,
			wantUTCDate: "2026-05-01",
			wantOK:      true,
		},
		{
			name:   "unparseable line",
			line:   `some random text without a timestamp`,
			wantOK: false,
		},
		{
			name:   "malformed time value",
			line:   `{"time":"not-a-real-timestamp","level":"INFO"}`,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, ok := extractTimestamp(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (ts=%v)", ok, tt.wantOK, ts)
			}
			if !ok {
				return
			}
			got := ts.UTC().Format("2006-01-02")
			if got != tt.wantUTCDate {
				t.Errorf("UTC date = %q, want %q", got, tt.wantUTCDate)
			}
		})
	}
}

func TestRunLegacyCollate_DedupesAndBuckets(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, "sources", "thane_legacy")

	// Two sources with overlapping content. Three unique events across
	// two UTC dates, plus one duplicate.
	thaneLog := `{"time":"2026-04-22T00:30:00.000-05:00","level":"INFO","msg":"a"}` // UTC: 2026-04-22
	tarballA := `time=2026-02-15T21:49:29.802-06:00 level=INFO msg="b"`             // UTC: 2026-02-16
	tarballB := `time=2026-02-16T05:00:00.000+00:00 level=INFO msg="c"`             // UTC: 2026-02-16
	// the duplicate — thaneLog and the existing gz both contain it
	dup := `{"time":"2026-04-22T01:00:00.000-05:00","level":"INFO","msg":"shared"}`

	// thane.log holds two records (one unique to it, one duplicated below).
	thanePath := filepath.Join(legacy, "thane.log")
	if err := os.MkdirAll(filepath.Dir(thanePath), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(thanePath, []byte(thaneLog+"\n"+dup+"\n"), 0o644); err != nil {
		t.Fatalf("setup thane.log: %v", err)
	}

	// Two tarballs covering the Feb 2026 era. One legacy logfmt line each.
	writeTarGz(t, filepath.Join(legacy, "original-thane-log.tar.gz"), tarballA)
	writeTarGz(t, filepath.Join(legacy, "final-thane-log.tar.gz"), tarballA) // identical content → dup

	// A daily gz containing the dup again, plus another record.
	writeGz(t, filepath.Join(legacy, "thane-2026-02-16.log.gz"), tarballB, dup)

	var buf bytes.Buffer
	if err := runLegacyCollate(&buf, []string{tmp}); err != nil {
		t.Fatalf("runLegacyCollate: %v\n%s", err, buf.String())
	}

	// Expect two daily files: 2026-02-16 and 2026-04-22.
	files, err := filepath.Glob(filepath.Join(legacy, "thane-*.log.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("daily files count = %d, want 2 — files: %v\noutput:\n%s", len(files), files, buf.String())
	}

	feb := readGzLines(t, filepath.Join(legacy, "thane-2026-02-16.log.gz"))
	apr := readGzLines(t, filepath.Join(legacy, "thane-2026-04-22.log.gz"))

	// Feb bucket: the two tarballs share content (dedup'd) + the daily-gz
	// added record. Two unique lines.
	if len(feb) != 2 {
		t.Errorf("Feb bucket lines = %d, want 2 — got:\n%s", len(feb), strings.Join(feb, "\n"))
	}
	// Apr bucket: the original record + the shared "dup" record. Two unique lines.
	if len(apr) != 2 {
		t.Errorf("Apr bucket lines = %d, want 2 — got:\n%s", len(apr), strings.Join(apr, "\n"))
	}

	// Originals moved into collated-sources/.
	consumed := filepath.Join(legacy, "collated-sources")
	consumedEntries, err := os.ReadDir(consumed)
	if err != nil {
		t.Fatalf("read collated-sources: %v", err)
	}
	if len(consumedEntries) != 4 {
		t.Errorf("collated-sources entries = %d, want 4 (thane.log + 2 tarballs + 1 daily gz)", len(consumedEntries))
	}
	// thane.log no longer exists at its old path.
	if _, err := os.Stat(thanePath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("thane.log should have moved, stat err = %v", err)
	}

	// Stats line in the summary reflects deduplication.
	if !strings.Contains(buf.String(), "duplicates dropped") {
		t.Errorf("summary missing dedup count; output:\n%s", buf.String())
	}
}

func TestRunLegacyCollate_RefusesIfAlreadyCollated(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, "sources", "thane_legacy", "collated-sources")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	err := runLegacyCollate(&buf, []string{tmp})
	if err == nil {
		t.Fatal("expected refusal when collated-sources/ already exists, got nil")
	}
	if !strings.Contains(err.Error(), "collated-sources") {
		t.Errorf("error should mention collated-sources, got: %v", err)
	}
}

func TestRunLegacyCollate_UnparseableTimestampsGoToUnknownBucket(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, "sources", "thane_legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "thane.log"), []byte("no timestamp here\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	if err := runLegacyCollate(&buf, []string{tmp}); err != nil {
		t.Fatalf("runLegacyCollate: %v\n%s", err, buf.String())
	}
	unknown := filepath.Join(legacy, "thane-unknown.log.gz")
	if _, err := os.Stat(unknown); err != nil {
		t.Errorf("thane-unknown.log.gz should exist: %v", err)
	}
}

func TestRunLegacyCollate_EmptyLegacyDirNoOp(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, "sources", "thane_legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var buf bytes.Buffer
	if err := runLegacyCollate(&buf, []string{tmp}); err != nil {
		t.Fatalf("runLegacyCollate empty: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "no legacy sources") {
		t.Errorf("expected 'no legacy sources' message, got:\n%s", buf.String())
	}
}
