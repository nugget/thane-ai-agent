package email

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/yuin/goldmark"
)

// ComposeOptions holds everything needed to build a complete RFC 5322
// message. The Body field is expected to be markdown.
type ComposeOptions struct {
	// From is the sender address (e.g., "Name <addr@host>").
	From string

	// To is the list of recipient addresses.
	To []string

	// Cc is the list of CC addresses.
	Cc []string

	// Bcc is the list of BCC addresses.
	Bcc []string

	// Subject is the message subject line.
	Subject string

	// Body is the message body in markdown format.
	Body string

	// InReplyTo is the Message-ID of the parent message (for replies).
	InReplyTo string

	// References is the full References chain (for threading).
	References []string
}

// ComposeMessage builds a complete RFC 5322 MIME message from the given
// options. The body markdown is converted to both text/plain and
// text/html parts in a multipart/alternative structure.
func ComposeMessage(opts ComposeOptions) ([]byte, error) {
	var buf bytes.Buffer

	// Build the mail header.
	var h mail.Header

	h.SetDate(time.Now())
	if err := h.GenerateMessageID(); err != nil {
		return nil, fmt.Errorf("generate message-id: %w", err)
	}
	h.SetSubject(opts.Subject)

	from, err := mail.ParseAddress(opts.From)
	if err != nil {
		return nil, fmt.Errorf("parse from address %q: %w", opts.From, err)
	}
	h.SetAddressList("From", []*mail.Address{from})

	toAddrs, err := parseAddressList(opts.To)
	if err != nil {
		return nil, fmt.Errorf("parse to addresses: %w", err)
	}
	h.SetAddressList("To", toAddrs)

	if len(opts.Cc) > 0 {
		ccAddrs, err := parseAddressList(opts.Cc)
		if err != nil {
			return nil, fmt.Errorf("parse cc addresses: %w", err)
		}
		h.SetAddressList("Cc", ccAddrs)
	}

	if len(opts.Bcc) > 0 {
		bccAddrs, err := parseAddressList(opts.Bcc)
		if err != nil {
			return nil, fmt.Errorf("parse bcc addresses: %w", err)
		}
		h.SetAddressList("Bcc", bccAddrs)
	}

	// Threading headers for replies.
	if opts.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{opts.InReplyTo})
	}
	if len(opts.References) > 0 {
		h.SetMsgIDList("References", opts.References)
	}

	// Create the mail writer.
	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return nil, fmt.Errorf("create mail writer: %w", err)
	}

	// Create multipart/alternative inline section.
	tw, err := mw.CreateInline()
	if err != nil {
		return nil, fmt.Errorf("create inline writer: %w", err)
	}

	// text/plain part: markdown stripped to plain text.
	plainText := markdownToPlain(opts.Body)

	var ph mail.InlineHeader
	ph.Set("Content-Type", "text/plain; charset=utf-8")
	pw, err := tw.CreatePart(ph)
	if err != nil {
		return nil, fmt.Errorf("create plain text part: %w", err)
	}
	if _, err := io.WriteString(pw, plainText); err != nil {
		return nil, fmt.Errorf("write plain text: %w", err)
	}
	if err := pw.Close(); err != nil {
		return nil, fmt.Errorf("close plain text part: %w", err)
	}

	// text/html part: markdown rendered to HTML.
	htmlContent, err := markdownToHTML(opts.Body)
	if err != nil {
		return nil, fmt.Errorf("render markdown to HTML: %w", err)
	}

	var hh mail.InlineHeader
	hh.Set("Content-Type", "text/html; charset=utf-8")
	hw, err := tw.CreatePart(hh)
	if err != nil {
		return nil, fmt.Errorf("create html part: %w", err)
	}
	if _, err := io.WriteString(hw, htmlContent); err != nil {
		return nil, fmt.Errorf("write html: %w", err)
	}
	if err := hw.Close(); err != nil {
		return nil, fmt.Errorf("close html part: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close inline writer: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("close mail writer: %w", err)
	}

	return buf.Bytes(), nil
}

// parseAddressList parses a slice of email address strings into
// mail.Address values. Each string can be "Name <addr>" or just "addr".
func parseAddressList(addrs []string) ([]*mail.Address, error) {
	result := make([]*mail.Address, 0, len(addrs))
	for _, a := range addrs {
		parsed, err := mail.ParseAddress(a)
		if err != nil {
			return nil, fmt.Errorf("parse address %q: %w", a, err)
		}
		result = append(result, parsed)
	}
	return result, nil
}

// markdownToHTML renders markdown to an HTML document fragment suitable
// for email. The output is wrapped in minimal HTML structure with no
// external resources.
func markdownToHTML(md string) (string, error) {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return "", err
	}

	// Wrap in minimal HTML envelope.
	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"></head>
<body style="font-family: sans-serif; font-size: 14px; line-height: 1.5;">
%s
</body></html>`, buf.String())

	return html, nil
}

// Patterns for stripping markdown formatting.
var (
	mdBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdItalic     = regexp.MustCompile(`\*(.+?)\*`)
	mdLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	mdHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	mdCodeBlock  = regexp.MustCompile("(?s)```[a-zA-Z]*\n?(.*?)```")
	mdInlineCode = regexp.MustCompile("`([^`]+)`")
)

// markdownToPlain converts markdown to plain text by stripping
// formatting characters while preserving structure.
func markdownToPlain(md string) string {
	s := md

	// Strip code blocks first (preserve content).
	s = mdCodeBlock.ReplaceAllString(s, "$1")

	// Strip inline formatting.
	s = mdImage.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1 ($2)")
	s = mdBold.ReplaceAllString(s, "$1")
	s = mdItalic.ReplaceAllString(s, "$1")
	s = mdInlineCode.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")

	// Clean up list markers — leave them as-is since "- item" and
	// "1. item" are perfectly readable as plain text.

	return strings.TrimSpace(s)
}
