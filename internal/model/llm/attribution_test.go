package llm_test

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func ExampleWithAttribution() {
	ctx := llm.WithAttribution(context.Background(), llm.Attribution{LoopID: "loop-123", LoopName: "reflection"})
	attribution := llm.AttributionFromContext(ctx)
	fmt.Printf("%s: %s\n", attribution.LoopName, attribution.LoopID)
	// Output: reflection: loop-123
}
