package introspection

import (
	"context"
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
