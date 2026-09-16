package usage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

func ExampleStore_Report() {
	db, err := database.OpenMemory()
	if err != nil {
		panic(err)
	}
	defer db.Close()
	store, err := usage.NewStore(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	for _, record := range []usage.Record{
		{Timestamp: start, LoopID: "loop-1", LoopName: "reflection", CostUSD: 1.25, PricingStatus: "priced"},
		{Timestamp: start, CostUSD: 0.50}, // Retained history without a loop ID.
	} {
		if err := store.Record(ctx, record); err != nil {
			panic(err)
		}
	}
	report, err := store.Report(ctx, usage.ReportOptions{
		Start: start, End: start.Add(24 * time.Hour), GroupBy: "loop", Limit: 10,
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("Recorded total: $%.2f; unattributed: $%.2f\n", report.Summary.TotalCostUSD, report.Unattributed.TotalCostUSD)
	for _, group := range report.Groups {
		fmt.Printf("%s (%s): $%.2f\n", group.LoopName, group.Key, group.Summary.TotalCostUSD)
	}
	fmt.Printf("Groups: %d of %d; truncated: %t\n", len(report.Groups), report.MatchedGroups, report.Truncated)
	// Output:
	// Recorded total: $1.75; unattributed: $0.50
	// reflection (loop-1): $1.25
	// Groups: 1 of 1; truncated: false
}
