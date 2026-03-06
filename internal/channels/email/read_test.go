package email

import (
	"log/slog"
	"strings"
	"testing"
)

// testClient returns a Client with only a logger, suitable for testing
// parseBody without an IMAP connection.
func testClient() *Client {
	return &Client{logger: slog.Default()}
}

// simplePlainText is a single-part plain text message.
const simplePlainText = "From: sender@example.com\r\n" +
	"To: recipient@example.com\r\n" +
	"Subject: Simple\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello, world!\r\n"

// nestedMultipartAlternative is a typical Gmail/Apple Mail structure:
// multipart/mixed → multipart/alternative → text/plain + text/html.
const nestedMultipartAlternative = "From: sender@example.com\r\n" +
	"To: recipient@example.com\r\n" +
	"Subject: Nested\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
	"\r\n" +
	"--inner\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Plain text body\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>HTML body</p>\r\n" +
	"--inner--\r\n" +
	"--outer--\r\n"

// multipartAlternativeOnly is multipart/alternative at the top level
// (no outer multipart/mixed wrapper).
const multipartAlternativeOnly = "From: sender@example.com\r\n" +
	"To: recipient@example.com\r\n" +
	"Subject: Alternative\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"alt\"\r\n" +
	"\r\n" +
	"--alt\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Plain alternative\r\n" +
	"--alt\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>HTML alternative</p>\r\n" +
	"--alt--\r\n"

// tripleNested is a deeply nested structure:
// multipart/mixed → multipart/related → multipart/alternative → text/plain + text/html.
const tripleNested = "From: sender@example.com\r\n" +
	"To: recipient@example.com\r\n" +
	"Subject: Triple\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"b1\"\r\n" +
	"\r\n" +
	"--b1\r\n" +
	"Content-Type: multipart/related; boundary=\"b2\"\r\n" +
	"\r\n" +
	"--b2\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b3\"\r\n" +
	"\r\n" +
	"--b3\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Deep plain text\r\n" +
	"--b3\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Deep HTML</p>\r\n" +
	"--b3--\r\n" +
	"--b2--\r\n" +
	"--b1--\r\n"

// withReferences includes a References header for threading.
const withReferences = "From: sender@example.com\r\n" +
	"To: recipient@example.com\r\n" +
	"Subject: Reply\r\n" +
	"References: <abc@example.com> <def@example.com>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"This is a reply.\r\n"

func TestParseBody_SimplePlainText(t *testing.T) {
	c := testClient()
	msg := &Message{}

	if err := c.parseBody(msg, strings.NewReader(simplePlainText)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	if msg.TextBody != "Hello, world!" {
		t.Errorf("TextBody = %q, want %q", msg.TextBody, "Hello, world!")
	}
	if msg.HTMLBody != "" {
		t.Errorf("HTMLBody = %q, want empty", msg.HTMLBody)
	}
}

func TestParseBody_NestedMultipartAlternative(t *testing.T) {
	c := testClient()
	msg := &Message{}

	if err := c.parseBody(msg, strings.NewReader(nestedMultipartAlternative)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	if msg.TextBody != "Plain text body" {
		t.Errorf("TextBody = %q, want %q", msg.TextBody, "Plain text body")
	}
	if msg.HTMLBody != "<p>HTML body</p>" {
		t.Errorf("HTMLBody = %q, want %q", msg.HTMLBody, "<p>HTML body</p>")
	}
}

func TestParseBody_MultipartAlternativeOnly(t *testing.T) {
	c := testClient()
	msg := &Message{}

	if err := c.parseBody(msg, strings.NewReader(multipartAlternativeOnly)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	if msg.TextBody != "Plain alternative" {
		t.Errorf("TextBody = %q, want %q", msg.TextBody, "Plain alternative")
	}
	if msg.HTMLBody != "<p>HTML alternative</p>" {
		t.Errorf("HTMLBody = %q, want %q", msg.HTMLBody, "<p>HTML alternative</p>")
	}
}

func TestParseBody_TripleNested(t *testing.T) {
	c := testClient()
	msg := &Message{}

	if err := c.parseBody(msg, strings.NewReader(tripleNested)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	if msg.TextBody != "Deep plain text" {
		t.Errorf("TextBody = %q, want %q", msg.TextBody, "Deep plain text")
	}
	if msg.HTMLBody != "<p>Deep HTML</p>" {
		t.Errorf("HTMLBody = %q, want %q", msg.HTMLBody, "<p>Deep HTML</p>")
	}
}

func TestParseBody_References(t *testing.T) {
	c := testClient()
	msg := &Message{}

	if err := c.parseBody(msg, strings.NewReader(withReferences)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	if len(msg.References) != 2 {
		t.Fatalf("References len = %d, want 2", len(msg.References))
	}
	if msg.References[0] != "abc@example.com" {
		t.Errorf("References[0] = %q, want %q", msg.References[0], "abc@example.com")
	}
	if msg.References[1] != "def@example.com" {
		t.Errorf("References[1] = %q, want %q", msg.References[1], "def@example.com")
	}
}

func TestParseBody_UnknownCharset(t *testing.T) {
	// Messages with unknown charsets should still have their body
	// extracted (possibly garbled) rather than returning empty.
	// The go-message library returns both a valid reader AND an error
	// for unknown charsets — we must not discard the reader.
	c := testClient()
	msg := &Message{}

	// Use a fictitious charset that go-message won't recognize.
	raw := "From: sender@example.com\r\n" +
		"Content-Type: text/plain; charset=x-fake-charset\r\n" +
		"\r\n" +
		"Body with unknown charset\r\n"

	err := c.parseBody(msg, strings.NewReader(raw))
	// Should not return a fatal error.
	if err != nil {
		t.Fatalf("parseBody should not fail for unknown charset: %v", err)
	}
	// Body should still be extracted (raw bytes, possibly not decoded).
	if msg.TextBody == "" {
		t.Error("TextBody should not be empty for unknown charset — content should be preserved as-is")
	}
}

func TestParseBody_UnknownCharsetInPart(t *testing.T) {
	// Unknown charset in a nested part should not abort parsing of
	// other parts.
	c := testClient()
	msg := &Message{}

	raw := "From: sender@example.com\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"cs\"\r\n" +
		"\r\n" +
		"--cs\r\n" +
		"Content-Type: text/plain; charset=x-nonexistent\r\n" +
		"\r\n" +
		"Garbled plain text\r\n" +
		"--cs\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>Clean HTML</p>\r\n" +
		"--cs--\r\n"

	if err := c.parseBody(msg, strings.NewReader(raw)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	// The text part may have garbled content, but should not be empty.
	if msg.TextBody == "" {
		t.Error("TextBody should be populated even with unknown charset")
	}
	if msg.HTMLBody != "<p>Clean HTML</p>" {
		t.Errorf("HTMLBody = %q, want %q", msg.HTMLBody, "<p>Clean HTML</p>")
	}
}

func TestParseBody_Truncation(t *testing.T) {
	c := testClient()
	msg := &Message{}

	// Build a message with body > maxBodySize.
	bigBody := strings.Repeat("X", maxBodySize+100)
	raw := "From: sender@example.com\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		bigBody + "\r\n"

	if err := c.parseBody(msg, strings.NewReader(raw)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}

	if !strings.Contains(msg.TextBody, "[truncated") {
		t.Error("large body should contain truncation marker")
	}
	if len(msg.TextBody) > maxBodySize+200 {
		t.Errorf("TextBody len = %d, should be bounded near maxBodySize", len(msg.TextBody))
	}
}
