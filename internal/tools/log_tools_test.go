package tools

import (
	"database/sql"
	"fmt"
	"strings"
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

func insertTestLogEntry(t *testing.T, db *sql.DB, ts time.Time, level, msg, loopID, loopName, subsystem string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO log_entries
		(timestamp, level, msg, loop_id, loop_name, subsystem)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ts.UTC().Format(time.RFC3339Nano), level, msg,
		sql.NullString{String: loopID, Valid: loopID != ""},
		sql.NullString{String: loopName, Valid: loopName != ""},
		sql.NullString{String: subsystem, Valid: subsystem != ""},
	)
	if err != nil {
		t.Fatalf("insert test log entry: %v", err)
	}
}

func TestLogsQuery_LoopFilters(t *testing.T) {
	db := openLogTestDB(t)
	now := time.Now()

	// Insert entries for two different loops at INFO and above
	// (default level filter is INFO now).
	insertTestLogEntry(t, db, now.Add(-10*time.Second), "INFO", "metacog iteration", "loop-aaa", "metacognitive", "loop")
	insertTestLogEntry(t, db, now.Add(-9*time.Second), "WARN", "metacog warning", "loop-aaa", "metacognitive", "loop")
	insertTestLogEntry(t, db, now.Add(-8*time.Second), "INFO", "email poll", "loop-bbb", "email-poller", "loop")
	insertTestLogEntry(t, db, now.Add(-7*time.Second), "INFO", "no loop context", "", "", "agent")
	insertTestLogEntry(t, db, now.Add(-6*time.Second), "DEBUG", "debug noise", "loop-aaa", "metacognitive", "loop")

	r := NewEmptyRegistry()
	r.SetLogIndexDB(db)
	tool := r.Get("logs_query")
	if tool == nil {
		t.Fatal("logs_query not registered")
	}

	t.Run("filter by loop_id", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"loop_id": "loop-aaa",
			"since":   "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "metacog iteration") {
			t.Error("should contain metacog iteration")
		}
		if !strings.Contains(result, "metacog warning") {
			t.Error("should contain metacog warning")
		}
		if strings.Contains(result, "email poll") {
			t.Error("should not contain email-poller entries")
		}
	})

	t.Run("filter by loop_name", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"loop_name": "email-poller",
			"since":     "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "email poll") {
			t.Error("should contain email poll entry")
		}
		if strings.Contains(result, "metacog") {
			t.Error("should not contain metacognitive entries")
		}
	})

	t.Run("response includes loop fields", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"loop_id": "loop-aaa",
			"limit":   float64(1),
			"since":   "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Compact JSON now (no spaces after colons in keys).
		if !strings.Contains(result, `"loop_id":"loop-aaa"`) {
			t.Errorf("response should include loop_id field, got: %s", result)
		}
		if !strings.Contains(result, `"loop_name":"metacognitive"`) {
			t.Errorf("response should include loop_name field, got: %s", result)
		}
	})

	t.Run("default level is INFO", func(t *testing.T) {
		// No level specified — should default to INFO, excluding DEBUG.
		result, err := tool.Handler(nil, map[string]any{
			"loop_id": "loop-aaa",
			"since":   "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "debug noise") {
			t.Error("default level should be INFO, should not include DEBUG entries")
		}
		if !strings.Contains(result, "metacog iteration") {
			t.Error("should include INFO entries")
		}
		if !strings.Contains(result, "metacog warning") {
			t.Error("should include WARN entries")
		}
	})

	t.Run("explicit DEBUG includes debug entries", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"loop_id": "loop-aaa",
			"level":   "DEBUG",
			"since":   "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "debug noise") {
			t.Error("explicit DEBUG level should include DEBUG entries")
		}
	})

	t.Run("limit respected", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"since": "1h",
			"limit": float64(2),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, `"count":2`) {
			t.Errorf("limit=2 should return count:2, got: %s", result)
		}
	})

	t.Run("no loop filter returns all INFO+", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"since": "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "metacog iteration") || !strings.Contains(result, "email poll") || !strings.Contains(result, "no loop context") {
			t.Errorf("unfiltered query should return all INFO+ entries, got: %s", result)
		}
		// DEBUG should be excluded by default.
		if strings.Contains(result, "debug noise") {
			t.Error("default query should not include DEBUG entries")
		}
	})
}

func TestLogsQuery_ResultTruncation(t *testing.T) {
	db := openLogTestDB(t)
	now := time.Now()

	// Insert many entries with large messages to exceed the byte cap.
	bigMsg := strings.Repeat("x", 2000)
	for i := 0; i < 100; i++ {
		insertTestLogEntry(t, db, now.Add(-time.Duration(i)*time.Second), "INFO",
			fmt.Sprintf("entry-%d %s", i, bigMsg), "", "", "agent")
	}

	r := NewEmptyRegistry()
	r.SetLogIndexDB(db)
	tool := r.Get("logs_query")

	result, err := tool.Handler(nil, map[string]any{
		"since": "1h",
		"limit": float64(100),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result) > maxLogsResultBytes {
		t.Errorf("result must not exceed %d-byte hard cap, got %d bytes", maxLogsResultBytes, len(result))
	}
	if !strings.Contains(result, `"truncated":true`) {
		t.Error("truncated result should include truncated:true")
	}
	if !strings.Contains(result, `"returned":100`) {
		t.Error("should report returned count from DB")
	}
	// count should be less than returned when truncated.
	if strings.Contains(result, `"count":100`) {
		t.Error("count should be less than returned when truncated by byte cap")
	}
}

func TestLogsQuery_ZeroResultPatternHint(t *testing.T) {
	db := openLogTestDB(t)
	now := time.Now()

	// Seed an entry whose loop_name is "personality-test" but whose
	// message text does not contain that string. pattern="personality-test"
	// must return zero rows; the loop_name filter must return one.
	insertTestLogEntry(t, db, now.Add(-5*time.Second), "INFO", "loop started", "loop-aaa", "personality-test", "loop")

	r := NewEmptyRegistry()
	r.SetLogIndexDB(db)
	tool := r.Get("logs_query")

	t.Run("pattern-only zero-result emits hint", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"pattern": "personality-test",
			"since":   "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, `"returned":0`) {
			t.Fatalf("expected returned:0 for pattern matching no message text, got: %s", result)
		}
		if !strings.Contains(result, `"hint"`) {
			t.Errorf("expected hint field steering toward attribute filters, got: %s", result)
		}
		if !strings.Contains(result, "loop_name") {
			t.Errorf("hint should mention loop_name as an alternative, got: %s", result)
		}
	})

	t.Run("loop_name filter returns the row", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"loop_name": "personality-test",
			"since":     "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, `"returned":1`) {
			t.Errorf("expected returned:1 via loop_name, got: %s", result)
		}
	})

	t.Run("pattern with attribute filter omits hint even when empty", func(t *testing.T) {
		// Attribute filter combined with pattern may legitimately
		// return zero rows — the caller clearly knew which attribute
		// they wanted, so do not second-guess.
		result, err := tool.Handler(nil, map[string]any{
			"pattern":   "does-not-match",
			"loop_name": "personality-test",
			"since":     "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, `"returned":0`) {
			t.Fatalf("expected returned:0, got: %s", result)
		}
		if strings.Contains(result, `"hint"`) {
			t.Errorf("hint should not appear when an attribute filter is already combined, got: %s", result)
		}
	})

	t.Run("non-empty result has no hint", func(t *testing.T) {
		result, err := tool.Handler(nil, map[string]any{
			"pattern": "loop started",
			"since":   "1h",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, `"returned":1`) {
			t.Fatalf("expected returned:1 when pattern actually matches message text, got: %s", result)
		}
		if strings.Contains(result, `"hint"`) {
			t.Errorf("hint should not appear when rows were returned, got: %s", result)
		}
	})
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
