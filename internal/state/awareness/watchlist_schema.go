package awareness

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// watchlistSchema declares the watched_entity_subscriptions table: the
// one subscription registry, keyed by (owner, entity_id). The legacy
// watched_entities migration was retired in #776; the scope→owner
// rename (#1209) retired the tag-scoped tier — the column names the
// owning loop ([OwnerCore] = the always-visible tier, [OwnerSystem] =
// runtime-seeded) instead of a capability-tag scope; #1208's closing
// steps below re-home the formerly anonymous tier onto core.
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
		// The former global tier (empty owner) belongs to core
		// (#1208's closing decision) — but its legacy rows are NOT
		// migrated: the owner chose to re-evaluate the subscription
		// set by hand rather than carry old choices forward
		// mechanically, so the anonymous tier is simply cleared.
		// Idempotent: once no empty-owner rows remain (the store
		// refuses to write them), this is a permanent no-op.
		database.Raw{
			Description: "clear the retired anonymous subscription tier",
			SQL:         `DELETE FROM watched_entity_subscriptions WHERE owner = ''`,
		},
	},
}
