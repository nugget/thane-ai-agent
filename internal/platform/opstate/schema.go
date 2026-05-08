package opstate

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// schema declares the operational_state table plus the additive
// expires_at column from #457.
var schema = database.Schema{
	Name: "opstate",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "operational_state",
			SQL: `CREATE TABLE IF NOT EXISTS operational_state (
				namespace  TEXT NOT NULL,
				key        TEXT NOT NULL,
				value      TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				PRIMARY KEY (namespace, key)
			)`,
		},
		database.ColumnAdd{Table: "operational_state", Column: "expires_at", Typedef: "TEXT"},
	},
}
