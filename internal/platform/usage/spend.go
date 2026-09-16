package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SpendSnapshot contains adjacent 24-hour reports from one ledger snapshot.
// AsOf is the supplied anchor converted to UTC and truncated to whole seconds,
// matching persisted timestamp precision. Last24Hours covers [AsOf-24h, AsOf)
// and includes the three known loops with the highest recorded costs, with
// complete MatchedGroups and Truncated metadata. Previous24Hours covers
// [AsOf-48h, AsOf-24h) and contains totals only. Both reports retain complete
// pricing coverage and unattributed totals; no cost or ancestry is inferred.
type SpendSnapshot struct {
	AsOf            time.Time `json:"as_of"`
	Last24Hours     Report    `json:"last_24_hours"`
	Previous24Hours Report    `json:"previous_24_hours"`
}

// SpendSnapshot reads two adjacent 24-hour windows in a single read
// transaction, honoring caller cancellation. AsOf must be nonzero and is
// normalized as documented on [SpendSnapshot]. All costs are the values
// recorded at call time. Empty windows retain zero records and coverage
// counts; they do not establish that unreported work was free. Any query,
// decoding, or cancellation failure returns no partial snapshot.
func (s *Store) SpendSnapshot(ctx context.Context, asOf time.Time) (*SpendSnapshot, error) {
	if asOf.IsZero() {
		return nil, fmt.Errorf("spend snapshot requires a nonzero as_of")
	}
	asOf = asOf.UTC().Truncate(time.Second)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin spend snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, err := readReport(ctx, tx, ReportOptions{
		Start: asOf.Add(-24 * time.Hour), End: asOf, GroupBy: "loop", Limit: 3,
	}, "loop_id")
	if err != nil {
		return nil, fmt.Errorf("read last 24 hours: %w", err)
	}
	previous, err := readReport(ctx, tx, ReportOptions{
		Start: asOf.Add(-48 * time.Hour), End: asOf.Add(-24 * time.Hour),
	}, "")
	if err != nil {
		return nil, fmt.Errorf("read previous 24 hours: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish spend snapshot: %w", err)
	}
	return &SpendSnapshot{AsOf: asOf, Last24Hours: *current, Previous24Hours: *previous}, nil
}
