package attachments

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the attachments metadata table plus its additive
// history (the received_at index added in phase 4 and the vision
// analysis columns added in phase 3).
var schema = database.Schema{
	Name: "attachments",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "attachments",
			SQL: `CREATE TABLE IF NOT EXISTS attachments (
				id TEXT PRIMARY KEY,
				hash TEXT NOT NULL,
				store_path TEXT NOT NULL,
				original_name TEXT NOT NULL DEFAULT '',
				content_type TEXT NOT NULL DEFAULT '',
				size INTEGER NOT NULL DEFAULT 0,
				width INTEGER NOT NULL DEFAULT 0,
				height INTEGER NOT NULL DEFAULT 0,
				channel TEXT NOT NULL DEFAULT '',
				sender TEXT NOT NULL DEFAULT '',
				conversation_id TEXT NOT NULL DEFAULT '',
				received_at TEXT NOT NULL
			)`,
		},
		database.IndexCreate{Name: "idx_attachments_hash", SQL: `CREATE INDEX IF NOT EXISTS idx_attachments_hash ON attachments(hash)`},
		database.IndexCreate{Name: "idx_attachments_conversation", SQL: `CREATE INDEX IF NOT EXISTS idx_attachments_conversation ON attachments(conversation_id)`},
		database.IndexCreate{Name: "idx_attachments_channel_sender", SQL: `CREATE INDEX IF NOT EXISTS idx_attachments_channel_sender ON attachments(channel, sender)`},
		database.IndexCreate{Name: "idx_attachments_received_at", SQL: `CREATE INDEX IF NOT EXISTS idx_attachments_received_at ON attachments(received_at)`},
		// Vision analysis columns (added in phase 3).
		database.ColumnAdd{Table: "attachments", Column: "description", Typedef: "TEXT NOT NULL DEFAULT ''"},
		database.ColumnAdd{Table: "attachments", Column: "analyzed_at", Typedef: "TEXT NOT NULL DEFAULT ''"},
		database.ColumnAdd{Table: "attachments", Column: "analysis_model", Typedef: "TEXT NOT NULL DEFAULT ''"},
	},
}
