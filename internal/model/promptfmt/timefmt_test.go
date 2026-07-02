package promptfmt

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
			want: "(-1h)",
		},
		{
			name: "past 54 minutes 7 seconds",
			t:    now.Add(-54*time.Minute - 7*time.Second),
			want: "(-3247s)",
		},
		{
			name: "future 1 hour",
			t:    now.Add(1 * time.Hour),
			want: "2026-03-07T13:00:00Z (+1h)",
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
			want: "2026-03-08T12:00:00Z (+24h)",
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
			want: "-1h",
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
			name: "past 1 day renders hours under the two-day tier",
			t:    now.Add(-24 * time.Hour),
			want: "-24h",
		},
		{
			name: "past 59 minutes 59 seconds stays exact seconds",
			t:    now.Add(-59*time.Minute - 59*time.Second),
			want: "-3599s",
		},
		{
			name: "past 26 hours 45 minutes",
			t:    now.Add(-26*time.Hour - 45*time.Minute - 30*time.Second),
			want: "-26h45m",
		},
		{
			name: "past 47 hours 59 minutes stays hours",
			t:    now.Add(-47*time.Hour - 59*time.Minute),
			want: "-47h59m",
		},
		{
			name: "past 5 days 9 hours",
			t:    now.Add(-5*24*time.Hour - 9*time.Hour - 51*time.Minute),
			want: "-5d9h",
		},
		{
			name: "past exactly 3 days",
			t:    now.Add(-3 * 24 * time.Hour),
			want: "-3d",
		},
		{
			name: "future 2 hours 30 minutes",
			t:    now.Add(2*time.Hour + 30*time.Minute),
			want: "+2h30m",
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
		{
			name:  "negative offset minutes unit",
			input: "-30m",
			want:  now.Add(-30 * time.Minute),
		},
		{
			name:  "negative offset hours unit",
			input: "-24h",
			want:  now.Add(-24 * time.Hour),
		},
		{
			name:  "negative offset days unit",
			input: "-7d",
			want:  now.Add(-7 * 24 * time.Hour),
		},
		{
			name:  "positive offset weeks unit",
			input: "+2w",
			want:  now.Add(2 * 7 * 24 * time.Hour),
		},
		{
			name:    "unknown unit",
			input:   "-7y",
			wantErr: true,
		},
		{
			name:  "compound days and hours",
			input: "-5d9h",
			want:  now.Add(-5*24*time.Hour - 9*time.Hour),
		},
		{
			name:  "compound hours and minutes",
			input: "-26h45m",
			want:  now.Add(-26*time.Hour - 45*time.Minute),
		},
		{
			name:  "compound future",
			input: "+2h30m",
			want:  now.Add(2*time.Hour + 30*time.Minute),
		},
		{
			name:    "compound with trailing digits missing unit",
			input:   "-5d9",
			wantErr: true,
		},
		{
			name:    "compound with bad unit in second term",
			input:   "-5d9y",
			wantErr: true,
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
