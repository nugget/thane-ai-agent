package telemetry

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// mockArchiveSource implements ArchiveSource for testing.
type mockArchiveSource struct {
	count int
	err   error
}

func (m *mockArchiveSource) ActiveSessionCount() (int, error) {
	return m.count, m.err
}

// mockAttachmentSource implements AttachmentSource for testing.
type mockAttachmentSource struct {
	total, totalBytes, unique int64
	err                       error
}

func (m *mockAttachmentSource) TelemetryStats(_ context.Context) (int64, int64, int64, error) {
	return m.total, m.totalBytes, m.unique, m.err
}

func TestCollect_AllNilSources(t *testing.T) {
	c := NewCollector(Sources{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	m := c.Collect(context.Background())
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m.CollectedAt.IsZero() {
		t.Error("CollectedAt should be set")
	}
	if m.LoopsTotal != 0 {
		t.Errorf("LoopsTotal = %d, want 0", m.LoopsTotal)
	}
	if m.ActiveSessions != 0 {
		t.Errorf("ActiveSessions = %d, want 0", m.ActiveSessions)
	}
}

func TestCollect_DBSizes(t *testing.T) {
	dir := t.TempDir()

	// Create a test file with known size.
	dbFile := filepath.Join(dir, "test.db")
	if err := os.WriteFile(dbFile, make([]byte, 4096), 0o640); err != nil {
		t.Fatal(err)
	}

	c := NewCollector(Sources{
		DBPaths: map[string]string{
			"main":    dbFile,
			"missing": filepath.Join(dir, "nonexistent.db"),
		},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	m := c.Collect(context.Background())
	if m.DBSizes["main"] != 4096 {
		t.Errorf("DBSizes[main] = %d, want 4096", m.DBSizes["main"])
	}
	if m.DBSizes["missing"] != 0 {
		t.Errorf("DBSizes[missing] = %d, want 0 (file doesn't exist)", m.DBSizes["missing"])
	}
}

func TestCollect_Sessions(t *testing.T) {
	c := NewCollector(Sources{
		ArchiveStore: &mockArchiveSource{count: 3},
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	m := c.Collect(context.Background())
	if m.ActiveSessions != 3 {
		t.Errorf("ActiveSessions = %d, want 3", m.ActiveSessions)
	}
}

func TestCollect_Attachments(t *testing.T) {
	c := NewCollector(Sources{
		AttachmentSource: &mockAttachmentSource{
			total:      42,
			totalBytes: 1024 * 1024,
			unique:     30,
		},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	m := c.Collect(context.Background())
	if m.AttachmentsTotal != 42 {
		t.Errorf("AttachmentsTotal = %d, want 42", m.AttachmentsTotal)
	}
	if m.AttachmentsTotalBytes != 1024*1024 {
		t.Errorf("AttachmentsTotalBytes = %d, want 1048576", m.AttachmentsTotalBytes)
	}
	if m.AttachmentsUnique != 30 {
		t.Errorf("AttachmentsUnique = %d, want 30", m.AttachmentsUnique)
	}
}

func TestCollect_Requests(t *testing.T) {
	db := openTestLogsDB(t)

	now := time.Now().UTC()
	// Insert log entries for two requests.
	for _, e := range []struct {
		reqID string
		ts    time.Time
		level string
	}{
		{"req-1", now.Add(-1 * time.Hour), "INFO"},
		{"req-1", now.Add(-1*time.Hour + 500*time.Millisecond), "INFO"},
		{"req-2", now.Add(-30 * time.Minute), "INFO"},
		{"req-2", now.Add(-30*time.Minute + 2*time.Second), "INFO"},
		{"", now.Add(-10 * time.Minute), "ERROR"}, // error without request_id
		{"req-3", now.Add(-5 * time.Minute), "ERROR"},
	} {
		_, err := db.Exec(
			`INSERT INTO log_entries (timestamp, level, msg, request_id, session_id, conversation_id, subsystem, tool, model)
			 VALUES (?, ?, 'test', ?, '', '', '', '', '')`,
			e.ts.Format(time.RFC3339Nano), e.level, e.reqID,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	c := NewCollector(Sources{
		LogsDB: db,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	m := c.Collect(context.Background())

	// 3 distinct request IDs.
	if m.Requests24h != 3 {
		t.Errorf("Requests24h = %d, want 3", m.Requests24h)
	}

	// 2 error entries (one without request_id, one with req-3).
	if m.Errors24h != 2 {
		t.Errorf("Errors24h = %d, want 2", m.Errors24h)
	}

	// Latency: req-1 = 500ms, req-2 = 2000ms. req-3 has only 1 entry
	// so it's excluded from latency (HAVING COUNT(*) > 1).
	// p50 of [500, 2000] = 500 + 0.5*(2000-500) = 1250.
	if m.LatencyP50Ms < 1200 || m.LatencyP50Ms > 1300 {
		t.Errorf("LatencyP50Ms = %.1f, want ~1250", m.LatencyP50Ms)
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		data   []float64
		p      float64
		wantLo float64
		wantHi float64
	}{
		{"empty", nil, 50, 0, 0},
		{"single", []float64{100}, 50, 100, 100},
		{"two values p50", []float64{100, 200}, 50, 140, 160},
		{"three values p95", []float64{10, 20, 30}, 95, 28, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.data, tt.p)
			if got < tt.wantLo || got > tt.wantHi {
				t.Errorf("percentile(%v, %.0f) = %.1f, want [%.1f, %.1f]",
					tt.data, tt.p, got, tt.wantLo, tt.wantHi)
			}
		})
	}
}

// openTestLogsDB creates an in-memory SQLite database with the
// log_entries schema matching the logging indexer.
func openTestLogsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE log_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		level TEXT NOT NULL DEFAULT '',
		msg TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		conversation_id TEXT NOT NULL DEFAULT '',
		subsystem TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
