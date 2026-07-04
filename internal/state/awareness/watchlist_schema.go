package awareness

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// watchlistSchema declares the watched_entity_subscriptions table: the
// one subscription registry, keyed by (owner, entity_id). The legacy
// watched_entities migration was retired in #776; the scope→owner
// rename (#1209) retired the tag-scoped tier — the column now names
// the owning loop (” = always-visible, [OwnerSystem] = runtime-seeded)
// instead of a capability-tag scope.
var watchlistSchema = database.Schema{
	Name: "awareness/watchlist",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "watched_entity_subscriptions",
			SQL: `CREATE TABLE IF NOT EXISTS watched_entity_subscriptions (
				entity_id TEXT NOT NULL,
				owner     TEXT NOT NULL DEFAULT '',
				added_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				options   TEXT NOT NULL DEFAULT '{}',
				PRIMARY KEY (owner, entity_id)
			)`,
		},
		// Pre-#1209 databases created the owner column as "scope"; the
		// rename is a no-op on fresh databases and on every run after
		// the first.
		database.ColumnRename{Table: "watched_entity_subscriptions", From: "scope", To: "owner"},
	},
}
