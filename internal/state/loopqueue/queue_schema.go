package loopqueue

import "github.com/nugget/thane-ai-agent/internal/platform/database"

// queueSchema declares the loop_queue table: a durable, deduped,
// per-consumer-loop work queue. Rows are partitioned by consumer_loop
// (the loop name that drains them) and deduplicated on dedup_key within
// that partition, so re-enqueuing the same subject coalesces instead of
// piling up. Forward-only schema (CREATE IF NOT EXISTS).
var queueSchema = database.Schema{
	Name: "loopqueue",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "loop_queue",
			SQL: `CREATE TABLE IF NOT EXISTS loop_queue (
				consumer_loop TEXT NOT NULL,
				dedup_key     TEXT NOT NULL,
				priority      INTEGER NOT NULL DEFAULT 0,
				status        TEXT NOT NULL DEFAULT 'pending',
				attempts      INTEGER NOT NULL DEFAULT 0,
				payload       TEXT NOT NULL DEFAULT '{}',
				enqueued_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (consumer_loop, dedup_key)
			)`,
		},
		database.IndexCreate{
			Name: "idx_loop_queue_drain",
			// Supports the per-partition drain query: pending items for
			// one consumer, priority-first then FIFO.
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_queue_drain
				ON loop_queue (consumer_loop, status, priority DESC, enqueued_at ASC)`,
		},
	},
}
