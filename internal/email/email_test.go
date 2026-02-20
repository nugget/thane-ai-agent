package email

import "testing"

func TestValidFlag(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		wantIMAP string
		wantOK   bool
	}{
		{"seen", "seen", `\Seen`, true},
		{"flagged", "flagged", `\Flagged`, true},
		{"answered", "answered", `\Answered`, true},
		{"invalid", "deleted", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIMAP, gotOK := ValidFlag(tt.flag)
			if gotOK != tt.wantOK {
				t.Errorf("ValidFlag(%q) ok = %v, want %v", tt.flag, gotOK, tt.wantOK)
			}
			if gotIMAP != tt.wantIMAP {
				t.Errorf("ValidFlag(%q) = %q, want %q", tt.flag, gotIMAP, tt.wantIMAP)
			}
		})
	}
}

func TestEnvelope_Defaults(t *testing.T) {
	var env Envelope
	if env.UID != 0 {
		t.Errorf("zero-value UID should be 0, got %d", env.UID)
	}
	if env.From != "" {
		t.Errorf("zero-value From should be empty, got %q", env.From)
	}
	if len(env.Flags) != 0 {
		t.Errorf("zero-value Flags should be nil, got %v", env.Flags)
	}
}

func TestMessage_Embeds_Envelope(t *testing.T) {
	msg := Message{
		Envelope: Envelope{
			UID:     42,
			Subject: "Test",
		},
		TextBody: "Hello",
	}
	if msg.UID != 42 {
		t.Errorf("embedded UID = %d, want 42", msg.UID)
	}
	if msg.Subject != "Test" {
		t.Errorf("embedded Subject = %q, want %q", msg.Subject, "Test")
	}
	if msg.TextBody != "Hello" {
		t.Errorf("TextBody = %q, want %q", msg.TextBody, "Hello")
	}
}
