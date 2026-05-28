package main

import (
	"testing"
	"time"
)

func TestParseSinceDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"plain hours", "24h", 24 * time.Hour, false},
		{"days suffix", "7d", 7 * 24 * time.Hour, false},
		{"single day", "1d", 24 * time.Hour, false},
		{"zero days", "0d", 0, false},
		{"minutes", "90m", 90 * time.Minute, false},
		{"compound", "1h30m", 90 * time.Minute, false},
		{"negative days rejected", "-1d", 0, true},
		{"empty rejected", "", 0, true},
		{"garbage rejected", "lots", 0, true},
		{"bad day prefix rejected", "abcd", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSinceDuration(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseSinceDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
