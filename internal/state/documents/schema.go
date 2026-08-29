package documents

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the document-index tables. The whole index is derived
// state: every row is reproducible from the markdown on disk, which is
// what lets schema evolution here purge-and-rebuild instead of
// migrating data in place.
var schema = database.Schema{
	Name: "documents",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "indexed_documents",
			SQL: `CREATE TABLE IF NOT EXISTS indexed_documents (
				root TEXT NOT NULL,
				rel_path TEXT NOT NULL,
				abs_path TEXT NOT NULL,
				title TEXT NOT NULL DEFAULT '',
				summary TEXT NOT NULL DEFAULT '',
				facets_json TEXT NOT NULL DEFAULT '[]',
				facet_bytes_json TEXT,
				audience TEXT,
				tags_json TEXT NOT NULL DEFAULT '[]',
				frontmatter_json TEXT NOT NULL DEFAULT '{}',
				links_json TEXT NOT NULL DEFAULT '[]',
				modified_at TEXT NOT NULL,
				size_bytes INTEGER NOT NULL DEFAULT 0,
				word_count INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY(root, rel_path)
			)`,
		},
		database.IndexCreate{Name: "idx_indexed_documents_root_path", SQL: `CREATE INDEX IF NOT EXISTS idx_indexed_documents_root_path ON indexed_documents(root, rel_path)`},
		database.IndexCreate{Name: "idx_indexed_documents_modified", SQL: `CREATE INDEX IF NOT EXISTS idx_indexed_documents_modified ON indexed_documents(root, modified_at DESC)`},
		database.TableCreate{
			Table: "indexed_document_sections",
			SQL: `CREATE TABLE IF NOT EXISTS indexed_document_sections (
				root TEXT NOT NULL,
				rel_path TEXT NOT NULL,
				ordinal INTEGER NOT NULL,
				level INTEGER NOT NULL,
				heading TEXT NOT NULL,
				slug TEXT NOT NULL,
				start_line INTEGER NOT NULL DEFAULT 0,
				end_line INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY(root, rel_path, ordinal)
			)`,
		},
		database.IndexCreate{Name: "idx_indexed_document_sections_doc", SQL: `CREATE INDEX IF NOT EXISTS idx_indexed_document_sections_doc ON indexed_document_sections(root, rel_path, ordinal)`},
		// Additive upgrades for databases whose indexed_documents table
		// predates a column above. facets_json arrived with the #1250
		// facet ladder; facet_bytes_json and audience arrive with the
		// context-advertising groundwork (#1431).
		database.ColumnAdd{Table: "indexed_documents", Column: "facets_json", Typedef: "TEXT NOT NULL DEFAULT '[]'"},
		database.ColumnAdd{Table: "indexed_documents", Column: "facet_bytes_json", Typedef: "TEXT"},
		database.ColumnAdd{Table: "indexed_documents", Column: "audience", Typedef: "TEXT"},
		// Rebuild trigger for rows written before facet_bytes_json and
		// audience existed. The index re-parses a document only when its
		// mtime or size changes, so a pre-upgrade row would otherwise
		// carry NULLs indefinitely — and NULL facet_bytes_json means the
		// advertiser cannot cost a single facet, while NULL audience
		// falls on the excluded side of the SQL privacy gate, silently
		// hiding published documents. The index is derived data, so the
		// cure is deletion: purge the stale rows and let the background
		// refresher rebuild them from disk on its next pass.
		//
		// NULL facet_bytes_json is the marker for "row predates this
		// upgrade": every post-upgrade upsert writes the column, so the
		// step self-gates — it bites exactly once per pre-upgrade row and
		// no-ops on every later boot, without a schema probe that a
		// partially applied upgrade could fool. Sections are purged
		// first, while the marker rows that select them still exist.
		database.Raw{
			Description: "purge pre-facet_bytes_json index rows so the refresher rebuilds them",
			SQL: `DELETE FROM indexed_document_sections WHERE (root, rel_path) IN
					(SELECT root, rel_path FROM indexed_documents WHERE facet_bytes_json IS NULL);
				DELETE FROM indexed_documents WHERE facet_bytes_json IS NULL`,
		},
	},
}
