package tools

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/nugget/thane-ai-agent/internal/logging"
)

func openLogTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/test.db?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := logging.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSetLogIndexDB_Registration(t *testing.T) {
	db := openLogTestDB(t)
	r := NewEmptyRegistry()

	r.SetLogIndexDB(db)

	tool := r.Get("logs_query")
	if tool == nil {
		t.Fatal("logs_query tool not registered")
	}
	if tool.Name != "logs_query" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "logs_query")
	}
	if !tool.AlwaysAvailable {
		t.Error("logs_query should have AlwaysAvailable=true")
	}
}

func TestSetLogIndexDB_NilDB(t *testing.T) {
	r := NewEmptyRegistry()

	r.SetLogIndexDB(nil)

	tool := r.Get("logs_query")
	if tool != nil {
		t.Error("logs_query tool should not be registered with nil DB")
	}
}

func TestParseTimeOrDuration(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, got time.Time)
	}{
		{
			name:  "hours",
			input: "1h",
			check: func(t *testing.T, got time.Time) {
				t.Helper()
				diff := time.Since(got)
				if diff < 55*time.Minute || diff > 65*time.Minute {
					t.Errorf("1h: got %v ago, want ~1h ago", diff)
				}
			},
		},
		{
			name:  "minutes",
			input: "30m",
			check: func(t *testing.T, got time.Time) {
				t.Helper()
				diff := time.Since(got)
				if diff < 25*time.Minute || diff > 35*time.Minute {
					t.Errorf("30m: got %v ago, want ~30m ago", diff)
				}
			},
		},
		{
			name:  "days",
			input: "7d",
			check: func(t *testing.T, got time.Time) {
				t.Helper()
				diff := time.Since(got)
				want := 7 * 24 * time.Hour
				if diff < want-time.Hour || diff > want+time.Hour {
					t.Errorf("7d: got %v ago, want ~%v ago", diff, want)
				}
			},
		},
		{
			name:  "iso timestamp",
			input: "2026-03-10T14:00:00Z",
			check: func(t *testing.T, got time.Time) {
				t.Helper()
				want, _ := time.Parse(time.RFC3339, "2026-03-10T14:00:00Z")
				if !got.Equal(want) {
					t.Errorf("ISO: got %v, want %v", got, want)
				}
			},
		},
		{
			name:  "invalid returns zero",
			input: "not-a-time",
			check: func(t *testing.T, got time.Time) {
				t.Helper()
				if !got.IsZero() {
					t.Errorf("invalid: got %v, want zero", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTimeOrDuration(tt.input)
			tt.check(t, got)
		})
	}
}

func TestParseTimestamp(t *testing.T) {
	t.Run("RFC3339", func(t *testing.T) {
		got := parseTimestamp("2026-03-10T14:00:00Z")
		if got.IsZero() {
			t.Error("expected non-zero time")
		}
	})

	t.Run("RFC3339Nano", func(t *testing.T) {
		got := parseTimestamp("2026-03-10T14:00:00.123456789Z")
		if got.IsZero() {
			t.Error("expected non-zero time")
		}
	})

	t.Run("invalid", func(t *testing.T) {
		got := parseTimestamp("nope")
		if !got.IsZero() {
			t.Error("expected zero time for invalid input")
		}
	})
}
