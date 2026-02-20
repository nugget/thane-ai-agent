package email

import (
	"strings"
	"testing"
)

func TestMarkdownToPlain(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want string
	}{
		{
			name: "bold",
			md:   "This is **bold** text",
			want: "This is bold text",
		},
		{
			name: "italic",
			md:   "This is *italic* text",
			want: "This is italic text",
		},
		{
			name: "link",
			md:   "Visit [Example](https://example.com) now",
			want: "Visit Example (https://example.com) now",
		},
		{
			name: "heading",
			md:   "## Section Title\n\nSome text",
			want: "Section Title\n\nSome text",
		},
		{
			name: "inline code",
			md:   "Use the `fmt.Println` function",
			want: "Use the fmt.Println function",
		},
		{
			name: "code block",
			md:   "Before\n```go\nfmt.Println(\"hello\")\n```\nAfter",
			want: "Before\nfmt.Println(\"hello\")\n\nAfter",
		},
		{
			name: "image",
			md:   "See ![alt text](https://example.com/img.png) here",
			want: "See alt text here",
		},
		{
			name: "list items preserved",
			md:   "- item one\n- item two\n- item three",
			want: "- item one\n- item two\n- item three",
		},
		{
			name: "plain text unchanged",
			md:   "Just some regular text.",
			want: "Just some regular text.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownToPlain(tt.md)
			if got != tt.want {
				t.Errorf("markdownToPlain(%q) =\n  %q\nwant\n  %q", tt.md, got, tt.want)
			}
		})
	}
}

func TestMarkdownToHTML(t *testing.T) {
	html, err := markdownToHTML("Hello **world**")
	if err != nil {
		t.Fatalf("markdownToHTML() error: %v", err)
	}

	if !strings.Contains(html, "<strong>world</strong>") {
		t.Error("HTML should contain <strong> tag for bold")
	}
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("HTML should have DOCTYPE wrapper")
	}
	if !strings.Contains(html, "charset=\"utf-8\"") && !strings.Contains(html, "charset=utf-8") {
		t.Error("HTML should declare utf-8 charset")
	}
}

func TestComposeMessage(t *testing.T) {
	msg, err := ComposeMessage(ComposeOptions{
		From:    "Test User <test@example.com>",
		To:      []string{"recipient@example.com"},
		Subject: "Test Subject",
		Body:    "Hello **world**",
	})
	if err != nil {
		t.Fatalf("ComposeMessage() error: %v", err)
	}

	s := string(msg)

	// Check required headers.
	// go-message quotes display names: From: "Test User" <test@example.com>.
	if !strings.Contains(s, "From:") || !strings.Contains(s, "test@example.com") {
		t.Errorf("message should contain From header with address, got headers:\n%s", s[:min(len(s), 500)])
	}
	if !strings.Contains(s, "To:") || !strings.Contains(s, "recipient@example.com") {
		t.Errorf("message should contain To header with address, got headers:\n%s", s[:min(len(s), 500)])
	}
	if !strings.Contains(s, "Subject: Test Subject") {
		t.Error("message should contain Subject header")
	}
	if !strings.Contains(s, "Message-Id:") {
		t.Error("message should contain Message-Id header")
	}
	if !strings.Contains(s, "Date:") {
		t.Error("message should contain Date header")
	}

	// Check multipart/alternative structure.
	if !strings.Contains(s, "multipart/alternative") {
		t.Error("message should be multipart/alternative")
	}
	if !strings.Contains(s, "text/plain") {
		t.Error("message should contain text/plain part")
	}
	if !strings.Contains(s, "text/html") {
		t.Error("message should contain text/html part")
	}
}

func TestComposeMessage_WithThreading(t *testing.T) {
	msg, err := ComposeMessage(ComposeOptions{
		From:       "Test User <test@example.com>",
		To:         []string{"recipient@example.com"},
		Subject:    "Re: Original Subject",
		Body:       "Reply body",
		InReplyTo:  "abc123@example.com",
		References: []string{"parent@example.com", "abc123@example.com"},
	})
	if err != nil {
		t.Fatalf("ComposeMessage() error: %v", err)
	}

	s := string(msg)

	if !strings.Contains(s, "In-Reply-To:") {
		t.Error("reply message should contain In-Reply-To header")
	}
	if !strings.Contains(s, "References:") {
		t.Error("reply message should contain References header")
	}
}

func TestComposeMessage_WithCcBcc(t *testing.T) {
	msg, err := ComposeMessage(ComposeOptions{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Cc:      []string{"cc@example.com"},
		Bcc:     []string{"bcc@example.com"},
		Subject: "Test",
		Body:    "Body",
	})
	if err != nil {
		t.Fatalf("ComposeMessage() error: %v", err)
	}

	s := string(msg)

	if !strings.Contains(s, "Cc:") {
		t.Error("message should contain Cc header")
	}
	if !strings.Contains(s, "Bcc:") {
		t.Error("message should contain Bcc header")
	}
}

func TestComposeMessage_InvalidFrom(t *testing.T) {
	_, err := ComposeMessage(ComposeOptions{
		From:    "not-an-email",
		To:      []string{"to@example.com"},
		Subject: "Test",
		Body:    "Body",
	})
	if err == nil {
		t.Error("ComposeMessage should fail with invalid From address")
	}
}
