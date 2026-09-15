package loopqueue

import (
	"maps"
	"testing"
)

// TestSplitPendingFollowsTheQueue pins the split across every way a
// partition changes: a mark taken once still divides the work it saw
// from work that arrived after it, a key enqueued again falls after the
// mark, a deferral keeps its item before it, an acknowledgement removes
// the item from either side, and another partition is never counted.
func TestSplitPendingFollowsTheQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	enqueue := func(consumer, key string) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			if err := s.Enqueue(ctx, consumer, key, 0, []byte(`{}`)); err != nil {
				t.Fatalf("enqueue %s/%s: %v", consumer, key, err)
			}
		}
	}
	enqueue("review", "a")(t)
	enqueue("review", "b")(t)
	enqueue("other", "z")(t)

	first, err := s.SplitPending(ctx, "review", "")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if first.Through != 0 || first.Since != 2 || first.Mark == "" {
		t.Fatalf("split with no mark = %+v, want 0 through, 2 since, and a mark", first)
	}
	mark := first.Mark

	steps := []struct {
		name        string
		change      func(*testing.T)
		wantThrough int
		wantSince   int
	}{
		{"nothing since the mark", nil, 2, 0},
		{"another partition's work", enqueue("other", "y"), 2, 0},
		{"a new key", enqueue("review", "c"), 2, 1},
		{"a key enqueued again", enqueue("review", "a"), 1, 2},
		{"a deferral", func(t *testing.T) {
			if err := s.Defer(ctx, "review", "b"); err != nil {
				t.Fatalf("defer: %v", err)
			}
		}, 1, 2},
		{"the deferred item acknowledged", func(t *testing.T) { ackPending(t, s, "review", "b") }, 0, 2},
		{"the later work acknowledged", func(t *testing.T) {
			ackPending(t, s, "review", "a")
			ackPending(t, s, "review", "c")
		}, 0, 0},
	}
	for _, step := range steps {
		if step.change != nil {
			step.change(t)
		}
		got, err := s.SplitPending(ctx, "review", mark)
		if err != nil {
			t.Fatalf("%s: split: %v", step.name, err)
		}
		if got.Through != step.wantThrough || got.Since != step.wantSince || got.Pending() != step.wantThrough+step.wantSince {
			t.Fatalf("%s: split = %+v, want %d through and %d since", step.name, got, step.wantThrough, step.wantSince)
		}
	}
	if got, _ := s.SplitPending(ctx, "review", mark); got.Mark != "" {
		t.Errorf("mark of an empty partition = %q, want empty", got.Mark)
	}
}

// TestPendingCountsByKeyPrefixFollowsTheQueue pins the prefix count: it
// counts each key under the longest prefix it starts with, counts a key
// that starts with none only in total, reads the key and never the
// payload, keeps a deferred item, drops an acknowledged one, and never
// counts another partition.
func TestPendingCountsByKeyPrefixFollowsTheQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	prefixes := []string{"draft:a:", "draft:a:b:", "message:a:", "", "draft:a:"}
	// Every payload names account "z": a count that read payloads would
	// not follow the keys.
	const payload = `{"events":[{"type":"message","metadata":{"account":"z"}}]}`
	enqueue := func(consumer, key string) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			if err := s.Enqueue(ctx, consumer, key, 0, []byte(payload)); err != nil {
				t.Fatalf("enqueue %s/%s: %v", consumer, key, err)
			}
		}
	}
	counts := func(drafts, nested, msgs int) map[string]int {
		return map[string]int{"draft:a:": drafts, "draft:a:b:": nested, "message:a:": msgs}
	}

	steps := []struct {
		name      string
		change    func(*testing.T)
		want      map[string]int
		wantTotal int
	}{
		{"an empty partition", nil, counts(0, 0, 0), 0},
		{"a draft", enqueue("review", "draft:a:1"), counts(1, 0, 0), 1},
		{"a message", enqueue("review", "message:a:m@example.com"), counts(1, 0, 1), 2},
		{"a key under the longer prefix", enqueue("review", "draft:a:b:2"), counts(1, 1, 1), 3},
		{"a key under no prefix", enqueue("review", "session:9"), counts(1, 1, 1), 4},
		{"another partition's item", enqueue("other", "draft:a:9"), counts(1, 1, 1), 4},
		{"a key enqueued again", enqueue("review", "draft:a:1"), counts(1, 1, 1), 4},
		{"a deferral", func(t *testing.T) {
			if err := s.Defer(ctx, "review", "draft:a:b:2"); err != nil {
				t.Fatalf("defer: %v", err)
			}
		}, counts(1, 1, 1), 4},
		{"an acknowledgement", func(t *testing.T) { ackPending(t, s, "review", "draft:a:1") }, counts(0, 1, 1), 3},
	}
	for _, step := range steps {
		if step.change != nil {
			step.change(t)
		}
		got, total, err := s.PendingCountsByKeyPrefix(ctx, "review", prefixes)
		if err != nil {
			t.Fatalf("%s: count: %v", step.name, err)
		}
		if !maps.Equal(got, step.want) || total != step.wantTotal {
			t.Fatalf("%s: counts = %v total %d, want %v total %d", step.name, got, total, step.want, step.wantTotal)
		}
	}

	got, total, err := s.PendingCountsByKeyPrefix(ctx, "review", nil)
	if err != nil || len(got) != 0 || total != 3 {
		t.Errorf("with no prefixes: counts = %v total %d (%v), want none and a total of 3", got, total, err)
	}
}
