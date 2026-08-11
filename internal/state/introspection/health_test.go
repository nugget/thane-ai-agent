package introspection

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/memguard"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func rowByName(t *testing.T, snap HealthSnapshot, name string) HealthRow {
	t.Helper()
	for _, row := range snap.Annunciator {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no annunciator row named %q in %+v", name, snap.Annunciator)
	return HealthRow{}
}

func TestInspectorHealthAssemblesAnnunciator(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	insp := NewInspector(HealthSources{
		ConnStatus: func() map[string]connwatch.ServiceStatus {
			return map[string]connwatch.ServiceStatus{
				"homeassistant": {Name: "homeassistant", Ready: true, LastCheck: now.Add(-30 * time.Second)},
				"signal":        {Name: "signal", Ready: false, LastCheck: now.Add(-time.Minute), LastError: "connection refused"},
			}
		},
		MemGuard: func() (memguard.Reading, bool) {
			return memguard.Reading{CurrentMB: 412, SoftMB: 1536, HardMB: 3072}, true
		},
		BusDropped: func() uint64 { return 0 },
		IndexStats: func() logging.IndexStats { return logging.IndexStats{} },
		SyncStates: func() []checkout.SyncState {
			return []checkout.SyncState{
				{Name: "core", OK: true, Outcome: provenance.SyncClean, LastSyncAt: now.Add(-5 * time.Minute)},
				{Name: "self", OK: true, Outcome: provenance.SyncDiverged, Detail: "local and remote both moved", LastSyncAt: now.Add(-5 * time.Minute)},
			}
		},
		QueueStats: func(context.Context) ([]loopqueue.ConsumerPending, error) {
			return []loopqueue.ConsumerPending{
				{Consumer: "archivist", Pending: 3, OldestEnqueuedAt: now.Add(-10 * time.Minute)},
				{Consumer: "core", Pending: 1, OldestEnqueuedAt: now.Add(-3 * time.Hour)},
			}, nil
		},
		LoopStatuses: func() []looppkg.Status {
			return []looppkg.Status{
				{Name: "core", State: looppkg.State("processing")},
				{Name: "archivist", State: looppkg.State("sleeping"), ConsecutiveErrors: 2, LastError: "boom"},
			}
		},
		StartedAt: now.Add(-48 * time.Hour),
	})
	insp.now = func() time.Time { return now }

	snap := insp.Health(context.Background())

	// Connections: one lamp each, failure carries the error and check age.
	if row := rowByName(t, snap, "conn:homeassistant"); row.Status != HealthOK || row.LastCheck != "-30s" {
		t.Errorf("homeassistant row = %+v, want ok checked -30s", row)
	}
	if row := rowByName(t, snap, "conn:signal"); row.Status != HealthFailed || row.Detail != "connection refused" {
		t.Errorf("signal row = %+v, want failed with the probe error", row)
	}

	// Memory guard under soft limit reads ok with the precomputed sentence.
	if row := rowByName(t, snap, "memory_guard"); row.Status != HealthOK || !strings.Contains(row.Detail, "412MB in use of 1536MB soft") {
		t.Errorf("memory_guard row = %+v", row)
	}

	// Doc sync: clean is ok; diverged degrades with outcome + detail.
	if row := rowByName(t, snap, "doc_sync:core"); row.Status != HealthOK {
		t.Errorf("core sync row = %+v, want ok", row)
	}
	if row := rowByName(t, snap, "doc_sync:self"); row.Status != HealthDegraded || !strings.Contains(row.Detail, "diverged") {
		t.Errorf("self sync row = %+v, want degraded diverged", row)
	}

	// Queue backlog: the 3h-old core item exceeds the 1h threshold.
	if row := rowByName(t, snap, "queue_backlog"); row.Status != HealthDegraded || !strings.Contains(row.Detail, "core") {
		t.Errorf("queue_backlog row = %+v, want degraded naming core", row)
	}
	if len(snap.Queues) != 2 || snap.Queues[0].Consumer != "archivist" || snap.Queues[0].Pending != 3 {
		t.Errorf("queues = %+v", snap.Queues)
	}

	// Loop census: archivist's consecutive errors degrade the fleet lamp.
	if row := rowByName(t, snap, "loops"); row.Status != HealthDegraded || !strings.Contains(row.Detail, "archivist") {
		t.Errorf("loops row = %+v, want degraded naming archivist", row)
	}
	if snap.Loops.Total != 2 || snap.Loops.Degraded != 1 || snap.Loops.ByState["processing"] != 1 {
		t.Errorf("census = %+v", snap.Loops)
	}

	// Host: uptime delta present, goroutines counted.
	if snap.Host.UptimeDelta == "" || snap.Host.Goroutines <= 0 {
		t.Errorf("host = %+v", snap.Host)
	}

	// Degraded() gathers exactly the not-ok rows.
	degraded := snap.Degraded()
	names := make([]string, 0, len(degraded))
	for _, row := range degraded {
		names = append(names, row.Name)
	}
	want := []string{"conn:signal", "doc_sync:self", "queue_backlog", "loops"}
	if len(degraded) != len(want) {
		t.Errorf("degraded rows = %v, want %v", names, want)
	}
}

func TestInspectorHealthDegradedStates(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	t.Run("memory guard past soft limit degrades, tripped fails", func(t *testing.T) {
		reading := memguard.Reading{CurrentMB: 1700, SoftMB: 1536, HardMB: 3072}
		insp := NewInspector(HealthSources{MemGuard: func() (memguard.Reading, bool) { return reading, true }})
		insp.now = func() time.Time { return now }
		if row := rowByName(t, insp.Health(context.Background()), "memory_guard"); row.Status != HealthDegraded {
			t.Errorf("past-soft row = %+v, want degraded", row)
		}
		reading.Tripped = true
		if row := rowByName(t, insp.Health(context.Background()), "memory_guard"); row.Status != HealthFailed {
			t.Errorf("tripped row = %+v, want failed", row)
		}
	})

	t.Run("disabled guard is ok with a disabled detail", func(t *testing.T) {
		insp := NewInspector(HealthSources{MemGuard: func() (memguard.Reading, bool) { return memguard.Reading{}, false }})
		if row := rowByName(t, insp.Health(context.Background()), "memory_guard"); row.Status != HealthOK || row.Detail != "disabled by config" {
			t.Errorf("disabled row = %+v", row)
		}
	})

	t.Run("bus drops and index loss degrade their rows", func(t *testing.T) {
		insp := NewInspector(HealthSources{
			BusDropped: func() uint64 { return 7 },
			IndexStats: func() logging.IndexStats { return logging.IndexStats{WriteErrors: 2} },
		})
		snap := insp.Health(context.Background())
		if row := rowByName(t, snap, "event_bus"); row.Status != HealthDegraded || !strings.Contains(row.Detail, "7") {
			t.Errorf("event_bus row = %+v", row)
		}
		if row := rowByName(t, snap, "log_index"); row.Status != HealthDegraded || !strings.Contains(row.Detail, "2 write errors") {
			t.Errorf("log_index row = %+v", row)
		}
	})

	t.Run("a failed queue probe is itself a health finding", func(t *testing.T) {
		insp := NewInspector(HealthSources{
			QueueStats: func(context.Context) ([]loopqueue.ConsumerPending, error) {
				return nil, context.DeadlineExceeded
			},
		})
		if row := rowByName(t, insp.Health(context.Background()), "queue_backlog"); row.Status != HealthDegraded || !strings.Contains(row.Detail, "probe failed") {
			t.Errorf("probe-failure row = %+v", row)
		}
	})

	t.Run("unwired sources contribute no rows", func(t *testing.T) {
		snap := NewInspector(HealthSources{}).Health(context.Background())
		if len(snap.Annunciator) != 0 {
			t.Errorf("empty sources produced rows: %+v", snap.Annunciator)
		}
	})
}

// TestLogActivityRidesTheSnapshot: the severity tally lands on the
// snapshot delta-formatted and sample-capped — data for the panel and
// system_health, with no judged lamp attached.
func TestLogActivityRidesTheSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var recent []logging.SeverityRecord
	for i := range maxLogSamples + 4 {
		recent = append(recent, logging.SeverityRecord{
			At: now.Add(-time.Duration(i+1) * time.Minute), Level: "WARN",
			Source: "mqtt", Msg: fmt.Sprintf("complaint %d", i),
		})
	}
	insp := NewInspector(HealthSources{
		LogSeverity: func() logging.SeveritySummary {
			return logging.SeveritySummary{
				WarnsSinceBoot: 40, ErrorsSinceBoot: 3,
				WarnsLastHour: 12, ErrorsLastHour: 1,
				Recent: recent,
			}
		},
	})
	insp.now = func() time.Time { return now }

	la := insp.Health(context.Background()).LogActivity
	if la.WarnsLastHour != 12 || la.ErrorsSinceBoot != 3 {
		t.Errorf("rates = %+v", la)
	}
	if len(la.Recent) != maxLogSamples {
		t.Fatalf("samples = %d, want capped at %d", len(la.Recent), maxLogSamples)
	}
	if la.Recent[0].AtDelta != "-60s" || la.Recent[0].Source != "mqtt" || la.Recent[0].Msg != "complaint 0" {
		t.Errorf("samples[0] = %+v, want the newest delta-formatted", la.Recent[0])
	}
}

// TestVersionInfoComputesTheDeployStory pins the mechanical deploy
// detection: the boundary walk (oldest same-version boot is when the
// running version arrived), the change classification, the boot-storm
// count, and the raw boot tail — all precomputed so no model ever
// bookkeeps a version.
func TestVersionInfoComputesTheDeployStory(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	boots := []BootRecord{
		{At: now.Add(-30 * time.Minute), Version: "v0.10.3", Commit: "abcdef1234567"},
		{At: now.Add(-2 * time.Hour), Version: "v0.10.3", Commit: "abcdef1234567"},
		{At: now.Add(-3 * 24 * time.Hour), Version: "v0.10.2", Commit: "0123456789ab"},
		{At: now.Add(-9 * 24 * time.Hour), Version: "v0.10.2", Commit: "0123456789ab"},
	}
	insp := NewInspector(HealthSources{
		BuildVersion: "v0.10.3",
		BuildCommit:  "abcdef1234567",
		BootHistory:  func(context.Context) ([]BootRecord, error) { return boots, nil },
	})
	insp.now = func() time.Time { return now }

	v := insp.Health(context.Background()).Version
	if v.Running != "v0.10.3" || v.Previous != "v0.10.2" {
		t.Errorf("running/previous = %s/%s, want v0.10.3/v0.10.2", v.Running, v.Previous)
	}
	// The boundary is the OLDEST boot carrying the running version.
	if v.ChangedDelta != "-2h" {
		t.Errorf("changed_delta = %q, want -2h — the boot that introduced v0.10.3", v.ChangedDelta)
	}
	if v.Change != "patch" {
		t.Errorf("change = %q, want patch", v.Change)
	}
	if v.BootsLast24h != 2 {
		t.Errorf("boots_last_24h = %d, want 2", v.BootsLast24h)
	}
	if len(v.RecentBoots) != 4 || v.RecentBoots[0].AtDelta != "-1800s" || v.RecentBoots[0].Commit != "abcdef1" {
		t.Errorf("recent_boots = %+v, want 4 delta-formatted rows with short commits", v.RecentBoots)
	}
}

func TestVersionChangeClassification(t *testing.T) {
	tests := []struct{ prev, curr, want string }{
		{"v0.10.2", "v0.10.3", "patch"},
		{"v0.10.3", "v0.11.0", "minor"},
		{"v0.11.0", "v1.0.0", "major"},
		{"v0.10.3", "dev", "dev"},
		{"dev", "v0.10.3", "dev"},
		{"v0.10.3", "v0.10.3", ""},
		{"v0.10.3-rc1", "v0.10.3", ""},
	}
	for _, tt := range tests {
		if got := classifyVersionChange(tt.prev, tt.curr); got != tt.want {
			t.Errorf("classify(%s -> %s) = %q, want %q", tt.prev, tt.curr, got, tt.want)
		}
	}
}

// TestRuntimeLampInformsWithoutJudging: restart counts have no
// objective threshold, so the runtime row carries facts (version, boot
// count when notable) and never a verdict — the judgment belongs to the
// reader with the context.
func TestRuntimeLampInformsWithoutJudging(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	var boots []BootRecord
	for i := range 8 {
		boots = append(boots, BootRecord{At: now.Add(-time.Duration(i+1) * 10 * time.Minute), Version: "dev"})
	}
	insp := NewInspector(HealthSources{
		BuildVersion: "dev",
		BootHistory:  func(context.Context) ([]BootRecord, error) { return boots, nil },
	})
	insp.now = func() time.Time { return now }

	if row := rowByName(t, insp.Health(context.Background()), "runtime"); row.Status != HealthOK || !strings.Contains(row.Detail, "8 boots") {
		t.Errorf("busy runtime row = %+v, want ok with the boot count stated", row)
	}

	// A single boot reads ok with just the version — an unremarkable
	// count is not worth a clause.
	calm := NewInspector(HealthSources{
		BuildVersion: "v0.10.3",
		BootHistory: func(context.Context) ([]BootRecord, error) {
			return []BootRecord{{At: now.Add(-time.Hour), Version: "v0.10.3"}}, nil
		},
	})
	calm.now = func() time.Time { return now }
	if row := rowByName(t, calm.Health(context.Background()), "runtime"); row.Status != HealthOK || !strings.Contains(row.Detail, "v0.10.3") || strings.Contains(row.Detail, "boots") {
		t.Errorf("calm runtime row = %+v, want version only", row)
	}
}

// TestRecordBootRetryingOutlastsTransientFailure pins the production
// lesson: the boot row must survive the startup write burst. The
// fail-then-succeed transition is the case that matters — early
// attempts fail, the contention clears, and exactly one row lands,
// carrying the instant the retrying STARTED rather than the instant
// the write finally won.
func TestRecordBootRetryingOutlastsTransientFailure(t *testing.T) {
	j := newTestJournal(t)
	ctx := context.Background()

	// Manufacture a deterministic transient failure: capture the table's
	// DDL, drop it (every insert now errors), and restore it mid-retry.
	var ddl string
	if err := j.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'loop_events'`).Scan(&ddl); err != nil {
		t.Fatalf("capture ddl: %v", err)
	}
	if _, err := j.db.ExecContext(ctx, `DROP TABLE loop_events`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	before := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		j.recordBootRetrying(ctx, "v0.10.3", "abc", nil, 50, 20*time.Millisecond)
	}()

	// Let a few attempts fail before the "contention" clears.
	time.Sleep(70 * time.Millisecond)
	restoredAt := time.Now()
	if _, err := j.db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("restore: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop never recovered after the failure cleared")
	}

	boots, err := j.RecentBoots(ctx, 5)
	if err != nil || len(boots) != 1 {
		t.Fatalf("boots = %v (%v), want exactly one row after recovery", boots, err)
	}
	// The recorded instant is the boot, not the lock acquisition: it
	// must predate the moment the table came back.
	if boots[0].At.Before(before.Add(-time.Second)) || boots[0].At.After(restoredAt) {
		t.Errorf("boot at = %v, want the retry start (before %v), not the eventual write time", boots[0].At, restoredAt)
	}

	// Bounded failure: a permanently dead database exhausts the attempt
	// budget rather than spinning forever.
	dead := newTestJournal(t)
	if err := dead.db.Close(); err != nil {
		t.Fatal(err)
	}
	deadDone := make(chan struct{})
	go func() {
		defer close(deadDone)
		dead.recordBootRetrying(ctx, "v0.10.3", "abc", nil, 3, time.Millisecond)
	}()
	select {
	case <-deadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop did not respect its attempt budget")
	}

	// Window exhaustion: an expired deadline ends the loop even with
	// attempts remaining (the production wrapper bounds lifetime this way).
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	windowDone := make(chan struct{})
	go func() {
		defer close(windowDone)
		dead.recordBootRetrying(expired, "v0.10.3", "abc", nil, 1000, time.Millisecond)
	}()
	select {
	case <-windowDone:
	case <-time.After(5 * time.Second):
		t.Fatal("retry loop ignored its window deadline")
	}
}

// TestJournalBootRoundTrip covers the boot journal itself.
func TestJournalBootRoundTrip(t *testing.T) {
	j := newTestJournal(t)
	ctx := context.Background()
	if err := j.RecordBoot(ctx, "v0.10.2", "0123456"); err != nil {
		t.Fatalf("record boot: %v", err)
	}
	if err := j.RecordBoot(ctx, "v0.10.3", "abcdef0"); err != nil {
		t.Fatalf("record boot: %v", err)
	}
	boots, err := j.RecentBoots(ctx, 10)
	if err != nil {
		t.Fatalf("recent boots: %v", err)
	}
	if len(boots) != 2 || boots[0].Version != "v0.10.3" || boots[1].Version != "v0.10.2" {
		t.Fatalf("boots = %+v, want newest-first v0.10.3 then v0.10.2", boots)
	}
	if boots[0].Commit != "abcdef0" || boots[0].At.IsZero() {
		t.Errorf("boot detail not round-tripped: %+v", boots[0])
	}
}

// TestLoopCensusTopWakersOrderAndCap pins the busiest-waker ranking:
// descending by trailing-day wakes with a stable name tiebreak, zero
// wakers omitted, and the list truncated at the cap.
func TestLoopCensusTopWakersOrderAndCap(t *testing.T) {
	statuses := []looppkg.Status{
		{Name: "quiet", WakesLast24h: 0},
		{Name: "beta", WakesLast24h: 12},
		{Name: "alpha", WakesLast24h: 12},
		{Name: "storm", WakesLast24h: 96},
		{Name: "steady", WakesLast24h: 4},
		{Name: "ticker", WakesLast24h: 7},
		{Name: "pinger", WakesLast24h: 6},
		{Name: "extra", WakesLast24h: 1},
		// A handler poller wakes per event with no model turn; its count
		// would bury every cognitive loop (prod's ha-state-watcher hit
		// 459 in eight minutes and metacog misread it as a storm).
		{Name: "ha-state-watcher", WakesLast24h: 459, HandlerOnly: true},
	}
	census := buildLoopCensus(statuses)

	if len(census.TopWakers) != maxCensusTopWakers {
		t.Fatalf("top wakers = %d entries, want the cap %d", len(census.TopWakers), maxCensusTopWakers)
	}
	wantOrder := []LoopWakeRate{
		{Name: "storm", WakesLast24h: 96},
		{Name: "alpha", WakesLast24h: 12},
		{Name: "beta", WakesLast24h: 12},
		{Name: "ticker", WakesLast24h: 7},
		{Name: "pinger", WakesLast24h: 6},
	}
	for i, want := range wantOrder {
		if census.TopWakers[i] != want {
			t.Errorf("top_wakers[%d] = %+v, want %+v", i, census.TopWakers[i], want)
		}
	}
	for _, w := range census.TopWakers {
		if w.Name == "quiet" {
			t.Errorf("zero-wake loop must not appear in top wakers")
		}
		if w.Name == "ha-state-watcher" {
			t.Errorf("handler-only poller must not appear in top wakers")
		}
	}
}

// TestCensusWakeWindowIsHonest: after a restart the in-memory wake ring
// only spans the uptime, and the census says so instead of letting the
// counts imply a full day's coverage.
func TestCensusWakeWindowIsHonest(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	young := NewInspector(HealthSources{
		LoopStatuses: func() []looppkg.Status { return []looppkg.Status{{Name: "core"}} },
		StartedAt:    now.Add(-8 * time.Minute),
	})
	young.now = func() time.Time { return now }
	if got := young.Health(context.Background()).Loops.WakeWindow; got != "8m" {
		t.Errorf("young wake_window = %q, want 8m", got)
	}

	mature := NewInspector(HealthSources{
		LoopStatuses: func() []looppkg.Status { return []looppkg.Status{{Name: "core"}} },
		StartedAt:    now.Add(-72 * time.Hour),
	})
	mature.now = func() time.Time { return now }
	if got := mature.Health(context.Background()).Loops.WakeWindow; got != "24h" {
		t.Errorf("mature wake_window = %q, want 24h", got)
	}
}
