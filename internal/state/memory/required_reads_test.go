package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newRequiredReadStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(t.TempDir()+"/required.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddMessage("conv", "user", "remember this important conversation history", OriginChannel); err != nil {
		t.Fatal(err)
	}
	if err := store.AddCompactionSummary("conv", CompactionSummaryPrefix+" the earlier conversation"); err != nil {
		t.Fatal(err)
	}
	return store
}

func requiredStoreRead(ctx context.Context, store *SQLiteStore, name string) (any, error) {
	switch name {
	case "messages":
		return store.GetMessages(ctx, "conv")
	case "tokens":
		return store.GetTokenCount(ctx, "conv")
	case "active count":
		return store.ActiveMessageCount(ctx, "conv")
	case "compaction messages":
		return store.GetMessagesForCompaction(ctx, "conv", 0)
	case "prior summaries":
		return store.GetActiveCompactionSummaries(ctx, "conv")
	case "transcript":
		return store.GetAllMessages(ctx, "conv")
	case "conversation":
		return store.GetConversation(ctx, "conv")
	case "snapshot":
		return store.GetAllConversations(ctx)
	default:
		panic("unknown required read: " + name)
	}
}

func assertRequiredReadFailed(t *testing.T, got any, err error) {
	t.Helper()
	if err == nil || (got != nil && !reflect.ValueOf(got).IsZero()) {
		t.Fatalf("required read = %#v, %v; want zero/nil payload and error", got, err)
	}
}

func TestRequiredReadsQueryFailures(t *testing.T) {
	store := newRequiredReadStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"messages", "tokens", "active count", "compaction messages", "prior summaries", "transcript", "conversation", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			got, err := requiredStoreRead(context.Background(), store, name)
			assertRequiredReadFailed(t, got, err)
		})
	}
}

func TestRequiredReadsRejectMalformedRows(t *testing.T) {
	for _, column := range []string{"timestamp", "mid_turn"} {
		t.Run(column, func(t *testing.T) {
			store := newRequiredReadStore(t)
			if _, err := store.db.Exec("UPDATE messages SET " + column + " = 'invalid'"); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"messages", "compaction messages", "prior summaries", "transcript", "conversation", "snapshot"} {
				t.Run(name, func(t *testing.T) {
					got, err := requiredStoreRead(context.Background(), store, name)
					assertRequiredReadFailed(t, got, err)
				})
			}
		})
	}
	t.Run("token scan", func(t *testing.T) {
		store := newRequiredReadStore(t)
		if _, err := store.db.Exec("UPDATE messages SET token_count = 1e100"); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetTokenCount(context.Background(), "conv")
		assertRequiredReadFailed(t, got, err)
	})
}

func TestRequiredReadsCancellation(t *testing.T) {
	for _, name := range []string{"messages", "tokens", "active count", "compaction messages", "prior summaries", "transcript", "conversation", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			store := newRequiredReadStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got, err := requiredStoreRead(ctx, store, name)
			assertRequiredReadFailed(t, got, err)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read error = %v", err)
			}
			store.db.SetMaxOpenConns(1)
			conn, err := store.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Bound a regression that accidentally drops the caller context.
			release := time.AfterFunc(time.Second, func() { _ = conn.Close() })
			defer release.Stop()
			ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			got, err = requiredStoreRead(ctx, store, name)
			assertRequiredReadFailed(t, got, err)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting read error = %v", err)
			}
		})
	}
}

func TestScanWorkingMessagesIterationFailure(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT 'one', 'user', 'valid first row', '2026-09-15T00:00:00Z', 0, ''
		UNION ALL SELECT 'two', 'user', json_extract('invalid', '$'), '2026-09-15T00:00:01Z', 0, ''`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got, err := scanWorkingMessages(context.Background(), rows)
	assertRequiredReadFailed(t, got, err)
	if !strings.Contains(err.Error(), "read active messages") {
		t.Fatalf("iteration error = %v", err)
	}
}

func TestScanWorkingMessagesCanceledIteration(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT 'one', 'user', 'valid first row', '2026-09-15T00:00:00Z', 0, ''
		UNION ALL SELECT 'two', 'user', 'later row', '2026-09-15T00:00:01Z', 0, ''`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("first row unavailable: %v", rows.Err())
	}
	cancel()
	got, err := scanWorkingMessages(ctx, rows)
	assertRequiredReadFailed(t, got, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("iteration error = %v", err)
	}
}

func TestConversationSnapshotSingleConnectionAndFailures(t *testing.T) {
	store := newRequiredReadStore(t)
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := store.GetAllConversations(ctx)
	if err != nil || len(got) != 1 || len(got[0].Messages) != 2 {
		t.Fatalf("single-connection snapshot = %#v, %v", got, err)
	}
	for _, field := range []string{"metadata", "created_at", "updated_at"} {
		t.Run(field, func(t *testing.T) {
			broken := newRequiredReadStore(t)
			if _, err := broken.db.Exec("UPDATE conversations SET " + field + " = 'invalid'"); err != nil {
				t.Fatal(err)
			}
			got, err := broken.GetAllConversations(context.Background())
			assertRequiredReadFailed(t, got, err)
			conv, err := broken.GetConversation(context.Background(), "conv")
			assertRequiredReadFailed(t, conv, err)
		})
	}
}

func TestBindConversationChannelPreservesMetadataOnReadFailure(t *testing.T) {
	store := newRequiredReadStore(t)
	if err := store.BindConversationChannel("conv", &ChannelBinding{Channel: "signal", Address: "original", IsOwner: true}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := store.db.QueryRow("SELECT metadata FROM conversations WHERE id = 'conv'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE messages SET timestamp = 'invalid'"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindConversationChannel("conv", &ChannelBinding{Channel: "signal", Address: "replacement"}); err == nil {
		t.Fatal("binding succeeded after unreadable history")
	}
	var after string
	if err := store.db.QueryRow("SELECT metadata FROM conversations WHERE id = 'conv'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("binding replaced metadata after a read failure: %s -> %s", before, after)
	}
}

func TestInMemoryRequiredReadsCancellationAndAbsence(t *testing.T) {
	store := NewStore(100)
	if err := store.AddMessage("conv", "user", "remember the conversation", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	messages, err := store.GetMessages(ctx, "conv")
	assertRequiredReadFailed(t, messages, err)
	count, err := store.GetTokenCount(ctx, "conv")
	assertRequiredReadFailed(t, count, err)
	conv, err := store.GetConversation(ctx, "conv")
	assertRequiredReadFailed(t, conv, err)
	conv, err = store.GetConversation(context.Background(), "missing")
	if conv != nil || err != nil {
		t.Fatalf("absent conversation = %#v, %v", conv, err)
	}
}

type failingRequiredCompactionStore struct {
	*SQLiteStore
	fail       string
	err        error
	applyCalls int
}

func (s *failingRequiredCompactionStore) GetTokenCount(ctx context.Context, id string) (int, error) {
	if s.fail == "tokens" {
		return 0, s.err
	}
	if s.fail == "active count" {
		return 0, nil // Exercise the count-based trigger independently.
	}
	return s.SQLiteStore.GetTokenCount(ctx, id)
}

func (s *failingRequiredCompactionStore) ActiveMessageCount(ctx context.Context, id string) (int, error) {
	if s.fail == "active count" {
		return 0, s.err
	}
	return s.SQLiteStore.ActiveMessageCount(ctx, id)
}

func (s *failingRequiredCompactionStore) GetMessagesForCompaction(ctx context.Context, id string, keep int) ([]Message, error) {
	if s.fail == "selection" {
		return []Message{{ID: "partial", Content: "must not summarize partial data"}}, s.err
	}
	return s.SQLiteStore.GetMessagesForCompaction(ctx, id, keep)
}

func (s *failingRequiredCompactionStore) GetActiveCompactionSummaries(ctx context.Context, id string) ([]Message, error) {
	if s.fail == "priors" {
		return nil, s.err
	}
	return s.SQLiteStore.GetActiveCompactionSummaries(ctx, id)
}

func (s *failingRequiredCompactionStore) ApplyCompaction(ctx context.Context, id string, ids []string, summary string, ts time.Time) error {
	s.applyCalls++
	return s.SQLiteStore.ApplyCompaction(ctx, id, ids, summary, ts)
}

func TestCompactionRequiredReadFailurePreservesHistory(t *testing.T) {
	for _, stage := range []string{"tokens", "active count", "selection", "priors"} {
		t.Run(stage, func(t *testing.T) {
			store := newCompactionTestStore(t, "conv", time.Now().Add(-time.Hour), 15)
			before, err := store.GetAllMessages(context.Background(), "conv")
			if err != nil {
				t.Fatal(err)
			}
			wantErr := fmt.Errorf("%s unavailable", stage)
			failed := &failingRequiredCompactionStore{SQLiteStore: store, fail: stage, err: wantErr}
			sum := &countingSummarizer{}
			compactor := NewCompactor(failed, CompactionConfig{
				MaxTokens: 2000, TriggerRatio: 0.5, MaxActiveMessages: 10,
				KeepRecent: 4, MinMessagesToCompact: 6,
			}, sum, slog.Default())
			if err := compactor.Compact(context.Background(), "conv"); !errors.Is(err, wantErr) {
				t.Fatalf("Compact error = %v, want %v", err, wantErr)
			}
			if sum.calls.Load() != 0 || failed.applyCalls != 0 {
				t.Fatalf("read failure reached summarize=%d apply=%d", sum.calls.Load(), failed.applyCalls)
			}
			after, err := store.GetAllMessages(context.Background(), "conv")
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("history changed after failed read: error=%v", err)
			}
			if stage == "tokens" || stage == "active count" {
				stats, err := compactor.CompactionStats(context.Background(), "conv")
				if stats != nil || !errors.Is(err, wantErr) {
					t.Fatalf("failed stats = %v, %v", stats, err)
				}
			}
		})
	}
}

type cancelingRequiredSummarizer struct{ cancel context.CancelFunc }

func (s cancelingRequiredSummarizer) Summarize(context.Context, []Message, string) (string, error) {
	s.cancel()
	return "late successful summary", nil
}

func TestCompactionCanceledSummaryDoesNotApply(t *testing.T) {
	store := newCompactionTestStore(t, "conv", time.Now().Add(-time.Hour), 15)
	before, err := store.GetAllMessages(context.Background(), "conv")
	if err != nil {
		t.Fatal(err)
	}
	tracked := &failingRequiredCompactionStore{SQLiteStore: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	compactor := NewCompactor(tracked, CompactionConfig{MaxTokens: 2000, TriggerRatio: 0.5, KeepRecent: 4, MinMessagesToCompact: 6}, cancelingRequiredSummarizer{cancel}, slog.Default())
	if err := compactor.Compact(ctx, "conv"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact error = %v, want cancellation", err)
	}
	if tracked.applyCalls != 0 {
		t.Fatal("late successful summary reached ApplyCompaction")
	}
	if err := store.ApplyCompaction(ctx, "conv", []string{before[0].ID}, "canceled direct apply", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled direct apply = %v", err)
	}
	after, err := store.GetAllMessages(context.Background(), "conv")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled compaction changed history: %v", err)
	}
}
