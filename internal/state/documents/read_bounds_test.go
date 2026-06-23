package documents

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadDocumentBytes_RefusesOversize is the regression guard for the
// 2026-06 incident: a loop output document grew to 8 GB and reading it whole
// OOM-crashed the host. readDocumentBytes must refuse oversized files (by
// stat, before allocating) while reading normal documents unchanged.
func TestReadDocumentBytes_RefusesOversize(t *testing.T) {
	dir := t.TempDir()

	small := filepath.Join(dir, "small.md")
	if err := os.WriteFile(small, []byte("# hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readDocumentBytes(small)
	if err != nil {
		t.Fatalf("normal document read failed: %v", err)
	}
	if string(got) != "# hello\n" {
		t.Fatalf("normal document content = %q, want %q", got, "# hello\n")
	}

	// A sparse file one byte over the cap: Stat reports the apparent size, so
	// the guard trips without writing or allocating the full size.
	big := filepath.Join(dir, "big.md")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxReadableDocumentBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readDocumentBytes(big); err == nil {
		t.Fatalf("expected oversize document to be refused, got nil error")
	}
}
