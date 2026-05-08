package knowledge

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the knowledge facts table and its additive history.
// FTS5 is set up separately in tryEnableFTS — it's allowed to fail
// (graceful LIKE fallback) and so does not belong in the schema.
var schema = database.Schema{
	Name: "knowledge",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "facts",
			SQL: `CREATE TABLE IF NOT EXISTS facts (
				id TEXT PRIMARY KEY,
				category TEXT NOT NULL,
				key TEXT NOT NULL,
				value TEXT NOT NULL,
				source TEXT,
				confidence REAL DEFAULT 1.0,
				embedding BLOB,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				accessed_at TEXT NOT NULL,
				deleted_at TEXT,
				UNIQUE(category, key)
			)`,
		},
		database.IndexCreate{
			Name: "idx_facts_category",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_facts_category ON facts(category)`,
		},
		database.IndexCreate{
			Name: "idx_facts_key",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_facts_key ON facts(key)`,
		},
		database.IndexCreate{
			Name: "idx_facts_accessed",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_facts_accessed ON facts(accessed_at DESC)`,
		},
		database.IndexCreate{
			Name: "idx_facts_deleted",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_facts_deleted ON facts(deleted_at)`,
		},
		// Additive columns for facts that pre-date the latest schema.
		database.ColumnAdd{Table: "facts", Column: "embedding", Typedef: "BLOB"},
		database.ColumnAdd{Table: "facts", Column: "deleted_at", Typedef: "TEXT"},
		database.ColumnAdd{Table: "facts", Column: "subjects", Typedef: "TEXT"},
		database.ColumnAdd{Table: "facts", Column: "ref", Typedef: "TEXT"},
	},
}
