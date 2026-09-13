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

	// Bcc is the list of BCC addresses. They are never written into
	// the message headers; callers put them only in the SMTP envelope.
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

// Composed is a rendered message and the identifiers a caller needs to
// refer to it afterwards.
type Composed struct {
	// Bytes is the complete RFC 5322 message.
	Bytes []byte

	// MessageID is the generated Message-ID without angle brackets. It
	// is the key under which the message can be found in a Sent or
	// Drafts folder and the value a reply's In-Reply-To will carry.
	MessageID string

	// From, To, and Cc are the parsed header addresses.
	From Address
	To   []Address
	Cc   []Address
}

// ComposeMessage builds a complete RFC 5322 MIME message from the given
// options. The body markdown is converted to both text/plain and
// text/html parts in a multipart/alternative structure. The Message-ID
// is generated under the sender's domain so it is stable and
// attributable.
func ComposeMessage(opts ComposeOptions) (Composed, error) {
	var buf bytes.Buffer
	var out Composed

	from, err := parseAddress(opts.From)
	if err != nil {
		return out, fmt.Errorf("from address: %w", err)
	}
	to, err := parseAddresses(opts.To)
	if err != nil {
		return out, fmt.Errorf("to addresses: %w", err)
	}
	cc, err := parseAddresses(opts.Cc)
	if err != nil {
		return out, fmt.Errorf("cc addresses: %w", err)
	}
	out.From, out.To, out.Cc = from, to, cc

	var h mail.Header
	h.SetDate(time.Now())
	if domain := from.Domain(); domain != "" {
		if err := h.GenerateMessageIDWithHostname(domain); err != nil {
			return out, fmt.Errorf("generate message-id: %w", err)
		}
	} else if err := h.GenerateMessageID(); err != nil {
		return out, fmt.Errorf("generate message-id: %w", err)
	}
	out.MessageID, _ = h.MessageID()
	h.SetSubject(opts.Subject)
	h.SetAddressList("From", []*mail.Address{{Name: from.Name, Address: from.Address}})
	h.SetAddressList("To", mailAddresses(to))
	if len(cc) > 0 {
		h.SetAddressList("Cc", mailAddresses(cc))
	}

	// Bcc recipients are intentionally omitted from message headers to
	// avoid leaking blind-copy information. Callers include Bcc
	// addresses only in the SMTP envelope (RCPT TO).

	if opts.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{opts.InReplyTo})
	}
	if len(opts.References) > 0 {
		h.SetMsgIDList("References", opts.References)
	}

	mw, err := mail.CreateWriter(&buf, h)
	if err != nil {
		return out, fmt.Errorf("create mail writer: %w", err)
	}

	tw, err := mw.CreateInline()
	if err != nil {
		return out, fmt.Errorf("create inline writer: %w", err)
	}

	var ph mail.InlineHeader
	ph.Set("Content-Type", "text/plain; charset=utf-8")
	pw, err := tw.CreatePart(ph)
	if err != nil {
		return out, fmt.Errorf("create plain text part: %w", err)
	}
	if _, err := io.WriteString(pw, markdownToPlain(opts.Body)); err != nil {
		return out, fmt.Errorf("write plain text: %w", err)
	}
	if err := pw.Close(); err != nil {
		return out, fmt.Errorf("close plain text part: %w", err)
	}

	htmlContent, err := markdownToHTML(opts.Body)
	if err != nil {
		return out, fmt.Errorf("render markdown to HTML: %w", err)
	}
	var hh mail.InlineHeader
	hh.Set("Content-Type", "text/html; charset=utf-8")
	hw, err := tw.CreatePart(hh)
	if err != nil {
		return out, fmt.Errorf("create html part: %w", err)
	}
	if _, err := io.WriteString(hw, htmlContent); err != nil {
		return out, fmt.Errorf("write html: %w", err)
	}
	if err := hw.Close(); err != nil {
		return out, fmt.Errorf("close html part: %w", err)
	}

	if err := tw.Close(); err != nil {
		return out, fmt.Errorf("close inline writer: %w", err)
	}
	if err := mw.Close(); err != nil {
		return out, fmt.Errorf("close mail writer: %w", err)
	}

	out.Bytes = buf.Bytes()
	return out, nil
}

// mailAddresses converts to go-message's header type.
func mailAddresses(list []Address) []*mail.Address {
	out := make([]*mail.Address, len(list))
	for i, a := range list {
		out[i] = &mail.Address{Name: a.Name, Address: a.Address}
	}
	return out
}

// markdownToHTML renders markdown to an HTML document fragment suitable
// for email. The output is wrapped in minimal HTML structure with no
// external resources.
func markdownToHTML(md string) (string, error) {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return "", err
	}

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
// formatting characters while preserving structure. List markers are
// left alone: "- item" and "1. item" read fine as text.
func markdownToPlain(md string) string {
	s := md
	s = mdCodeBlock.ReplaceAllString(s, "$1")
	s = mdImage.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1 ($2)")
	s = mdBold.ReplaceAllString(s, "$1")
	s = mdItalic.ReplaceAllString(s, "$1")
	s = mdInlineCode.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}
