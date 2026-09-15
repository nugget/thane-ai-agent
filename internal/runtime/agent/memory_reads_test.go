package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func mustMemoryMessages(t *testing.T, store MemoryStore, conversationID string) []memory.Message {
	t.Helper()
	messages, err := store.GetMessages(t.Context(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func TestRun_HistoryReadFailureDoesNotStartTurn(t *testing.T) {
	for _, failure := range []string{"query", "partial_scan", "canceled"} {
		for _, turn := range []struct{ name, conversationID, content string }{
			{"internal", "read-failure", "Inspect the source."},
			{"external", "owu-read-failure", "Inspect the source."},
			{"greeting", "read-failure", "hello"},
		} {
			t.Run(failure+"/"+turn.name, func(t *testing.T) {
				loop, store, _ := newAccountingTestLoop(t)
				for _, content := range []string{"complete earlier evidence", "second historical message"} {
					if err := store.AddMessage(turn.conversationID, "user", content, memory.OriginChannel); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				switch failure {
				case "query":
					// The history query needs this column; ordinary AddMessage
					// inserts remain valid, so a swallowed read could write input.
					if _, err := store.DB().Exec(`ALTER TABLE messages RENAME COLUMN mid_turn TO unavailable_mid_turn`); err != nil {
						t.Fatal(err)
					}
				case "partial_scan":
					// Corrupt the later row after a valid prefix. A truncated
					// successful read must not become the model's entire history.
					if _, err := store.DB().Exec(`UPDATE messages SET mid_turn = 'invalid boolean' WHERE content = 'second historical message'`); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					cancel()
				}
				calls := 0
				loop.llm = accountingTestClient{call: func(context.Context, string, []map[string]any) (*llm.ChatResponse, error) {
					calls++
					return &llm.ChatResponse{Model: "test", Message: llm.Message{Role: "assistant", Content: "must not run"}}, nil
				}}
				response, err := loop.Run(ctx, &Request{
					ConversationID: turn.conversationID,
					Messages:       []Message{{Role: "user", Content: turn.content}},
				}, nil)
				if err == nil || response != nil || calls != 0 {
					t.Fatalf("history failure started turn: response=%+v error=%v model_calls=%d", response, err, calls)
				}
				if !strings.Contains(err.Error(), "read conversation history") {
					t.Fatalf("lost read failure context: %v", err)
				}
				if failure == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation not preserved: %v", err)
				}
				var messages, usageRows int
				if err := store.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, turn.conversationID).Scan(&messages); err != nil {
					t.Fatal(err)
				}
				if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&usageRows); err != nil {
					t.Fatal(err)
				}
				if messages != 2 || usageRows != 0 {
					t.Fatalf("failed turn wrote input or usage: messages=%d usage=%d", messages, usageRows)
				}
				if history, readErr := loop.GetHistory(ctx, turn.conversationID); readErr == nil || history != nil {
					t.Fatalf("public history hid failure: messages=%v error=%v", history, readErr)
				}
				if transcript, readErr := loop.ConversationTranscript(ctx, turn.conversationID); readErr == nil || transcript != "" {
					t.Fatalf("public transcript hid failure: transcript=%q error=%v", transcript, readErr)
				}
			})
		}
	}
}

type failingTokenMemory struct {
	MemoryStore
	err error
}

func (m failingTokenMemory) GetTokenCount(context.Context, string) (int, error) {
	return 0, m.err
}

type readCheckCompactor struct {
	check   func(context.Context, string) (bool, error)
	compact func(context.Context, string) error
}

func (c readCheckCompactor) NeedsCompaction(ctx context.Context, id string) (bool, error) {
	return c.check(ctx, id)
}

func (c readCheckCompactor) Compact(ctx context.Context, id string) error {
	return c.compact(ctx, id)
}

func (readCheckCompactor) CompactionThreshold() int { return 1000 }

func TestRun_MemoryTelemetryFailurePreservesCompletedResponse(t *testing.T) {
	type contextKey struct{}
	loop, store, _ := newAccountingTestLoop(t)
	readErr := errors.New("token query unavailable")
	loop.memory = failingTokenMemory{MemoryStore: store, err: readErr}
	var logs bytes.Buffer
	ctx := logging.WithLogger(context.WithValue(t.Context(), contextKey{}, "origin"), slog.New(slog.NewTextHandler(&logs, nil)))
	loop.compactor = readCheckCompactor{
		check: func(callCtx context.Context, _ string) (bool, error) {
			if callCtx.Value(contextKey{}) != "origin" {
				t.Error("compaction decision lost caller context")
			}
			return false, readErr
		},
		compact: func(context.Context, string) error {
			t.Error("compaction scheduled after failed decision read")
			return nil
		},
	}
	loop.llm = accountingTestClient{call: func(context.Context, string, []map[string]any) (*llm.ChatResponse, error) {
		return &llm.ChatResponse{Model: "test", InputTokens: 20, OutputTokens: 5,
			Message: llm.Message{Role: "assistant", Content: "Completed response."}}, nil
	}}
	response, err := loop.Run(ctx, &Request{
		ConversationID: "telemetry", Messages: []Message{{Role: "user", Content: "Inspect the source."}},
	}, nil)
	if err != nil || response == nil || response.Content != "Completed response." {
		t.Fatalf("telemetry failure discarded completed response: %+v, %v", response, err)
	}
	if messages := mustMemoryMessages(t, store, "telemetry"); len(messages) != 2 || messages[1].Content != response.Content {
		t.Fatalf("completed response not persisted: %+v", messages)
	}
	if count, err := loop.GetTokenCount(ctx, "telemetry"); !errors.Is(err, readErr) || count != 0 {
		t.Fatalf("public token read lost error: count=%d error=%v", count, err)
	}
	if got := loop.routerContextTokens(ctx, "telemetry", 123); got != 123 {
		t.Fatalf("router fallback tokens=%d, want explicit prompt estimate 123", got)
	}
	for _, message := range []string{"failed to check conversation compaction", "failed to read request completion context tokens", "using prompt estimate"} {
		if !strings.Contains(logs.String(), message) {
			t.Errorf("missing logged read failure %q", message)
		}
	}
	if strings.Contains(logs.String(), " context_tokens=") {
		t.Fatalf("unavailable context tokens recorded as measurement: %s", logs.String())
	}
}

type observedReadMemory struct {
	MemoryStore
	observe func(context.Context)
}

func (m observedReadMemory) GetMessages(ctx context.Context, id string) ([]memory.Message, error) {
	m.observe(ctx)
	return m.MemoryStore.GetMessages(ctx, id)
}

func (m observedReadMemory) GetTokenCount(ctx context.Context, id string) (int, error) {
	m.observe(ctx)
	return m.MemoryStore.GetTokenCount(ctx, id)
}

func TestCompactionBackgroundReadsShareBoundedContext(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "origin"))
	defer cancel()
	var mu sync.Mutex
	var seen []context.Context
	done := make(chan struct{})
	loop := newTestLoop(observedReadMemory{MemoryStore: memory.NewStore(100), observe: func(readCtx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, readCtx)
		if len(seen) == 4 {
			close(done)
		}
	}}, nil)
	loop.compactor = readCheckCompactor{
		check: func(callCtx context.Context, _ string) (bool, error) {
			if callCtx != ctx {
				t.Error("decision read lost caller context")
			}
			return true, nil
		},
		compact: func(compactCtx context.Context, _ string) error {
			cancel() // A completed turn may end while maintenance continues.
			if compactCtx.Err() != nil || compactCtx.Value(contextKey{}) != "origin" {
				t.Error("background compaction lost lifetime or metadata")
			}
			return nil
		},
	}
	loop.maybeCompact(ctx, "maintenance")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background compaction did not complete its reads")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, readCtx := range seen {
		deadline, limited := readCtx.Deadline()
		if !limited || time.Until(deadline) > 5*time.Minute || readCtx.Value(contextKey{}) != "origin" || readCtx != seen[0] {
			t.Fatalf("background reads did not share bounded origin context: deadline=%v limited=%v", deadline, limited)
		}
	}
}
