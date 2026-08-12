package introspection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

// DefaultLoopEventRetention bounds the journal by age when the logging
// retention config does not. Thirty days covers month-scale incident
// retrospectives; the journal is operational history, not an archive.
const DefaultLoopEventRetention = 30 * 24 * time.Hour

// KindThaneBoot is the journal-native row recorded once per process
// start, stamped with the running version and commit. It is written by
// the wiring, not received from the bus: the bus has no process-level
// lifecycle event, and the boot record is what makes deploy boundaries
// and restart storms mechanically computable instead of model-inferred.
const KindThaneBoot = "thane_boot"

// loopEventsSchema declares the loop_events journal: the persistent
// record of loop lifecycle events (wakes with attribution, iteration
// outcomes, errors, state changes, mailbox arrivals) that the in-memory
// event bus otherwise forgets at restart. Forward-only
// (CREATE IF NOT EXISTS); rows are pruned by age.
var loopEventsSchema = database.Schema{
	Name: "introspection",
	Steps: []database.MigrationStep{
		database.TableCreate{
			Table: "loop_events",
			SQL: `CREATE TABLE IF NOT EXISTS loop_events (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				at          TIMESTAMP NOT NULL,
				loop_id     TEXT NOT NULL DEFAULT '',
				loop_name   TEXT NOT NULL DEFAULT '',
				kind        TEXT NOT NULL,
				wake_reason TEXT NOT NULL DEFAULT '',
				wake_source TEXT NOT NULL DEFAULT '',
				detail      TEXT NOT NULL DEFAULT '{}'
			)`,
		},
		database.IndexCreate{
			Name: "idx_loop_events_at",
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_events_at
				ON loop_events (at)`,
		},
		database.IndexCreate{
			Name: "idx_loop_events_loop",
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_events_loop
				ON loop_events (loop_name, at)`,
		},
		database.IndexCreate{
			Name: "idx_loop_events_kind",
			SQL: `CREATE INDEX IF NOT EXISTS idx_loop_events_kind
				ON loop_events (kind, at)`,
		},
	},
}

// LoopEvent is one journaled loop lifecycle event. WakeReason and
// WakeSource are populated on loop_iteration_start rows; Detail is the
// bounded, whitelisted projection of the bus event's payload.
type LoopEvent struct {
	At         time.Time
	LoopID     string
	LoopName   string
	Kind       string
	WakeReason string
	WakeSource string
	Detail     map[string]any
}

// Journal is the durable store for loop lifecycle events, backed by a
// table on logs.db next to the log index it complements.
type Journal struct {
	db *sql.DB
}

// NewJournal migrates the loop_events schema and returns the journal.
func NewJournal(db *sql.DB, logger *slog.Logger) (*Journal, error) {
	if db == nil {
		return nil, fmt.Errorf("introspection: journal requires a database")
	}
	if err := database.Migrate(db, loopEventsSchema, logger); err != nil {
		return nil, err
	}
	return &Journal{db: db}, nil
}

// record inserts a batch of events in one transaction. Called by the
// recorder's flush; not exported because producers go through the bus,
// never write the journal directly.
func (j *Journal) record(ctx context.Context, events []LoopEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("introspection: record loop events: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO loop_events (at, loop_id, loop_name, kind, wake_reason, wake_source, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("introspection: record loop events: %w", err)
	}
	defer stmt.Close()

	for _, ev := range events {
		detail := "{}"
		if len(ev.Detail) > 0 {
			if blob, err := json.Marshal(ev.Detail); err == nil {
				detail = string(blob)
			}
		}
		if _, err := stmt.ExecContext(ctx,
			ev.At.UTC(), ev.LoopID, ev.LoopName, ev.Kind, ev.WakeReason, ev.WakeSource, detail,
		); err != nil {
			return fmt.Errorf("introspection: record loop event %s/%s: %w", ev.LoopName, ev.Kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("introspection: record loop events: %w", err)
	}
	return nil
}

// LoopEventQuery filters the journal. Zero-valued fields are
// unrestricted; Limit <= 0 defaults to 50 and caps at 200 (matching the
// logs_query convention).
type LoopEventQuery struct {
	LoopName string
	Kind     string
	Since    time.Time
	Until    time.Time
	Limit    int
}

const (
	defaultLoopEventLimit = 50
	maxLoopEventLimit     = 200
)

// Query returns journaled events matching q in chronological order.
// The newest matching rows win when the limit clips (newest-first
// select, reversed), so a capped result covers the most recent window
// rather than a stale prefix.
func (j *Journal) Query(ctx context.Context, q LoopEventQuery) ([]LoopEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLoopEventLimit
	}
	if limit > maxLoopEventLimit {
		limit = maxLoopEventLimit
	}

	query := `
		SELECT at, loop_id, loop_name, kind, wake_reason, wake_source, detail
		FROM loop_events
		WHERE 1=1`
	var args []any
	if name := strings.TrimSpace(q.LoopName); name != "" {
		query += ` AND loop_name = ?`
		args = append(args, name)
	}
	if kind := strings.TrimSpace(q.Kind); kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	if !q.Since.IsZero() {
		query += ` AND at >= ?`
		args = append(args, q.Since.UTC())
	}
	if !q.Until.IsZero() {
		query += ` AND at <= ?`
		args = append(args, q.Until.UTC())
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("introspection: query loop events: %w", err)
	}
	defer rows.Close()

	var out []LoopEvent
	for rows.Next() {
		var (
			ev     LoopEvent
			atRaw  any
			detail string
		)
		if err := rows.Scan(&atRaw, &ev.LoopID, &ev.LoopName, &ev.Kind, &ev.WakeReason, &ev.WakeSource, &detail); err != nil {
			return nil, err
		}
		ev.At = parseJournalTimestamp(atRaw)
		if detail != "" && detail != "{}" {
			var m map[string]any
			if err := json.Unmarshal([]byte(detail), &m); err == nil {
				ev.Detail = m
			}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse newest-first to chronological so callers read a timeline.
	for i, jj := 0, len(out)-1; i < jj; i, jj = i+1, jj-1 {
		out[i], out[jj] = out[jj], out[i]
	}
	return out, nil
}

// LoopActivityAggregate summarizes a window of journaled events for one
// loop (or every loop when unscoped): wake volume and rate, the
// wake-reason and wake-source decomposition, and outcome counts.
type LoopActivityAggregate struct {
	Wakes        int
	WakesPerHour float64
	ByReason     map[string]int
	BySource     map[string]int
	Errors       int
	Completions  int
	// NoOps is the subset of Completions whose iteration changed
	// nothing — every no-op is already counted there, so summing the
	// two double-counts. It rides beside Completions because the ratio
	// is the signal: a loop completing every wake while changing
	// nothing is going through the motions.
	NoOps int
}

// maxAggregateSources caps the by-source decomposition; sources beyond
// the cap fold into an explicit "(other)" bucket rather than vanishing.
const maxAggregateSources = 10

// AggregateActivity computes the activity rollup for loopName (empty =
// all loops) between since and until (zero until = now). All counting
// runs in SQL; the caller never re-derives rates.
func (j *Journal) AggregateActivity(ctx context.Context, loopName string, since, until time.Time) (LoopActivityAggregate, error) {
	if until.IsZero() {
		until = time.Now().UTC()
	}
	agg := LoopActivityAggregate{}

	scope := ` AND at >= ? AND at <= ?`
	scopeArgs := []any{since.UTC(), until.UTC()}
	loopName = strings.TrimSpace(loopName)
	if loopName != "" {
		scope += ` AND loop_name = ?`
		scopeArgs = append(scopeArgs, loopName)
	}

	countRow := func(where string, args ...any) (int, error) {
		var n int
		err := j.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM loop_events WHERE `+where+scope,
			append(args, scopeArgs...)...,
		).Scan(&n)
		return n, err
	}

	var err error
	if agg.Wakes, err = countRow(`kind = ?`, events.KindLoopIterationStart); err != nil {
		return agg, fmt.Errorf("introspection: aggregate wakes: %w", err)
	}
	if agg.Errors, err = countRow(`kind = ?`, events.KindLoopError); err != nil {
		return agg, fmt.Errorf("introspection: aggregate errors: %w", err)
	}
	if agg.Completions, err = countRow(`kind = ?`, events.KindLoopIterationComplete); err != nil {
		return agg, fmt.Errorf("introspection: aggregate completions: %w", err)
	}
	if agg.NoOps, err = countRow(`kind = ? AND json_extract(detail, '$.no_op') = 1`, events.KindLoopIterationComplete); err != nil {
		return agg, fmt.Errorf("introspection: aggregate no-ops: %w", err)
	}

	if hours := until.Sub(since).Hours(); hours > 0 {
		agg.WakesPerHour = float64(agg.Wakes) / hours
	}

	groupCount := func(column string) (map[string]int, error) {
		rows, err := j.db.QueryContext(ctx, `
			SELECT `+column+`, COUNT(*) AS n
			FROM loop_events
			WHERE kind = ? AND `+column+` != ''`+scope+`
			GROUP BY `+column+`
			ORDER BY n DESC, `+column+` ASC
		`, append([]any{events.KindLoopIterationStart}, scopeArgs...)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var counts map[string]int
		for rows.Next() {
			var (
				key string
				n   int
			)
			if err := rows.Scan(&key, &n); err != nil {
				return nil, err
			}
			if counts == nil {
				counts = make(map[string]int)
			}
			if len(counts) >= maxAggregateSources {
				counts["(other)"] += n
				continue
			}
			counts[key] += n
		}
		return counts, rows.Err()
	}

	if agg.ByReason, err = groupCount("wake_reason"); err != nil {
		return agg, fmt.Errorf("introspection: aggregate wake reasons: %w", err)
	}
	if agg.BySource, err = groupCount("wake_source"); err != nil {
		return agg, fmt.Errorf("introspection: aggregate wake sources: %w", err)
	}
	return agg, nil
}

// BootRecord is one journaled process start: when, and what build.
type BootRecord struct {
	At      time.Time
	Version string
	Commit  string
}

// RecordBoot journals one process start with its build identity.
func (j *Journal) RecordBoot(ctx context.Context, version, commit string) error {
	return j.recordBootAt(ctx, time.Now(), version, commit)
}

// recordBootAt persists the boot row with an explicit instant, so a
// retried record carries the time the process started rather than the
// time the write finally won the lock — that instant feeds the deploy
// boundary, the boot timeline, and the 24h count.
func (j *Journal) recordBootAt(ctx context.Context, at time.Time, version, commit string) error {
	return j.record(ctx, []LoopEvent{{
		At:   at,
		Kind: KindThaneBoot,
		Detail: map[string]any{
			"version": version,
			"commit":  commit,
		},
	}})
}

// Boot-record retry pacing. Boot is the single worst moment to write to
// logs.db — the log indexer is absorbing the startup burst, and on a
// large database that contention can outlast the connection's 5s busy
// timeout (observed in production on first deploy: SQLITE_BUSY on the
// very row this journal exists to guarantee). Each failed attempt can
// itself block for the busy timeout before the 3s wait, so the window
// bounds the goroutine's true lifetime where the attempt count alone
// would not.
const (
	bootRecordAttempts = 20
	bootRecordSpacing  = 3 * time.Second
	bootRecordWindow   = 3 * time.Minute
)

// RecordBootWithRetry journals the boot record from its own goroutine,
// out-stubborning the startup write burst: it retries until the row
// lands, the retry window closes, or the attempt budget is spent —
// either exhaustion is warned, because a missing boot row silently
// breaks deploy detection, so giving up must be loud. The recorded
// instant is captured before the first attempt, so a late-landing row
// still says when the process started. Called once from the app wiring
// as the recorder comes up.
func (j *Journal) RecordBootWithRetry(ctx context.Context, version, commit string, logger *slog.Logger) {
	go func() {
		retryCtx, cancel := context.WithTimeout(ctx, bootRecordWindow)
		defer cancel()
		j.recordBootRetrying(retryCtx, version, commit, logger, bootRecordAttempts, bootRecordSpacing)
	}()
}

// recordBootRetrying is the synchronous body of RecordBootWithRetry,
// with pacing injectable for tests. A deadline on ctx is the retry
// window and its exhaustion warns; a plain cancellation is process
// shutdown and exits quietly.
func (j *Journal) recordBootRetrying(ctx context.Context, version, commit string, logger *slog.Logger, attempts int, spacing time.Duration) {
	if logger == nil {
		logger = slog.Default()
	}
	bootAt := time.Now()
	surrender := func(lastErr error, attempt int) {
		logger.Warn("boot record failed after retries; deploy detection will miss this boot",
			"attempts", attempt, "error", lastErr)
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := j.recordBootAt(ctx, bootAt, version, commit); err == nil {
			if attempt > 1 {
				logger.Info("boot record landed after retries", "attempts", attempt)
			}
			return
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				surrender(lastErr, attempt)
			}
			return
		case <-time.After(spacing):
		}
	}
	surrender(lastErr, attempts)
}

// RecentBoots returns the newest boot records, newest first, bounded by
// limit (<= 0 defaults to 50). The walk from the newest row backward is
// what turns raw boots into a deploy boundary: the first row whose
// version differs from the running one marks where the current version
// began.
func (j *Journal) RecentBoots(ctx context.Context, limit int) ([]BootRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := j.db.QueryContext(ctx, `
		SELECT at, detail FROM loop_events
		WHERE kind = ?
		ORDER BY at DESC, id DESC LIMIT ?
	`, KindThaneBoot, limit)
	if err != nil {
		return nil, fmt.Errorf("introspection: recent boots: %w", err)
	}
	defer rows.Close()

	var boots []BootRecord
	for rows.Next() {
		var (
			atRaw  any
			detail string
		)
		if err := rows.Scan(&atRaw, &detail); err != nil {
			return nil, err
		}
		rec := BootRecord{At: parseJournalTimestamp(atRaw)}
		var m map[string]any
		if err := json.Unmarshal([]byte(detail), &m); err == nil {
			rec.Version, _ = m["version"].(string)
			rec.Commit, _ = m["commit"].(string)
		}
		boots = append(boots, rec)
	}
	return boots, rows.Err()
}

// Prune deletes journaled events older than maxAge and reports how many
// rows were removed. Deliberately pure age-based — unlike the log
// index's level-gated prune, every journal row expires.
func (j *Journal) Prune(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("introspection: prune requires a positive max age")
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := j.db.ExecContext(ctx, `DELETE FROM loop_events WHERE at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("introspection: prune loop events: %w", err)
	}
	return res.RowsAffected()
}

func parseJournalTimestamp(raw any) time.Time {
	switch v := raw.(type) {
	case time.Time:
		return v
	case string:
		if ts, err := database.ParseTimestamp(v); err == nil {
			return ts
		}
	case []byte:
		if ts, err := database.ParseTimestamp(string(v)); err == nil {
			return ts
		}
	}
	return time.Time{}
}
