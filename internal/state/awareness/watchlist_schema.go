package awareness

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// watchlistSchema declares the watched_entity_subscriptions table. The
// legacy watched_entities migration was retired in #776 so this is now
// a forward-only schema.
var watchlistSchema = database.Schema{
	Name: "awareness/watchlist",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "watched_entity_subscriptions",
			SQL: `CREATE TABLE IF NOT EXISTS watched_entity_subscriptions (
				entity_id TEXT NOT NULL,
				scope     TEXT NOT NULL DEFAULT '',
				added_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				options   TEXT NOT NULL DEFAULT '{}',
				PRIMARY KEY (scope, entity_id)
			)`,
		},
	},
}
