package main

import "testing"

func TestParseFarewellResponse(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantFarewell string
		wantCarry    string
		wantEmpty    bool // if true, both fields should be empty
	}{
		{
			name:         "valid JSON",
			content:      `{"farewell": "Goodbye!", "carry_forward": "User discussed testing."}`,
			wantFarewell: "Goodbye!",
			wantCarry:    "User discussed testing.",
		},
		{
			name:         "code-fenced JSON",
			content:      "```json\n{\"farewell\": \"See you!\", \"carry_forward\": \"Notes here.\"}\n```",
			wantFarewell: "See you!",
			wantCarry:    "Notes here.",
		},
		{
			name:         "bare code fence",
			content:      "```\n{\"farewell\": \"Later!\", \"carry_forward\": \"Context.\"}\n```",
			wantFarewell: "Later!",
			wantCarry:    "Context.",
		},
		{
			name:      "malformed JSON",
			content:   "This is not JSON at all",
			wantEmpty: true,
		},
		{
			name:      "empty string",
			content:   "",
			wantEmpty: true,
		},
		{
			name:         "extra whitespace",
			content:      "  \n {\"farewell\": \"Bye!\", \"carry_forward\": \"Notes.\"} \n  ",
			wantFarewell: "Bye!",
			wantCarry:    "Notes.",
		},
		{
			name:         "missing carry_forward",
			content:      `{"farewell": "Goodbye!"}`,
			wantFarewell: "Goodbye!",
			wantCarry:    "",
		},
		{
			name:         "missing farewell",
			content:      `{"carry_forward": "Notes."}`,
			wantFarewell: "",
			wantCarry:    "Notes.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			farewell, carry := parseFarewellResponse(tt.content)

			if tt.wantEmpty {
				if farewell != "" || carry != "" {
					t.Errorf("expected empty, got farewell=%q carry=%q", farewell, carry)
				}
				return
			}

			if farewell != tt.wantFarewell {
				t.Errorf("farewell = %q, want %q", farewell, tt.wantFarewell)
			}
			if carry != tt.wantCarry {
				t.Errorf("carry_forward = %q, want %q", carry, tt.wantCarry)
			}
		})
	}
}
