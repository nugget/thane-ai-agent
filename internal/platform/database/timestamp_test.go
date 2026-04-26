package database

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestParseTimestamp(t *testing.T) {
	ref := time.Date(2026, 3, 9, 21, 39, 58, 0, time.UTC)

	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:  "RFC3339",
			input: "2026-03-09T21:39:58Z",
			want:  ref,
		},
		{
			name:  "RFC3339Nano",
			input: "2026-03-09T21:39:58.123456789Z",
			want:  time.Date(2026, 3, 9, 21, 39, 58, 123456789, time.UTC),
		},
		{
			name:  "RFC3339 with offset",
			input: "2026-03-09T21:39:58+00:00",
			want:  ref,
		},
		{
			name:  "T-separated without timezone",
			input: "2026-03-09T21:39:58",
			want:  ref,
		},
		{
			name:  "space-separated (SQLite datetime)",
			input: "2026-03-09 21:39:58",
			want:  ref,
		},
		{
			name:  "space-separated with timezone",
			input: "2026-03-09 21:39:58+00:00",
			want:  ref,
		},
		{
			name:  "trailing whitespace",
			input: "2026-03-09T21:39:58Z\n",
			want:  ref,
		},
		{
			name:  "leading whitespace",
			input: "  2026-03-09 21:39:58  ",
			want:  ref,
		},
		{
			name:    "garbage",
			input:   "not a date",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTimestamp(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseTimestamp(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTimestamp(%q) error: %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseTimestamp(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatTimestamp_RoundTrip(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 4, 25, 10, 0, 0, 123456789, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 999999999, time.FixedZone("CST", -6*3600)),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, ts := range cases {
		s := FormatTimestamp(ts)
		got, err := ParseTimestamp(s)
		if err != nil {
			t.Fatalf("ParseTimestamp(FormatTimestamp(%v)) error: %v (string was %q)", ts, err, s)
		}
		if !got.Equal(ts) {
			t.Errorf("round-trip mismatch: in=%v string=%q parsed=%v", ts, s, got)
		}
	}
}

func TestFormatTimestamp_MatchesDriverBinding(t *testing.T) {
	// Pin FormatTimestamp to go-sqlite3's actual binding output. If the
	// driver ever changes its time.Time → TEXT serialization, this test
	// fails immediately rather than silently rotting the helper.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (ts TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	cases := []time.Time{
		time.Date(2026, 4, 25, 10, 0, 0, 123456789, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 0, time.FixedZone("CST", -6*3600)),
	}
	for _, ts := range cases {
		if _, err := db.Exec("DELETE FROM t"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO t VALUES (?)", ts); err != nil {
			t.Fatalf("insert: %v", err)
		}
		var stored string
		if err := db.QueryRow("SELECT ts FROM t").Scan(&stored); err != nil {
			t.Fatalf("select: %v", err)
		}
		formatted := FormatTimestamp(ts)
		if stored != formatted {
			t.Errorf("driver wrote %q but FormatTimestamp returned %q (instant %v)", stored, formatted, ts)
		}
	}
}
