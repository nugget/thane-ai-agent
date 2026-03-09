package database

import (
	"testing"
	"time"
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
