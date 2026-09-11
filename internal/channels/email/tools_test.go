package email

import (
	"testing"
	"time"
)

// TestParseMarkAction exercises the args→MarkAction translation that
// HandleMark uses. The omitted-`add`-defaults-to-true row is the
// regression guard for #930: a handler that reverted to
// `toolargs.Bool(args, "add")` (false-default) would silently flip this
// row's expected Add from true back to false.
func TestParseMarkAction(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want MarkAction
	}{
		{
			name: "omitted add defaults to true (#930 regression guard)",
			args: map[string]any{"uid": float64(123), "flag": "seen"},
			want: MarkAction{Flag: "seen", Add: true, UIDs: []uint32{123}},
		},
		{
			name: "explicit add=false overrides default",
			args: map[string]any{"uid": float64(123), "flag": "seen", "add": false},
			want: MarkAction{Flag: "seen", Add: false, UIDs: []uint32{123}},
		},
		{
			name: "explicit add=true matches default",
			args: map[string]any{"uid": float64(123), "flag": "seen", "add": true},
			want: MarkAction{Flag: "seen", Add: true, UIDs: []uint32{123}},
		},
		{
			name: "uids array preferred over uid",
			args: map[string]any{
				"uids": []any{float64(10), float64(20)},
				"flag": "flagged",
			},
			want: MarkAction{Flag: "flagged", Add: true, UIDs: []uint32{10, 20}},
		},
		{
			name: "missing uids leaves UIDs nil for handler to reject",
			args: map[string]any{"flag": "seen"},
			want: MarkAction{Flag: "seen", Add: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMarkAction(tt.args)
			if got.Flag != tt.want.Flag {
				t.Errorf("Flag = %q, want %q", got.Flag, tt.want.Flag)
			}
			if got.Add != tt.want.Add {
				t.Errorf("Add = %v, want %v", got.Add, tt.want.Add)
			}
			if !uint32SliceEqual(got.UIDs, tt.want.UIDs) {
				t.Errorf("UIDs = %v, want %v", got.UIDs, tt.want.UIDs)
			}
		})
	}
}

func uint32SliceEqual(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseSearchDate(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"2026-09-01", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), false},
		{"2026-09-01T08:00:00Z", time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), false},
		{"-7d", now.Add(-7 * 24 * time.Hour), false},
		{"yesterday", time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSearchDate(tc.in, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !got.Equal(tc.want) {
				t.Errorf("parseSearchDate(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMissingUIDs(t *testing.T) {
	got := missingUIDs([]uint32{1, 2, 3}, []uint32{1, 3})
	if fmtUIDs(got) != fmtUIDs([]uint32{2}) {
		t.Errorf("missingUIDs = %v", got)
	}
}
