package provenance

import (
	"testing"
)

// Trailers reach the reader as one unfolded, separator-joined line, because a
// revision log line must stay a line. These cases pin what that line is allowed
// to contain and what must never become a phantom trailer.
func TestParseTrailers(t *testing.T) {
	tests := []struct {
		name     string
		rendered string
		want     map[string]string
	}{
		{
			name: "no trailers stays nil so absence is distinguishable",
		},
		{
			name:     "whitespace only is not a trailer",
			rendered: "   \n  ",
		},
		{
			name:     "a single trailer",
			rendered: "Thane-Model: gpt-oss:120b",
			want:     map[string]string{"Thane-Model": "gpt-oss:120b"},
		},
		{
			name:     "separator-joined trailers split cleanly",
			rendered: "Thane-Model: gpt-oss:120b" + trailerSeparator + "Thane-Iteration: 3",
			want: map[string]string{
				"Thane-Model":     "gpt-oss:120b",
				"Thane-Iteration": "3",
			},
		},
		{
			name:     "a value containing a colon survives intact",
			rendered: "Thane-Request: r_abc" + trailerSeparator + "Thane-Model: lmstudio/qwen:3.5",
			want: map[string]string{
				"Thane-Request": "r_abc",
				"Thane-Model":   "lmstudio/qwen:3.5",
			},
		},
		{
			name:     "an entry with no colon is skipped, not guessed at",
			rendered: "not a trailer" + trailerSeparator + "Thane-Model: gpt-oss:120b",
			want:     map[string]string{"Thane-Model": "gpt-oss:120b"},
		},
		{
			name:     "an empty key or value is not a fact",
			rendered: ": orphaned" + trailerSeparator + "Thane-Session:  " + trailerSeparator + "Thane-Model: m",
			want:     map[string]string{"Thane-Model": "m"},
		},
		{
			name:     "trailers a future build never heard of are still returned",
			rendered: "Reconstructed-From: thane.db tool_calls" + trailerSeparator + "Thane-Model: m",
			want: map[string]string{
				"Reconstructed-From": "thane.db tool_calls",
				"Thane-Model":        "m",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTrailers(tc.rendered)
			if len(tc.want) == 0 {
				if got != nil {
					t.Fatalf("expected nil for %q, got %v", tc.rendered, got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("trailer count: got %v, want %v", got, tc.want)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("trailer %s: got %q, want %q", key, got[key], want)
				}
			}
		})
	}
}
