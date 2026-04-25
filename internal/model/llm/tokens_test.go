package llm

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"1234", 1},
		{"12345678", 2},
		{"", 0},
		{"abc", 0},
	}

	for _, tc := range tests {
		if got := EstimateTokens(tc.input); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
