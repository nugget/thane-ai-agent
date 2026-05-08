package usage

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the usage_records table plus its additive history.
// Each ColumnAdd captures a column added in a later release; the
// IF-NOT-EXISTS / no-op behavior of ColumnAdd makes the sequence safe
// to run on both fresh and pre-existing databases.
var schema = database.Schema{
	Name: "usage",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "usage_records",
			SQL: `CREATE TABLE IF NOT EXISTS usage_records (
				id              TEXT PRIMARY KEY,
				timestamp       TEXT NOT NULL,
				request_id      TEXT NOT NULL,
				session_id      TEXT,
				conversation_id TEXT,
				model           TEXT NOT NULL,
				provider        TEXT NOT NULL,
				input_tokens    INTEGER NOT NULL,
				output_tokens   INTEGER NOT NULL,
				cost_usd        REAL NOT NULL,
				role            TEXT NOT NULL,
				task_name       TEXT
			)`,
		},
		database.IndexCreate{
			Name: "idx_usage_timestamp",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_usage_timestamp ON usage_records(timestamp)`,
		},
		database.IndexCreate{
			Name: "idx_usage_session",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_records(session_id)`,
		},
		database.IndexCreate{
			Name: "idx_usage_conversation",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_usage_conversation ON usage_records(conversation_id)`,
		},
		database.ColumnAdd{Table: "usage_records", Column: "upstream_model", Typedef: "TEXT NOT NULL DEFAULT ''"},
		database.ColumnAdd{Table: "usage_records", Column: "resource", Typedef: "TEXT NOT NULL DEFAULT ''"},
		database.ColumnAdd{Table: "usage_records", Column: "cache_creation_input_tokens", Typedef: "INTEGER NOT NULL DEFAULT 0"},
		database.ColumnAdd{Table: "usage_records", Column: "cache_read_input_tokens", Typedef: "INTEGER NOT NULL DEFAULT 0"},
		// Per-TTL cache-write breakdown columns, added for #736. Rows
		// written before this migration ran have NULL/0 in both buckets;
		// ComputeDetailedCostForIdentity treats that as "unknown mix" and
		// falls back to the 5m multiplier on the full total so historical
		// cost numbers don't spike retroactively.
		database.ColumnAdd{Table: "usage_records", Column: "cache_creation_5m_input_tokens", Typedef: "INTEGER NOT NULL DEFAULT 0"},
		database.ColumnAdd{Table: "usage_records", Column: "cache_creation_1h_input_tokens", Typedef: "INTEGER NOT NULL DEFAULT 0"},
		// upstream_request_id captures Anthropic's `x-request-id` response
		// header (and any equivalent from future providers) for billing
		// correlation. Rows from before this migration have "" — fine,
		// the column is informational and never participates in joins.
		database.ColumnAdd{Table: "usage_records", Column: "upstream_request_id", Typedef: "TEXT NOT NULL DEFAULT ''"},
	},
}
