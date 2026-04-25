package signal

import "testing"

func TestNormalizeSignalReactionTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "empty defaults latest", target: "", want: "latest"},
		{name: "latest passes through", target: "latest", want: "latest"},
		{name: "timestamp token", target: "[ts:1700000000000]", want: "1700000000000"},
		{name: "numeric string", target: "1700000000000", want: "1700000000000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSignalReactionTarget(tc.target); got != tc.want {
				t.Fatalf("normalizeSignalReactionTarget(%q) = %q, want %q", tc.target, got, tc.want)
			}
		})
	}
}
