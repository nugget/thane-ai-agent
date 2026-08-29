package documents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// TestMigratePurgesRowsPredatingFacetBytesForRebuild pins the upgrade
// path for indexes built before facet_bytes_json and audience existed.
// The trap being simulated is specific: the refresher re-parses a
// document only when its mtime or size changes, so the seeded row
// matches the on-disk file exactly — the row an incremental refresh
// would consider fresh and never touch, leaving its new columns NULL
// forever. The migration must clear it (sections too) so the refresher
// rebuilds it from disk with both columns populated, and a second
// migrate must leave the rebuilt row alone.
func TestMigratePurgesRowsPredatingFacetBytesForRebuild(t *testing.T) {
	t.Parallel()

	kbDir := t.TempDir()
	docPath := filepath.Join(kbDir, "doc.md")
	writeFile(t, docPath, "---\naudience: published\n---\n\n# Doc\n\nBody text.\n\n## Section\n\nMore.\n")
	info, err := os.Stat(docPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// The table shape as it existed before this schema revision, seeded
	// with a row whose modified_at and size_bytes match the real file.
	preUpgrade := `
	CREATE TABLE indexed_documents (
		root TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		abs_path TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		facets_json TEXT NOT NULL DEFAULT '[]',
		tags_json TEXT NOT NULL DEFAULT '[]',
		frontmatter_json TEXT NOT NULL DEFAULT '{}',
		links_json TEXT NOT NULL DEFAULT '[]',
		modified_at TEXT NOT NULL,
		size_bytes INTEGER NOT NULL DEFAULT 0,
		word_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(root, rel_path)
	);
	CREATE TABLE indexed_document_sections (
		root TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		level INTEGER NOT NULL,
		heading TEXT NOT NULL,
		slug TEXT NOT NULL,
		start_line INTEGER NOT NULL DEFAULT 0,
		end_line INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(root, rel_path, ordinal)
	);`
	if _, err := db.Exec(preUpgrade); err != nil {
		t.Fatalf("create pre-upgrade shape: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO indexed_documents (root, rel_path, abs_path, title, modified_at, size_bytes) VALUES ('kb', 'doc.md', ?, 'Doc', ?, ?)`,
		docPath, info.ModTime().UTC().Format(time.RFC3339Nano), info.Size(),
	); err != nil {
		t.Fatalf("seed pre-upgrade row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO indexed_document_sections (root, rel_path, ordinal, level, heading, slug) VALUES ('kb', 'doc.md', 0, 1, 'Doc', 'doc')`,
	); err != nil {
		t.Fatalf("seed pre-upgrade section: %v", err)
	}

	store, err := NewStore(db, map[string]string{"kb": kbDir}, nil)
	if err != nil {
		t.Fatalf("NewStore (migrate): %v", err)
	}
	ctx := context.Background()

	countRows := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	if got := countRows("indexed_documents"); got != 0 {
		t.Fatalf("indexed_documents rows after migrate = %d, want 0 (pre-upgrade rows purged for rebuild)", got)
	}
	if got := countRows("indexed_document_sections"); got != 0 {
		t.Fatalf("indexed_document_sections rows after migrate = %d, want 0", got)
	}

	// The refresher rebuilds the cleared row from disk with the new
	// columns populated.
	if err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var facetBytesNull, audienceNull bool
	var facetBytesJSON, audience string
	if err := db.QueryRowContext(ctx,
		`SELECT facet_bytes_json IS NULL, audience IS NULL, COALESCE(facet_bytes_json, ''), COALESCE(audience, '')
		 FROM indexed_documents WHERE root = 'kb' AND rel_path = 'doc.md'`,
	).Scan(&facetBytesNull, &audienceNull, &facetBytesJSON, &audience); err != nil {
		t.Fatalf("rebuilt row missing: %v", err)
	}
	if facetBytesNull || audienceNull {
		t.Fatalf("rebuilt row still carries NULLs: facet_bytes_json NULL=%v audience NULL=%v", facetBytesNull, audienceNull)
	}
	if facetBytesJSON == "" || facetBytesJSON == "{}" {
		t.Errorf("facet_bytes_json = %q, want a measured full entry", facetBytesJSON)
	}
	if audience != "published" {
		t.Errorf("audience = %q, want published", audience)
	}
	if got := countRows("indexed_document_sections"); got == 0 {
		t.Errorf("sections not rebuilt after refresh")
	}

	// Idempotency: a later boot's migrate must not purge rebuilt rows —
	// the NULL marker is gone, so the purge step no-ops.
	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := countRows("indexed_documents"); got != 1 {
		t.Fatalf("indexed_documents rows after second migrate = %d, want 1 (purge must self-gate on the NULL marker)", got)
	}
}
