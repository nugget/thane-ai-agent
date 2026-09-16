package memory

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

type attributedSummaryClient struct {
	mockLLMClient
	attributions []llm.Attribution
}

func (c *attributedSummaryClient) Chat(ctx context.Context, model string, messages []llm.Message, tools []map[string]any) (*llm.ChatResponse, error) {
	c.attributions = append(c.attributions, llm.AttributionFromContext(ctx))
	return c.mockLLMClient.Chat(ctx, model, messages, tools)
}

func TestSummarizerAttributesEachArchivedSession(t *testing.T) {
	store := newTestArchiveStore(t)
	client := &attributedSummaryClient{}
	worker := NewSummarizerWorker(store, client, newTestRouter(), slog.Default(), SummarizerConfig{Timeout: time.Second})
	parent := llm.Attribution{LoopID: "full-worker-loop-id", LoopName: "summary-worker", ParentLoopID: "full-core-id"}
	ctx := llm.WithAttribution(context.Background(), parent)
	for i, conversation := range []string{"first-full-conversation-id", "second-full-conversation-id"} {
		session := createUnsummarizedSession(t, store, conversation)
		if !worker.summarizeSession(ctx, session) {
			t.Fatal("session summary failed")
		}
		if len(client.attributions) != i+1 {
			t.Fatalf("model calls = %d, want %d", len(client.attributions), i+1)
		}
		want := parent
		want.SessionID = session.ID
		want.ConversationID = conversation
		if got := client.attributions[i]; got != want {
			t.Errorf("session attribution = %+v, want %+v", got, want)
		}
	}
	if got := llm.AttributionFromContext(ctx); got != parent {
		t.Errorf("worker context contaminated with a summarized session: %+v", got)
	}
}
