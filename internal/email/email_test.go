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

func TestMessage_ThreadingFields(t *testing.T) {
	msg := Message{
		MessageID:  "abc123@example.com",
		InReplyTo:  []string{"parent@example.com"},
		References: []string{"root@example.com", "parent@example.com"},
		Cc:         []string{"cc@example.com"},
		ReplyTo:    "reply@example.com",
	}
	if msg.MessageID != "abc123@example.com" {
		t.Errorf("MessageID = %q, want %q", msg.MessageID, "abc123@example.com")
	}
	if len(msg.InReplyTo) != 1 {
		t.Errorf("InReplyTo length = %d, want 1", len(msg.InReplyTo))
	}
	if len(msg.References) != 2 {
		t.Errorf("References length = %d, want 2", len(msg.References))
	}
	if len(msg.Cc) != 1 {
		t.Errorf("Cc length = %d, want 1", len(msg.Cc))
	}
	if msg.ReplyTo != "reply@example.com" {
		t.Errorf("ReplyTo = %q, want %q", msg.ReplyTo, "reply@example.com")
	}
}

func TestSendOptions(t *testing.T) {
	opts := SendOptions{
		To:      []string{"to@example.com"},
		Cc:      []string{"cc@example.com"},
		Subject: "Test",
		Body:    "Hello",
		Account: "personal",
	}
	if len(opts.To) != 1 {
		t.Errorf("To length = %d, want 1", len(opts.To))
	}
	if len(opts.Cc) != 1 {
		t.Errorf("Cc length = %d, want 1", len(opts.Cc))
	}
	if opts.Subject != "Test" {
		t.Errorf("Subject = %q, want %q", opts.Subject, "Test")
	}
	if opts.Body != "Hello" {
		t.Errorf("Body = %q, want %q", opts.Body, "Hello")
	}
	if opts.Account != "personal" {
		t.Errorf("Account = %q, want %q", opts.Account, "personal")
	}
}

func TestMoveOptions(t *testing.T) {
	opts := MoveOptions{
		UIDs:        []uint32{100, 200},
		Folder:      "INBOX",
		Destination: "Archive",
		Account:     "work",
	}
	if len(opts.UIDs) != 2 {
		t.Errorf("UIDs length = %d, want 2", len(opts.UIDs))
	}
	if opts.Folder != "INBOX" {
		t.Errorf("Folder = %q, want %q", opts.Folder, "INBOX")
	}
	if opts.Destination != "Archive" {
		t.Errorf("Destination = %q, want %q", opts.Destination, "Archive")
	}
	if opts.Account != "work" {
		t.Errorf("Account = %q, want %q", opts.Account, "work")
	}
}
