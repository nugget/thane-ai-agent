package timefmt

import (
	"testing"
	"time"
)

func TestFormatDelta(t *testing.T) {
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{
			name: "past 1 hour",
			t:    now.Add(-1 * time.Hour),
			want: "(-3600s)",
		},
		{
			name: "past 54 minutes 7 seconds",
			t:    now.Add(-54*time.Minute - 7*time.Second),
			want: "(-3247s)",
		},
		{
			name: "future 1 hour",
			t:    now.Add(1 * time.Hour),
			want: "2026-03-07T13:00:00Z (+3600s)",
		},
		{
			name: "exactly now",
			t:    now,
			want: "(-0s)",
		},
		{
			name: "past 1 second",
			t:    now.Add(-1 * time.Second),
			want: "(-1s)",
		},
		{
			name: "future 24 hours",
			t:    now.Add(24 * time.Hour),
			want: "2026-03-08T12:00:00Z (+86400s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatDelta(tt.t, now)
			if got != tt.want {
				t.Errorf("FormatDelta() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDeltaOnly(t *testing.T) {
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{
			name: "past 1 hour",
			t:    now.Add(-1 * time.Hour),
			want: "-3600s",
		},
		{
			name: "future 30 minutes",
			t:    now.Add(30 * time.Minute),
			want: "+1800s",
		},
		{
			name: "exactly now",
			t:    now,
			want: "-0s",
		},
		{
			name: "past 1 day",
			t:    now.Add(-24 * time.Hour),
			want: "-86400s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatDeltaOnly(tt.t, now)
			if got != tt.want {
				t.Errorf("FormatDeltaOnly() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTimeOrDelta(t *testing.T) {
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:  "absolute RFC3339",
			input: "2026-03-07T18:00:00Z",
			want:  time.Date(2026, 3, 7, 18, 0, 0, 0, time.UTC),
		},
		{
			name:  "positive offset 1 hour",
			input: "+3600s",
			want:  now.Add(1 * time.Hour),
		},
		{
			name:  "negative offset 5 minutes",
			input: "-300s",
			want:  now.Add(-5 * time.Minute),
		},
		{
			name:  "positive offset with spaces",
			input: "  +60s  ",
			want:  now.Add(1 * time.Minute),
		},
		{
			name:  "zero offset",
			input: "+0s",
			want:  now,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "not-a-timestamp",
			wantErr: true,
		},
		{
			name:    "invalid offset",
			input:   "+abcs",
			wantErr: true,
		},
		{
			name:  "absolute with timezone",
			input: "2026-03-07T12:00:00-06:00",
			want:  time.Date(2026, 3, 7, 18, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTimeOrDelta(tt.input, now)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseTimeOrDelta() = %v, want %v", got, tt.want)
			}
		})
	}
}
