package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestSessionLifecycleIterationsKeepOriginatingSession(t *testing.T) {
	for _, operation := range []string{"session_close", "conversation_reset"} {
		for _, maxIterations := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/max_iterations=%d", operation, maxIterations), func(t *testing.T) {
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				store, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "thane.db"), 100)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := store.Close(); err != nil {
						t.Error(err)
					}
				})
				store.DB().SetMaxOpenConns(1)
				if _, err := store.DB().Exec(`PRAGMA foreign_keys = ON`); err != nil {
					t.Fatal(err)
				}
				archive, err := memory.NewArchiveStoreFromDB(store.DB(), nil, logger)
				if err != nil {
					t.Fatal(err)
				}
				adapter := memory.NewArchiveAdapter(archive, store, store, logger)
				const conversationID = "iteration-preservation"
				if err := store.AddMessage(conversationID, "user", "Original source evidence.", memory.OriginChannel); err != nil {
					t.Fatal(err)
				}
				oldSession, err := adapter.StartSession(conversationID)
				if err != nil {
					t.Fatal(err)
				}

				closeCall := llm.ToolCall{ID: "lifecycle-call"}
				closeCall.Function.Name = operation
				closeCall.Function.Arguments = map[string]any{"reason": "user requested transition", "carry_forward": "Keep this handoff."}
				afterCall := llm.ToolCall{ID: "after-rotation-call"}
				afterCall.Function.Name = "inspect_after_rotation"
				afterCall.Function.Arguments = map[string]any{}
				mock := &mockLLM{responses: []*llm.ChatResponse{
					{Model: "test-model", InputTokens: 10, OutputTokens: 3, Message: llm.Message{
						Role: "assistant", ToolCalls: []llm.ToolCall{closeCall, afterCall},
					}},
					{Model: "test-model", InputTokens: 20, OutputTokens: 5, Message: llm.Message{Role: "assistant", Content: "Transition complete."}},
				}}
				reg := tools.NewEmptyRegistry()
				loop := &Loop{logger: logger, memory: store, archiver: adapter, tools: reg, llm: mock, model: "test-model"}
				reg.SetSessionManager(loop)
				reg.SetConversationResetter(loop)
				var afterCallSession string
				reg.Register(&tools.Tool{
					Name:        "inspect_after_rotation",
					Description: "Inspect session attribution after the lifecycle tool in the same batch.",
					Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
					Handler: func(ctx context.Context, _ map[string]any) (string, error) {
						afterCallSession = tools.SessionIDFromContext(ctx)
						return "inspected", nil
					},
				})
				response, err := loop.Run(context.Background(), &Request{
					ConversationID: conversationID,
					Messages:       []Message{{Role: "user", Content: "Please close or reset this session, then inspect the transition."}},
					MaxIterations:  maxIterations,
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if response.Content != "Transition complete." || response.Iterations != 2 || response.InputTokens != 30 || response.OutputTokens != 8 {
					t.Fatalf("response/usage changed: %+v", response)
				}
				newSession := adapter.ActiveSessionID(conversationID)
				if newSession == "" || newSession == oldSession {
					t.Fatalf("session did not rotate: old=%q current=%q", oldSession, newSession)
				}
				if afterCallSession != oldSession {
					t.Errorf("later tool in same model batch got session %q, want originating session %q", afterCallSession, oldSession)
				}
				oldIterations, err := archive.GetSessionIterations(oldSession)
				if err != nil || len(oldIterations) != 1 {
					t.Fatalf("old session iterations = %+v, error = %v; want one", oldIterations, err)
				}
				newIterations, err := archive.GetSessionIterations(newSession)
				if err != nil || len(newIterations) != 1 {
					t.Fatalf("new session iterations = %+v, error = %v; want one", newIterations, err)
				}
				if oldIterations[0].InputTokens != 10 || newIterations[0].InputTokens != 20 || len(oldIterations[0].ToolCallIDs) != 2 || newIterations[0].HasToolCalls {
					t.Fatalf("iterations assigned to wrong sessions: old=%+v new=%+v", oldIterations, newIterations)
				}
				for _, id := range oldIterations[0].ToolCallIDs {
					var sessionID, status, result, toolError string
					var iteration int
					if err := store.DB().QueryRow(`SELECT session_id, status, result, COALESCE(error, ''), iteration_index
						FROM tool_calls WHERE id = ?`, id).Scan(&sessionID, &status, &result, &toolError, &iteration); err != nil {
						t.Fatal(err)
					}
					if sessionID != oldSession || status != "archived" || iteration != oldIterations[0].IterationIndex || result == "" || toolError != "" {
						t.Errorf("tool %s lost provenance/link/result: session=%q status=%q iteration=%d result=%q error=%q", id, sessionID, status, iteration, result, toolError)
					}
				}
				var messages, sourceCopies, iterations, inputTokens, outputTokens int
				if err := store.DB().QueryRow(`SELECT COUNT(*), SUM(content = 'Original source evidence.') FROM messages WHERE conversation_id = ?`, conversationID).Scan(&messages, &sourceCopies); err != nil {
					t.Fatal(err)
				}
				wantMessages := 3 // Original source, current user input, final reply.
				if operation == "session_close" {
					wantMessages++ // Carry-forward is the only additional row.
				}
				if messages != wantMessages || sourceCopies != 1 {
					t.Errorf("message count = %d, original copies = %d; want %d and 1", messages, sourceCopies, wantMessages)
				}
				if err := store.DB().QueryRow(`SELECT COUNT(*), SUM(input_tokens), SUM(output_tokens) FROM archive_iterations`).Scan(&iterations, &inputTokens, &outputTokens); err != nil {
					t.Fatal(err)
				}
				if iterations != 2 || inputTokens != 30 || outputTokens != 8 {
					t.Errorf("archived usage duplicated/lost: iterations=%d input=%d output=%d", iterations, inputTokens, outputTokens)
				}
			})
		}
	}
}
