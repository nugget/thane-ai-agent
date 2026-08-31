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
		database.TableCreate{
			Table: "loop_queue_completions",
			// The audit trail Ack would otherwise erase: one row per
			// acknowledged item, written in the same transaction as the
			// DELETE, so consumer throughput, wait latency, and recent
			// outcomes are queryable facts instead of log archaeology.
			// A coalescing re-Enqueue is deliberately NOT a completion —
			// it restates the same pending work in place. Rows are
			// pruned by age via [Store.PruneCompletions].
			//
			// The priority column is vestigial: early builds journaled
			// each item's priority but no consumer ever read it back, so
			// Ack stopped recording it. The column stays because the
			// table already exists in production (forward-only schema);
			// new rows take its DEFAULT 0.
			SQL: `CREATE TABLE IF NOT EXISTS loop_queue_completions (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				consumer_loop TEXT NOT NULL,
				dedup_key     TEXT NOT NULL,
				priority      INTEGER NOT NULL DEFAULT 0,
				enqueued_at   TIMESTAMP NOT NULL,
				acked_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
		},
		database.IndexCreate{
			Name: "idx_loop_queue_completions_consumer",
			// Supports per-consumer recent-completions queries.
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_queue_completions_consumer
				ON loop_queue_completions (consumer_loop, acked_at DESC)`,
		},
		database.IndexCreate{
			Name: "idx_loop_queue_completions_acked",
			// Leads with acked_at for the queries that filter on the
			// window alone — the daily prune and the cross-consumer
			// completion stats — which the consumer-led index above
			// cannot serve.
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_queue_completions_acked
				ON loop_queue_completions (acked_at)`,
		},
		database.IndexCreate{
			Name: "idx_loop_queue_completions_key",
			// Supports producer overlap checks before admitting a historical
			// backfill key that the consumer recently completed.
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_queue_completions_key
				ON loop_queue_completions (consumer_loop, dedup_key)`,
		},
	},
}
