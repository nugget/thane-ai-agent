package checkpoint

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the checkpoint store's persistent shape.
var schema = database.Schema{
	Name: "checkpoint",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "checkpoints",
			SQL: `CREATE TABLE IF NOT EXISTS checkpoints (
				id TEXT PRIMARY KEY,
				created_at TEXT NOT NULL,
				trigger TEXT NOT NULL,
				note TEXT,
				state_gz BLOB NOT NULL,
				byte_size INTEGER NOT NULL,
				message_count INTEGER NOT NULL,
				fact_count INTEGER NOT NULL
			)`,
		},
		database.IndexCreate{
			Name: "idx_checkpoints_created",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_checkpoints_created ON checkpoints(created_at DESC)`,
		},
		database.IndexCreate{
			Name: "idx_checkpoints_trigger",
			SQL:  `CREATE INDEX IF NOT EXISTS idx_checkpoints_trigger ON checkpoints(trigger)`,
		},
	},
}
