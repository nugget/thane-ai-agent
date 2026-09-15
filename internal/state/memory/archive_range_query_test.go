package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func TestArchiveRangeMixedTimestamps(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(fmt.Sprintf("unified=%t", unified), func(t *testing.T) {
			var store *ArchiveStore
			if unified {
				store, _ = newRangeTestStore(t)
			} else {
				store = newTestArchiveStore(t)
			}
			seed := func(id, conv, timestamp string) {
				t.Helper()
				_, err := store.msgDB().Exec(fmt.Sprintf(`INSERT INTO %s
					(id, conversation_id, session_id, role, content, timestamp, archived_at, archive_reason)
					VALUES (?, ?, 'session', 'user', ?, ?, ?, 'test')`, store.msgTableName),
					id, conv, id, timestamp, "2026-09-15T12:00:00Z")
				if err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately out of timestamp and ID order. Fractional boundaries
			// differ by one nanosecond, and offsets cross a UTC date boundary.
			seed("c", "conv", "2026-09-15 00:00:00.123456789+00:00")
			seed("f", "conv", "2026-09-15T00:00:01Z")
			seed("a", "conv", "2026-09-14T19:00:00.123456788-05:00")
			seed("b", "conv", "2026-09-15T05:30:00.123456789+05:30")
			seed("d", "conv", "2026-09-14 19:00:00.123456790 -0500 CDT")
			seed("e", "conv", "2026-09-15 00:00:00.999999999")
			seed("other", "other", "2026-09-15T00:00:00.123456789Z")
			from := time.Date(2026, 9, 15, 0, 0, 0, 123456789, time.UTC)
			to := from.Add(time.Nanosecond)
			for _, tc := range []struct {
				name     string
				from, to time.Time
				want     []string
			}{
				{"inclusive", from.In(time.FixedZone("west", -7*3600)), to, []string{"b", "c", "d"}},
				{"same instant", from, from, []string{"b", "c"}},
				{"whole second", from.Truncate(time.Second), from.Truncate(time.Second).Add(time.Second), []string{"a", "b", "c", "d", "e", "f"}},
				{"empty", to.Add(time.Nanosecond), to.Add(2 * time.Nanosecond), nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					oldest, err := store.GetMessagesByTimeRange(context.Background(), tc.from, tc.to, "conv", 100)
					if err != nil {
						t.Fatal(err)
					}
					newest, truncated, err := store.GetMessagesInRange(context.Background(), RangeOptions{From: tc.from, To: tc.to, ConversationID: "conv"})
					if err != nil {
						t.Fatal(err)
					}
					if truncated {
						t.Fatal("unexpected truncation")
					}
					for _, got := range [][]Message{oldest, newest} {
						var ids []string
						for _, msg := range got {
							ids = append(ids, msg.ID)
						}
						if !slices.Equal(ids, tc.want) {
							t.Fatalf("IDs = %v, want %v", ids, tc.want)
						}
					}
				})
			}
			oldest, err := store.GetMessagesByTimeRange(context.Background(), from, to, "conv", 1)
			if err != nil || len(oldest) != 1 || oldest[0].ID != "b" {
				t.Fatalf("oldest = %v, %v", oldest, err)
			}
			newest, truncated, err := store.GetMessagesInRange(context.Background(), RangeOptions{From: from, To: to, ConversationID: "conv", MaxMessages: 2})
			if err != nil || !truncated || len(newest) != 2 || newest[0].ID != "c" || newest[1].ID != "d" {
				t.Fatalf("newest = %v, truncated=%t, %v", newest, truncated, err)
			}
		})
	}
}

func TestArchiveRangeHardLimitAndDefaults(t *testing.T) {
	store, insert := newRangeTestStore(t)
	base := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	for i := range MaxArchiveRangeMessages + 1 {
		insert("conv", "session", "user", fmt.Sprint(i), base.Add(time.Duration(i)*time.Nanosecond))
	}
	for _, limit := range []int{0, -1, 1, MaxArchiveRangeMessages, int(^uint(0) >> 1)} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			oldest, err := store.GetMessagesByTimeRange(context.Background(), base, base.Add(time.Second), "conv", limit)
			if err != nil {
				t.Fatal(err)
			}
			want := min(limit, MaxArchiveRangeMessages)
			if limit <= 0 {
				want = 500
			}
			if len(oldest) != want || oldest[0].Content != "0" {
				t.Fatalf("oldest count=%d want=%d first=%s", len(oldest), want, oldest[0].Content)
			}
			newest, truncated, err := store.GetMessagesInRange(context.Background(), RangeOptions{From: base, To: base.Add(time.Second), MaxMessages: limit})
			if err != nil {
				t.Fatal(err)
			}
			if limit <= 0 {
				want = 200
			}
			if len(newest) != want || !truncated || newest[len(newest)-1].Content != fmt.Sprint(MaxArchiveRangeMessages) {
				t.Fatalf("newest count=%d want=%d truncated=%t", len(newest), want, truncated)
			}
		})
	}
}

func TestArchiveRangeCancellationAndInvalidBounds(t *testing.T) {
	store, _ := newRangeTestStore(t)
	for _, tc := range []struct {
		name  string
		query func(context.Context, time.Time, time.Time) error
	}{
		{"oldest", func(ctx context.Context, from, to time.Time) error {
			_, err := store.GetMessagesByTimeRange(ctx, from, to, "", 1)
			return err
		}},
		{"newest", func(ctx context.Context, from, to time.Time) error {
			_, _, err := store.GetMessagesInRange(ctx, RangeOptions{From: from, To: to})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			if err := tc.query(context.Background(), now.Add(time.Second), now); err == nil {
				t.Fatal("reversed bounds succeeded")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tc.query(ctx, now.Add(-time.Hour), now); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled query = %v", err)
			}
			// With the only connection held, cancellation must interrupt the
			// wait for it, rather than waiting for database work to finish.
			store.msgDB().SetMaxOpenConns(1)
			conn, err := store.msgDB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Release even if cancellation propagation regresses, so this
			// assertion fails within a bounded time instead of hanging.
			release := time.AfterFunc(time.Second, func() { _ = conn.Close() })
			defer release.Stop()
			ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := tc.query(ctx, now.Add(-time.Hour), now); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting query = %v", err)
			}
		})
	}
}

func TestArchiveRangeRejectsMalformedTimestamp(t *testing.T) {
	store, _ := newRangeTestStore(t)
	_, err := store.msgDB().Exec(`INSERT INTO messages (id, conversation_id, role, content, timestamp) VALUES ('bad', 'conv', 'user', 'bad', 'not a timestamp')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.GetMessagesByTimeRange(context.Background(), time.Now().Add(-time.Hour), time.Now(), "conv", 1)
	if err == nil || !strings.Contains(err.Error(), "unrecognized timestamp") {
		t.Fatalf("malformed timestamp query = %v", err)
	}
}

func TestScanMessagesReturnsIterationError(t *testing.T) {
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT 'one', 'conv', 'session', 'user', 'valid', '2026-09-15T00:00:00Z', 0, NULL, NULL, '', '', ''
		UNION ALL SELECT 'two', 'conv', 'session', 'user', 'bad', thane_timestamp_key('invalid'), 0, NULL, NULL, '', '', ''`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	messages, err := (&ArchiveStore{}).scanMessages(rows)
	if err == nil || messages != nil {
		t.Fatalf("partial read = %v, %v; want no messages and error", messages, err)
	}
}

func TestArchiveRangeUnboundedIncludesBeforeEpoch(t *testing.T) {
	store, insert := newRangeTestStore(t)
	insert("conv", "session", "user", "before epoch", time.Unix(-1, 999999999))
	insert("conv", "session", "user", "epoch", time.Unix(0, 0))
	got, truncated, err := store.GetMessagesInRange(context.Background(), RangeOptions{To: time.Unix(0, 0)})
	if err != nil || truncated || len(got) != 2 || got[0].Content != "before epoch" || got[1].Content != "epoch" {
		t.Fatalf("unbounded range = %v, truncated=%t, %v", got, truncated, err)
	}
	got, truncated, err = store.GetMessagesInRange(context.Background(), RangeOptions{From: time.Unix(0, 0), To: time.Unix(0, 0), MinMessages: 2})
	if err != nil || truncated || len(got) != 2 || got[0].Content != "before epoch" {
		t.Fatalf("floor range = %v, truncated=%t, %v", got, truncated, err)
	}
}

func TestArchiveRangeExplicitZeroInstant(t *testing.T) {
	store, insert := newRangeTestStore(t)
	insert("conv", "session", "user", "zero", time.Time{})
	insert("conv", "session", "user", "modern", time.Now())
	got, err := store.GetMessagesByTimeRange(context.Background(), time.Time{}, time.Time{}, "conv", 100)
	if err != nil || len(got) != 1 || got[0].Content != "zero" {
		t.Fatalf("explicit zero range = %v, %v", got, err)
	}
}

func TestArchiveRangeDoesNotReadExcludedMalformedTimestamps(t *testing.T) {
	store, insert := newRangeTestStore(t)
	now := time.Now()
	insert("conv", "kept", "user", "valid", now)
	_, err := store.msgDB().Exec(`INSERT INTO messages (id, conversation_id, session_id, role, content, timestamp)
		VALUES ('excluded', 'conv', 'excluded', 'user', 'bad', 'invalid'), ('other', 'other', 'kept', 'user', 'bad', 'invalid')`)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := store.GetMessagesInRange(context.Background(), RangeOptions{ConversationID: "conv", ExcludeSessionID: "excluded", From: now.Add(-time.Hour), To: now})
	if err != nil || len(got) != 1 || got[0].Content != "valid" {
		t.Fatalf("filtered range = %v, %v", got, err)
	}
}
