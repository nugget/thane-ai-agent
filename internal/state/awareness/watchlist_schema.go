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
		// The former global tier (owner '') belongs to core (#1208's
		// closing decision): core is the root container and every
		// context is de facto core's context, so the anonymous tier
		// was a second name for the same thing. UPDATE OR IGNORE
		// re-homes rows; a collision with an existing core row keeps
		// the core row and the DELETE clears the leftover. Both steps
		// are idempotent no-ops once no '' rows remain.
		database.Raw{
			Description: "re-home global-tier subscription rows onto core",
			SQL:         `UPDATE OR IGNORE watched_entity_subscriptions SET owner = 'core' WHERE owner = ''`,
		},
		database.Raw{
			Description: "drop global-tier rows that collided with core rows",
			SQL:         `DELETE FROM watched_entity_subscriptions WHERE owner = ''`,
		},
	},
}
