package promptfmt

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{31200, "31,200"},
		{200000, "200,000"},
		{1234567, "1,234,567"},
		{1000000000, "1,000,000,000"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := FormatNumber(tt.input); got != tt.want {
				t.Errorf("FormatNumber(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
