package email

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/emersion/go-message/textproto"
)

// Auto-Submitted keywords, the closed vocabulary [HeaderMarks] reports.
// The first three are registered (RFC 3834 §5 and RFC 5436 §6.3); any
// other keyword is reported as AutoSubmittedOther, so sender-written
// text is never echoed.
const (
	// AutoSubmittedReplied marks an automatic reply such as an
	// out-of-office notice (RFC 3834 §5).
	AutoSubmittedReplied = "auto-replied"

	// AutoSubmittedGenerated marks a message a machine generated rather
	// than one answering another message (RFC 3834 §5).
	AutoSubmittedGenerated = "auto-generated"

	// AutoSubmittedNotified marks a mail-filter notification (RFC 5436
	// §2.7.1).
	AutoSubmittedNotified = "auto-notified"

	// AutoSubmittedOther is any Auto-Submitted keyword other than "no"
	// and the registered ones.
	AutoSubmittedOther = "other"
)

// HeaderMarks is what one message's own top-level headers claim about
// how it was sent. Nothing authenticates these headers and any sender
// can set or omit them at will, so they can mark a message but never
// vouch for it: they never change a sender's trust_zone or the
// per-address automated key. Their one effect in Go is that
// [Service.Send] refuses an unattended reply to a marked message
// (RFC 3834 §2).
type HeaderMarks struct {
	// AutoSubmitted is the message's Auto-Submitted keyword (RFC 3834
	// §2): one of the AutoSubmitted constants, and empty when the header
	// is absent or says "no".
	AutoSubmitted string `json:"auto_submitted,omitempty"`

	// Bulk reports that the message says it went to many: it carries a
	// List-Id (RFC 2919) or RFC 2369 List-* field, or a Precedence of
	// bulk, list, or junk. RFC 3834 §2 names only Precedence "list", as
	// a heuristic; bulk and junk are de facto convention (RFC 2076).
	Bulk bool `json:"bulk,omitempty"`
}

// Marked reports whether the message carries either mark.
func (m HeaderMarks) Marked() bool {
	return m.AutoSubmitted != "" || m.Bulk
}

// describe names the marks for a refusal sentence, for example "an
// automatic reply (auto-replied)" or "mailing-list or bulk mail".
func (m HeaderMarks) describe() string {
	var parts []string
	switch m.AutoSubmitted {
	case "":
	case AutoSubmittedReplied:
		parts = append(parts, "an automatic reply ("+m.AutoSubmitted+")")
	case AutoSubmittedGenerated:
		parts = append(parts, "a machine-generated message ("+m.AutoSubmitted+")")
	case AutoSubmittedNotified:
		parts = append(parts, "an automatic notification ("+m.AutoSubmitted+")")
	default:
		parts = append(parts, "an automatically submitted message ("+m.AutoSubmitted+")")
	}
	if m.Bulk {
		parts = append(parts, "mailing-list or bulk mail")
	}
	return strings.Join(parts, " and ")
}

// listHeaderFields are the mailing-list fields any one of which marks a
// message bulk: List-Id (RFC 2919) and the RFC 2369 List-* fields.
var listHeaderFields = []string{"List-Id", "List-Help", "List-Unsubscribe", "List-Subscribe", "List-Post", "List-Owner", "List-Archive"}

// headerMarks reads the marks from a raw RFC 5322 message. Only the
// top-level header block is read, so neither a nested message/rfc822
// part nor body text can mark a message. It fails open: a malformed
// header keeps the marks from the fields read before the fault, and the
// error is returned for the caller to log.
func headerMarks(raw []byte) (HeaderMarks, error) {
	var marks HeaderMarks
	if len(raw) == 0 {
		return marks, nil
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if errors.Is(err, io.EOF) {
		// ReadHeader reports io.EOF, keeping the fields before it, when
		// a message with no body ends on an unterminated header line
		// that exactly fills the bufio buffer. That is the end of the
		// header, not a fault worth logging.
		err = nil
	}
	for _, v := range h.Values("Auto-Submitted") {
		if kw := headerKeyword(v); kw != "" && kw != "no" {
			marks.AutoSubmitted = autoSubmittedKeyword(kw)
			break
		}
	}
	for _, field := range listHeaderFields {
		if h.Has(field) {
			marks.Bulk = true
		}
	}
	for _, v := range h.Values("Precedence") {
		switch headerKeyword(v) {
		case "bulk", "list", "junk":
			marks.Bulk = true
		}
	}
	return marks, err
}

// autoSubmittedKeyword maps a lower-cased Auto-Submitted keyword onto
// the closed vocabulary.
func autoSubmittedKeyword(kw string) string {
	switch kw {
	case AutoSubmittedReplied, AutoSubmittedGenerated, AutoSubmittedNotified:
		return kw
	}
	return AutoSubmittedOther
}

// headerKeyword returns the first keyword of a structured header value,
// lower-cased. Leading whitespace and RFC 5322 comments, which may nest
// and hold quoted pairs, are skipped, and the keyword runs to
// whitespace, ';', or '('. It is empty when the value holds no keyword
// or a comment never closes.
func headerKeyword(v string) string {
	for i := 0; i < len(v); {
		switch v[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '(':
			i = skipComment(v, i)
			if i < 0 {
				return ""
			}
		default:
			end := i + strings.IndexAny(v[i:], " \t\r\n;(")
			if end < i {
				end = len(v)
			}
			return strings.ToLower(v[i:end])
		}
	}
	return ""
}

// skipComment returns the index just past the comment that opens at
// v[start], or -1 when it never closes.
func skipComment(v string, start int) int {
	depth := 0
	for i := start; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
