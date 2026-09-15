package usage_test

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/platform/usage"
)

func ExampleWithObserver() {
	ctx := usage.WithObserver(context.Background(), func(record usage.Record) {
		fmt.Printf("%s: %d output tokens\n", record.Model, record.OutputTokens)
	})
	usage.Observe(ctx, usage.Record{Model: "local-model", OutputTokens: 12})
	// Output: local-model: 12 output tokens
}
