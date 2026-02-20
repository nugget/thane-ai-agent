package email

import "testing"

func TestExtractAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare address", "user@example.com", "user@example.com"},
		{"name and address", "Alice <alice@example.com>", "alice@example.com"},
		{"just angle brackets", "<user@test.com>", "user@test.com"},
		{"empty", "", ""},
		{"no closing bracket", "Alice <user@test.com", "Alice <user@test.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAddress(tt.input)
			if got != tt.want {
				t.Errorf("extractAddress(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCollectRecipients(t *testing.T) {
	result := collectRecipients(
		[]string{"Alice <alice@example.com>", "bob@example.com"},
		[]string{"cc@example.com"},
		[]string{"bcc@example.com", "alice@example.com"}, // duplicate of alice
	)

	// Should have 4 unique addresses (alice deduplicated).
	if len(result) != 4 {
		t.Errorf("collectRecipients = %d addresses, want 4: %v", len(result), result)
	}

	// Check that alice appears only once.
	count := 0
	for _, r := range result {
		if r == "alice@example.com" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("alice should appear once, got %d", count)
	}
}

func TestCollectRecipients_Empty(t *testing.T) {
	result := collectRecipients(nil, nil, nil)
	if len(result) != 0 {
		t.Errorf("empty inputs should return empty, got %v", result)
	}
}
