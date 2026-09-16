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

func ExampleStore_SpendSnapshot() {
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
	asOf := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if err := store.Record(ctx, usage.Record{
		Timestamp: asOf.Add(-time.Hour), LoopID: "loop-1", LoopName: "reflection",
		CostUSD: 2.50, PricingStatus: "priced",
	}); err != nil {
		panic(err)
	}
	snapshot, err := store.SpendSnapshot(ctx, asOf)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Last 24 hours: $%.2f from %d recorded calls\n",
		snapshot.Last24Hours.Summary.TotalCostUSD, snapshot.Last24Hours.Summary.TotalRecords)
	fmt.Printf("Previous 24 hours: %d recorded calls\n", snapshot.Previous24Hours.Summary.TotalRecords)
	fmt.Printf("Top loops: %d of %d\n", len(snapshot.Last24Hours.Groups), snapshot.Last24Hours.MatchedGroups)
	// Output:
	// Last 24 hours: $2.50 from 1 recorded calls
	// Previous 24 hours: 0 recorded calls
	// Top loops: 1 of 1
}
