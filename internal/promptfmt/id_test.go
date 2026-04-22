package promptfmt

import "testing"

func TestShortIDPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"short", "abc", "abc"},
		{"exactly eight", "12345678", "12345678"},
		{"longer than eight", "0123456789abcdef", "01234567"},
		{"multi-byte runes", "αβγδεζηθικ", "αβγδεζηθ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShortIDPrefix(tt.input); got != tt.want {
				t.Errorf("ShortIDPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestShortIDSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"short", "abc", "abc"},
		{"exactly eight", "12345678", "12345678"},
		{"uuid-shaped", "019c741c-abcd-ef01", "bcd-ef01"},
		{"owu prefixed", "owu-9da7e5cb-1234-5678-abcd-ef0123456789", "23456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShortIDSuffix(tt.input); got != tt.want {
				t.Errorf("ShortIDSuffix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
