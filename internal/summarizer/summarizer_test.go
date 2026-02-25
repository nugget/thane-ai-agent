package summarizer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// mockLLMClient returns a canned JSON metadata response.
type mockLLMClient struct {
	calls atomic.Int64
}

func (m *mockLLMClient) Chat(_ context.Context, _ string, _ []llm.Message, _ []map[string]any) (*llm.ChatResponse, error) {
	m.calls.Add(1)
	resp := map[string]any{
		"title":         "Test Session Title",
		"tags":          []string{"test", "mock"},
		"one_liner":     "A test session.",
		"paragraph":     "This was a test session with mock data.",
		"detailed":      "Detailed description of the test session.",
		"key_decisions": []string{"Used mocks"},
		"participants":  []string{"tester"},
		"session_type":  "debugging",
	}
	body, _ := json.Marshal(resp)
	return &llm.ChatResponse{
		Message: llm.Message{Role: "assistant", Content: string(body)},
	}, nil
}

func (m *mockLLMClient) ChatStream(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return m.Chat(ctx, model, msgs, tools)
}

func (m *mockLLMClient) Ping(_ context.Context) error { return nil }

// failingLLMClient always returns an error.
type failingLLMClient struct {
	calls atomic.Int64
}

func (m *failingLLMClient) Chat(_ context.Context, _ string, _ []llm.Message, _ []map[string]any) (*llm.ChatResponse, error) {
	m.calls.Add(1)
	return nil, fmt.Errorf("llm unavailable")
}

func (m *failingLLMClient) ChatStream(ctx context.Context, model string, msgs []llm.Message, tools []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return m.Chat(ctx, model, msgs, tools)
}

func (m *failingLLMClient) Ping(_ context.Context) error { return nil }

func newTestRouter() *router.Router {
	return router.NewRouter(slog.Default(), router.Config{
		DefaultModel: "test-model",
		Models: []router.Model{
			{
				Name:     "test-model",
				Provider: "ollama",
				Quality:  5,
				Speed:    8,
				CostTier: 0,
			},
		},
	})
}

func newTestStore(t *testing.T) *memory.ArchiveStore {
	t.Helper()
	dbPath := t.TempDir() + "/test-archive.db"
	store, err := memory.NewArchiveStore(dbPath, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// createUnsummarizedSession creates an ended session with messages but no metadata.
func createUnsummarizedSession(t *testing.T, store *memory.ArchiveStore, convID string) *memory.Session {
	t.Helper()
	sess, err := store.StartSession(convID)
	if err != nil {
		t.Fatal(err)
	}

	// Archive a message so the transcript is non-empty.
	msgs := []memory.ArchivedMessage{
		{
			ID:             fmt.Sprintf("msg-%s", sess.ID),
			ConversationID: convID,
			SessionID:      sess.ID,
			Role:           "user",
			Content:        "Hello, this is a test message.",
			Timestamp:      time.Now(),
			ArchiveReason:  "test",
		},
	}
	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	if err := store.EndSession(sess.ID, "test"); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestWorker_StartupScan(t *testing.T) {
	store := newTestStore(t)
	mock := &mockLLMClient{}
	rtr := newTestRouter()

	// Create 3 unsummarized sessions.
	for i := 0; i < 3; i++ {
		createUnsummarizedSession(t, store, fmt.Sprintf("conv-%d", i))
	}

	cfg := Config{
		Interval:     time.Hour, // Long interval — only startup scan should fire.
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, mock, rtr, slog.Default(), cfg)
	w.Start(ctx)

	// Give the startup scan time to complete.
	waitFor(t, 5*time.Second, func() bool {
		return mock.calls.Load() >= 3
	})

	cancel()
	w.Stop()

	// Verify all 3 sessions now have metadata.
	remaining, err := store.UnsummarizedSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 unsummarized sessions after startup scan, got %d", len(remaining))
	}
}

func TestWorker_PeriodicScan(t *testing.T) {
	store := newTestStore(t)
	mock := &mockLLMClient{}
	rtr := newTestRouter()

	cfg := Config{
		Interval:     50 * time.Millisecond, // Fast interval for testing.
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, mock, rtr, slog.Default(), cfg)
	w.Start(ctx)

	// Wait for startup scan (no sessions yet, so 0 calls).
	time.Sleep(20 * time.Millisecond)

	// Create a session after startup — periodic scan should pick it up.
	createUnsummarizedSession(t, store, "conv-late")

	waitFor(t, 5*time.Second, func() bool {
		return mock.calls.Load() >= 1
	})

	cancel()
	w.Stop()

	remaining, err := store.UnsummarizedSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 unsummarized sessions after periodic scan, got %d", len(remaining))
	}
}

func TestWorker_SkipsSummarizedSessions(t *testing.T) {
	store := newTestStore(t)
	mock := &mockLLMClient{}
	rtr := newTestRouter()

	// Create a session and give it metadata.
	sess := createUnsummarizedSession(t, store, "conv-already-done")
	meta := &memory.SessionMetadata{OneLiner: "Already done"}
	if err := store.SetSessionMetadata(sess.ID, meta, "Already Done", []string{"done"}); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Interval:     time.Hour,
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, mock, rtr, slog.Default(), cfg)
	w.Start(ctx)

	// Give startup scan time to run.
	time.Sleep(50 * time.Millisecond)

	cancel()
	w.Stop()

	if mock.calls.Load() != 0 {
		t.Errorf("expected 0 LLM calls for already-summarized session, got %d", mock.calls.Load())
	}
}

func TestWorker_GracefulShutdown(t *testing.T) {
	store := newTestStore(t)
	mock := &mockLLMClient{}
	rtr := newTestRouter()

	cfg := Config{
		Interval:     time.Hour,
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, mock, rtr, slog.Default(), cfg)
	w.Start(ctx)
	cancel()

	// Stop should return promptly.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5 seconds")
	}
}

func TestWorker_LLMFailureContinues(t *testing.T) {
	store := newTestStore(t)
	failing := &failingLLMClient{}
	rtr := newTestRouter()

	// Create 2 sessions — both should be attempted even though LLM fails.
	createUnsummarizedSession(t, store, "conv-0")
	createUnsummarizedSession(t, store, "conv-1")

	cfg := Config{
		Interval:     time.Hour,
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, failing, rtr, slog.Default(), cfg)
	w.Start(ctx)

	waitFor(t, 5*time.Second, func() bool {
		return failing.calls.Load() >= 2
	})

	cancel()
	w.Stop()

	// Both sessions should still be unsummarized (LLM failed).
	remaining, err := store.UnsummarizedSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Errorf("expected 2 unsummarized sessions after LLM failure, got %d", len(remaining))
	}
}

func TestWorker_ClosesOrphanedSessions(t *testing.T) {
	store := newTestStore(t)
	mock := &mockLLMClient{}
	rtr := newTestRouter()

	// Create a session with messages but don't end it — simulates a crash.
	sess, err := store.StartSession("conv-orphan")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []memory.ArchivedMessage{
		{
			ID:             fmt.Sprintf("msg-%s", sess.ID),
			ConversationID: "conv-orphan",
			SessionID:      sess.ID,
			Role:           "user",
			Content:        "This session was orphaned by a crash.",
			Timestamp:      time.Now(),
			ArchiveReason:  "test",
		},
	}
	if err := store.ArchiveMessages(msgs); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Interval:     time.Hour,
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, mock, rtr, slog.Default(), cfg)
	// Set startTime to the future so the orphaned session qualifies.
	w.startTime = time.Now().Add(time.Minute)
	w.Start(ctx)

	// The worker should close the orphaned session and then summarize it.
	waitFor(t, 5*time.Second, func() bool {
		return mock.calls.Load() >= 1
	})

	cancel()
	w.Stop()

	// Verify the session was closed with crash_recovery.
	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EndReason != "crash_recovery" {
		t.Errorf("end_reason = %q, want %q", got.EndReason, "crash_recovery")
	}

	// Verify it was summarized.
	if got.Title == "" {
		t.Error("orphaned session should have been summarized after recovery")
	}
}

func TestWorker_StaleCountSessionExcluded(t *testing.T) {
	store := newTestStore(t)
	mock := &mockLLMClient{}
	rtr := newTestRouter()

	// Create an ended session with message_count > 0 but no archived
	// messages — simulates scheduler/delegate sessions that bump the
	// counter without producing a user-visible transcript.
	// With the EXISTS-based query (issue #341), this session is excluded
	// from UnsummarizedSessions entirely rather than fetched-then-marked.
	sess, err := store.StartSession("conv-stale")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndSession(sess.ID, "test"); err != nil {
		t.Fatal(err)
	}

	// The session should not appear in the unsummarized list at all.
	remaining, err := store.UnsummarizedSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 unsummarized sessions (stale count excluded), got %d", len(remaining))
	}

	cfg := Config{
		Interval:     time.Hour,
		Timeout:      10 * time.Second,
		PauseBetween: 1 * time.Millisecond,
		BatchSize:    10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(store, mock, rtr, slog.Default(), cfg)
	w.Start(ctx)

	// Give the worker one scan cycle.
	time.Sleep(50 * time.Millisecond)

	cancel()
	w.Stop()

	// LLM should never have been called — session was never fetched.
	if mock.calls.Load() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", mock.calls.Load())
	}

	// Session should remain untouched (no title, no metadata).
	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "" {
		t.Errorf("title = %q, want empty (session should be untouched)", got.Title)
	}
}

func TestBuildTranscript(t *testing.T) {
	messages := []memory.ArchivedMessage{
		{Role: "system", Content: "You are a helpful assistant.", Timestamp: time.Now()},
		{Role: "user", Content: "Hello", Timestamp: time.Now()},
		{Role: "assistant", Content: "Hi there!", Timestamp: time.Now()},
	}

	transcript := buildTranscript(messages)

	// System message should be excluded.
	if strings.Contains(transcript, "helpful assistant") {
		t.Error("transcript should exclude system messages")
	}
	if !strings.Contains(transcript, "Hello") {
		t.Error("transcript should contain user message")
	}
	if !strings.Contains(transcript, "Hi there!") {
		t.Error("transcript should contain assistant message")
	}
}

func TestBuildTranscript_Truncation(t *testing.T) {
	// Create a message that exceeds maxTranscriptBytes.
	longContent := make([]byte, maxTranscriptBytes+1000)
	for i := range longContent {
		longContent[i] = 'x'
	}

	messages := []memory.ArchivedMessage{
		{Role: "user", Content: string(longContent), Timestamp: time.Now()},
		{Role: "assistant", Content: "Should not appear", Timestamp: time.Now()},
	}

	transcript := buildTranscript(messages)

	if !strings.Contains(transcript, "... (truncated)") {
		t.Error("transcript should be truncated")
	}
	if strings.Contains(transcript, "Should not appear") {
		t.Error("second message should be excluded after truncation")
	}
}

func TestParseMetadataResponse(t *testing.T) {
	resp := `{
		"title": "Test Title",
		"tags": ["go", "test"],
		"one_liner": "A one-liner.",
		"paragraph": "A paragraph summary.",
		"detailed": "Detailed text.",
		"key_decisions": ["decision 1"],
		"participants": ["Alice"],
		"session_type": "debugging"
	}`

	toolUsage := map[string]int{"shell_exec": 3}
	meta, title, tags := parseMetadataResponse(resp, toolUsage, slog.Default())

	if title != "Test Title" {
		t.Errorf("title = %q, want %q", title, "Test Title")
	}
	if len(tags) != 2 || tags[0] != "go" {
		t.Errorf("tags = %v, want [go test]", tags)
	}
	if meta.OneLiner != "A one-liner." {
		t.Errorf("one_liner = %q", meta.OneLiner)
	}
	if meta.ToolsUsed["shell_exec"] != 3 {
		t.Errorf("tools_used = %v", meta.ToolsUsed)
	}
}

func TestParseMetadataResponse_CodeFences(t *testing.T) {
	resp := "```json\n{\"title\": \"Fenced\", \"paragraph\": \"text\"}\n```"

	meta, title, _ := parseMetadataResponse(resp, nil, slog.Default())

	if title != "Fenced" {
		t.Errorf("title = %q, want %q", title, "Fenced")
	}
	if meta.Paragraph != "text" {
		t.Errorf("paragraph = %q, want %q", meta.Paragraph, "text")
	}
}

func TestParseMetadataResponse_InvalidJSON(t *testing.T) {
	resp := "Not valid JSON at all"

	meta, title, tags := parseMetadataResponse(resp, nil, slog.Default())

	if title != "" {
		t.Errorf("title should be empty on parse failure, got %q", title)
	}
	if tags != nil {
		t.Errorf("tags should be nil on parse failure, got %v", tags)
	}
	if meta.Paragraph != "Not valid JSON at all" {
		t.Errorf("paragraph should be raw content on parse failure, got %q", meta.Paragraph)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
