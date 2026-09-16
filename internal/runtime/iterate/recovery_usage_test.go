package iterate

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestEngine_ForceTextPreservesCacheDurationTotals(t *testing.T) {
	first := toolCallResponse(makeToolCall("search", map[string]any{"q": "source"}))
	first.CacheCreationInputTokens = 30
	first.CacheCreation5mInputTokens = 10
	first.CacheCreation1hInputTokens = 20
	first.CacheReadInputTokens = 40
	last := textResponse("Recovered source evidence.")
	last.CacheCreationInputTokens = 70
	last.CacheCreation5mInputTokens = 30
	last.CacheCreation1hInputTokens = 40
	last.CacheReadInputTokens = 50
	mock := &mockLLM{responses: []*llm.ChatResponse{first, last}}
	cfg := baseCfg(mock, &mockExecutor{})
	cfg.MaxIterations = 1
	result, err := (&Engine{}).Run(context.Background(), cfg, baseMessages())
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.calls) != 2 || len(mock.calls[1].Tools) != 0 || !result.Exhausted || result.Content != last.Message.Content {
		t.Fatalf("force-text path not exercised: calls=%+v result=%+v", mock.calls, result)
	}
	if result.InputTokens != 30 || result.OutputTokens != 15 || result.CacheCreationInputTokens != 100 || result.CacheCreation5mInputTokens != 40 || result.CacheCreation1hInputTokens != 60 || result.CacheReadInputTokens != 90 {
		t.Fatalf("recovery usage lost: %+v", result)
	}
	var cache5m, cache1h int
	for _, iteration := range result.Iterations {
		cache5m += iteration.CacheCreation5mInputTokens
		cache1h += iteration.CacheCreation1hInputTokens
	}
	if cache5m != result.CacheCreation5mInputTokens || cache1h != result.CacheCreation1hInputTokens {
		t.Fatalf("iteration/response cache totals disagree: iterations=%+v result=%+v", result.Iterations, result)
	}
}
