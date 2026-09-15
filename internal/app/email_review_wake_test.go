package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

// TestEmailReviewLoopsTiming pins how accounts shape a review wake,
// shared loops taking the shortest delay and wait.
func TestEmailReviewLoopsTiming(t *testing.T) {
	tests := []struct {
		name     string
		accounts []config.EmailAccountConfig
		want     map[string]emailReviewTiming
	}{
		{"no review loop", []config.EmailAccountConfig{emailPassAccount("a", config.EmailMailboxConfig{})}, map[string]emailReviewTiming{}},
		{"defaults", []config.EmailAccountConfig{emailPassAccount("a", config.EmailMailboxConfig{ReviewLoop: "r"})}, map[string]emailReviewTiming{"r": {15 * time.Minute, 2 * time.Hour}}},
		{"shared loop takes the shortest", []config.EmailAccountConfig{
			emailPassAccount("a", config.EmailMailboxConfig{ReviewLoop: "r", ReviewDelay: 10 * time.Minute, ReviewMaxWait: 3 * time.Hour}),
			emailPassAccount("b", config.EmailMailboxConfig{ReviewLoop: "r", ReviewDelay: 20 * time.Minute, ReviewMaxWait: time.Hour}),
		}, map[string]emailReviewTiming{"r": {10 * time.Minute, time.Hour}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := emailReviewLoops(tt.accounts)
			if len(got) != len(tt.want) {
				t.Fatalf("loops = %v, want %v", got, tt.want)
			}
			for name, want := range tt.want {
				if got[name] != want {
					t.Errorf("%s = %+v, want %+v", name, got[name], want)
				}
			}
		})
	}
}

func reviewAccount(delay, maxWait time.Duration) []config.EmailAccountConfig {
	return []config.EmailAccountConfig{emailPassAccount("personal", config.EmailMailboxConfig{
		Owner: config.EmailMailboxOwnerOperator, ReviewLoop: email.DraftReviewLoopName, ReviewDelay: delay, ReviewMaxWait: maxWait,
	})}
}

func wakeEvent(t *testing.T, env messages.Envelope) messages.LoopEventPayload {
	t.Helper()
	payload, ok := env.Payload.(messages.LoopNotifyPayload)
	if !ok || len(payload.Events) != 1 {
		t.Fatalf("wake payload = %#v", env.Payload)
	}
	return payload.Events[0]
}

func enqueueReviewItem(t *testing.T, queue *loopqueue.Store, loop, subject string) {
	t.Helper()
	if err := queue.Enqueue(t.Context(), loop, subject, 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue %s: %v", subject, err)
	}
}

// ackReviewItem acknowledges subject under its current receipt.
func ackReviewItem(t *testing.T, queue *loopqueue.Store, loop, subject string) {
	t.Helper()
	items, err := queue.PeekAll(t.Context(), loop)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	for _, item := range items {
		if item.DedupKey != subject {
			continue
		}
		if _, err := queue.Ack(t.Context(), loop, subject, item.Receipt); err != nil {
			t.Fatalf("ack %s: %v", subject, err)
		}
		return
	}
	t.Fatalf("%s is not pending", subject)
}

// TestEmailReviewWakesAfterItsDelay pins the enqueue wake: nothing
// before review_delay, then one wake naming what waits.
func TestEmailReviewWakesAfterItsDelay(t *testing.T) {
	queue := emailPassQueue(t)
	bus, snapshot := captureBus()
	w := newEmailReviewWaker(queue, bus, reviewAccount(300*time.Millisecond, 5*time.Second), nil)
	w.arm()

	enqueueReviewItem(t, queue, email.DraftReviewLoopName, "draft:d-1")
	time.Sleep(50 * time.Millisecond)
	if n := len(snapshot()); n != 0 {
		t.Fatalf("woken %d times before review_delay", n)
	}
	envs := waitFor(t, snapshot, 1, 5*time.Second)
	if len(envs) != 1 || envs[0].To.Target != email.DraftReviewLoopName {
		t.Fatalf("wakes = %+v, want one to %s", envs, email.DraftReviewLoopName)
	}
	ev := wakeEvent(t, envs[0])
	if ev.Metadata["drafts"] != "1" || ev.Metadata["messages"] != "0" || !strings.Contains(ev.Summary, "1 draft and 0 messages await review") {
		t.Errorf("wake event = %+v", ev)
	}
}

// TestEmailReviewWakeHonoursMaxWait pins the bound: work arriving
// faster than review_delay still wakes the loop by review_max_wait.
func TestEmailReviewWakeHonoursMaxWait(t *testing.T) {
	queue := emailPassQueue(t)
	bus, snapshot := captureBus()
	w := newEmailReviewWaker(queue, bus, reviewAccount(250*time.Millisecond, 400*time.Millisecond), nil)
	w.arm()

	wokeDuringBurst := false
	for i := range 40 {
		enqueueReviewItem(t, queue, email.DraftReviewLoopName, "message:personal:m"+strings.Repeat("x", i%3)+"@example.com")
		time.Sleep(50 * time.Millisecond)
		if len(snapshot()) > 0 {
			wokeDuringBurst = true
			break
		}
	}
	if !wokeDuringBurst {
		t.Fatal("a steady stream of enqueues postponed the wake past review_max_wait")
	}
}

// TestEmailReviewDebounceSkipsAnnouncedWork pins that the wake paths
// never announce the same work twice: an enqueue whose debounce is still
// pending when the boot sweep wakes the loop does not wake it again, and
// work queued after that wake still does.
func TestEmailReviewDebounceSkipsAnnouncedWork(t *testing.T) {
	const loop = email.DraftReviewLoopName
	queue := emailPassQueue(t)
	bus, snapshot := captureBus()
	w := newEmailReviewWaker(queue, bus, reviewAccount(200*time.Millisecond, 5*time.Second), nil)
	w.arm()

	enqueueReviewItem(t, queue, loop, "draft:d-1") // arms the debounce
	w.Sweep(t.Context())                           // announces d-1 before it fires
	time.Sleep(600 * time.Millisecond)
	if n := len(snapshot()); n != 1 {
		t.Fatalf("woken %d times for one announced item, want 1: the debounce woke the loop again for work the sweep announced", n)
	}

	enqueueReviewItem(t, queue, loop, "draft:d-2")
	envs := waitFor(t, snapshot, 2, 5*time.Second)
	if got := wakeEvent(t, envs[1]).Metadata["pending"]; got != "2" {
		t.Errorf("second wake pending = %s, want 2", got)
	}
}

// TestEmailReviewSweeps pins the boot sweep and the re-wake: work
// queued before a restart wakes the loop, work a wake announced and
// left wakes it again once review_delay has passed since that wake, at
// most once per delay, and an empty queue never wakes it.
func TestEmailReviewSweeps(t *testing.T) {
	t.Run("boot sweep wakes queued work", func(t *testing.T) {
		queue := emailPassQueue(t)
		for _, subject := range []string{"draft:d-1", "message:personal:m@example.com"} {
			enqueueReviewItem(t, queue, email.DraftReviewLoopName, subject)
		}
		bus, snapshot := captureBus()
		w := newEmailReviewWaker(queue, bus, reviewAccount(0, 0), nil)
		w.Sweep(t.Context())
		envs := snapshot()
		if len(envs) != 1 || !strings.Contains(wakeEvent(t, envs[0]).Summary, "1 draft and 1 message await review (2 queued)") {
			t.Fatalf("boot sweep wakes = %+v", envs)
		}
	})

	t.Run("idle queue never wakes", func(t *testing.T) {
		bus, snapshot := captureBus()
		w := newEmailReviewWaker(emailPassQueue(t), bus, reviewAccount(0, 0), nil)
		w.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		w.Sweep(t.Context())
		w.resweep(t.Context())
		if n := len(snapshot()); n != 0 {
			t.Fatalf("an empty queue woke the loop %d times", n)
		}
		var none *emailReviewWaker
		none.arm()
		none.Sweep(t.Context())
		none.resweep(t.Context())
	})

	t.Run("announced leftovers wake once per delay", func(t *testing.T) {
		queue := emailPassQueue(t)
		enqueueReviewItem(t, queue, email.DraftReviewLoopName, "draft:d-1")
		bus, snapshot := captureBus()
		w := newEmailReviewWaker(queue, bus, reviewAccount(15*time.Minute, time.Hour), nil)
		start := time.Now()
		w.now = func() time.Time { return start }
		w.Sweep(t.Context())
		steps := []struct {
			at        time.Duration
			wantWakes int
		}{
			{0, 1},                // the sweep announced it just now
			{10 * time.Minute, 1}, // not yet a delay since that wake
			{20 * time.Minute, 2}, // still waiting a delay later
			{21 * time.Minute, 2}, // woken a minute ago
			{40 * time.Minute, 3}, // a delay since the last wake
		}
		for _, step := range steps {
			w.now = func() time.Time { return start.Add(step.at) }
			w.resweep(t.Context())
			if got := len(snapshot()); got != step.wantWakes {
				t.Fatalf("at +%s: wakes = %d, want %d", step.at, got, step.wantWakes)
			}
		}
	})
}

// TestEmailReviewResweepLeavesNewWork pins what the re-wake after a poll
// counts as leftover: only an item the last wake announced that still
// waits under the same receipt, deferred or not. Work no wake has
// announced, including an announced subject queued again with new
// evidence, waits out its own debounce and max wait, so a poll never
// cuts a burst short.
func TestEmailReviewResweepLeavesNewWork(t *testing.T) {
	const loop = email.DraftReviewLoopName
	tests := []struct {
		name      string
		sweep     bool
		after     func(t *testing.T, queue *loopqueue.Store)
		wantWakes int // the sweep's included
	}{
		{"never woken, a burst in progress", false, func(t *testing.T, q *loopqueue.Store) { enqueueReviewItem(t, q, loop, "draft:d-2") }, 0},
		{"announced and left", true, nil, 2},
		{"announced and deferred", true, func(t *testing.T, q *loopqueue.Store) {
			if err := q.Defer(t.Context(), loop, "draft:d-1"); err != nil {
				t.Fatalf("defer: %v", err)
			}
		}, 2},
		{"announced and acknowledged, new work since", true, func(t *testing.T, q *loopqueue.Store) {
			ackReviewItem(t, q, loop, "draft:d-1")
			enqueueReviewItem(t, q, loop, "draft:d-2")
		}, 1},
		{"announced, then queued again with new evidence", true, func(t *testing.T, q *loopqueue.Store) { enqueueReviewItem(t, q, loop, "draft:d-1") }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := emailPassQueue(t)
			enqueueReviewItem(t, queue, loop, "draft:d-1")
			bus, snapshot := captureBus()
			w := newEmailReviewWaker(queue, bus, reviewAccount(15*time.Minute, 2*time.Hour), nil)
			start := time.Now()
			w.now = func() time.Time { return start }
			if tt.sweep {
				w.Sweep(t.Context())
			}
			if tt.after != nil {
				tt.after(t, queue)
			}
			w.now = func() time.Time { return start.Add(20 * time.Minute) }
			w.resweep(t.Context())
			if got := len(snapshot()); got != tt.wantWakes {
				t.Fatalf("wakes = %d, want %d", got, tt.wantWakes)
			}
		})
	}
}

// TestEmailReviewQueueToolsSpeakToAWokenLoop pins the review loop's
// queue tool wording: an event-driven loop has no set_next_sleep, so
// neither the pull, nor the refusal of a second pull, nor a
// retained_newer acknowledgement sends it to sleep, while the
// archivist's tools keep their sleep wording.
func TestEmailReviewQueueToolsSpeakToAWokenLoop(t *testing.T) {
	tests := []struct {
		name   string
		build  func(*loopqueue.Store, string) []looppkg.RuntimeTool
		loop   string
		want   string
		banned string
	}{
		{"review loop", emailReviewQueueTools, email.DraftReviewLoopName, "wakes you again", "sleep"},
		{"archivist", buildLoopQueueTools, "archivist", "set_next_sleep", "wakes you again"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := emailPassQueue(t)
			enqueueReviewItem(t, queue, tt.loop, "draft:d-1")
			handlers := map[string]func(context.Context, map[string]any) (string, error){}
			var texts []string
			for _, tool := range tt.build(queue, tt.loop) {
				handlers[tool.Name] = tool.Handler
				if tool.Name == "queue_pull" {
					limit := tool.Parameters["properties"].(map[string]any)["limit"].(map[string]any)["description"].(string)
					texts = append(texts, tool.Description, limit)
				}
			}
			ctx := logging.WithRequestID(t.Context(), "req-1")
			if _, err := handlers["queue_pull"](ctx, map[string]any{}); err != nil {
				t.Fatalf("queue_pull: %v", err)
			}
			_, err := handlers["queue_pull"](ctx, map[string]any{})
			if err == nil {
				t.Fatal("a second queue_pull in one turn was allowed")
			}
			texts = append(texts, err.Error())
			enqueueReviewItem(t, queue, tt.loop, "draft:d-1") // newer evidence
			out, err := handlers["queue_ack"](ctx, map[string]any{"subject": "draft:d-1"})
			if err != nil || !strings.Contains(out, `"retained_newer"`) {
				t.Fatalf("queue_ack = %s, %v; want retained_newer", out, err)
			}
			texts = append(texts, out)

			if !strings.Contains(strings.Join(texts, "\n"), tt.want) {
				t.Errorf("the queue tools never say %q:\n%s", tt.want, strings.Join(texts, "\n"))
			}
			for _, text := range texts {
				if strings.Contains(text, tt.banned) {
					t.Errorf("queue tool text says %q: %s", tt.banned, text)
				}
			}
		})
	}
}

// TestStartEmailReviewPassesChecksRoutes pins the startup check, which
// runs whether or not mail is polled: a review_loop naming no definition
// stops startup before anything is woken, a wake_loop is checked only
// while mail is polled, and a sound config sweeps queued review work.
func TestStartEmailReviewPassesChecksRoutes(t *testing.T) {
	zero := 0
	tests := []struct {
		name      string
		poll      *int
		mailbox   config.EmailMailboxConfig
		wantErr   string
		wantWakes int
	}{
		{"polling off, the built-in review pass", &zero, config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator, ReviewLoop: email.DraftReviewLoopName}, "", 1},
		{"polling off, a review loop with no definition", &zero, config.EmailMailboxConfig{ReviewLoop: "missing-review"}, `mailbox.review_loop "missing-review" names no loop definition`, 0},
		{"polling off, an unused wake loop is not checked", &zero, config.EmailMailboxConfig{WakeLoop: "missing-loop", ReviewLoop: email.DraftReviewLoopName}, "", 1},
		{"polling on, a wake loop with no definition", nil, config.EmailMailboxConfig{WakeLoop: "missing-loop"}, `mailbox.wake_loop "missing-loop" names no loop definition`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := emailPassConfig(emailPassAccount("personal", tt.mailbox))
			cfg.Email.PollInterval = tt.poll
			a := emailRoutesApp(t, cfg)
			queue := emailPassQueue(t)
			if review := cfg.Email.Accounts[0].ReviewLoopName(); review != "" {
				enqueueReviewItem(t, queue, review, "draft:d-1")
			}
			bus, snapshot := captureBus()
			a.emailReviewWake = newEmailReviewWaker(queue, bus, cfg.Email.Accounts, nil)

			err := a.startEmailReviewPasses(t.Context())
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("startEmailReviewPasses() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("startEmailReviewPasses() = %v, want an error containing %q", err, tt.wantErr)
			}
			if got := len(snapshot()); got != tt.wantWakes {
				t.Errorf("wakes = %d, want %d", got, tt.wantWakes)
			}
		})
	}
}
