package email

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// headerBlock joins header lines into a raw message with a short body.
func headerBlock(lines ...string) string {
	return strings.Join(lines, "\r\n") + "\r\n\r\nbody\r\n"
}

// TestHeaderMarks pins what marks a message: an Auto-Submitted keyword
// other than "no", the first such keyword winning, a List-Id or any
// RFC 2369 List-* field, or Precedence bulk, list, or junk, read from
// the top-level header block only and whatever the field name's case.
// The negative controls pin what does not: "no" in any spelling, vendor
// X-Auto* headers, header-shaped text in a subject, a body, or a nested
// message, and a header that fails to parse before any mark.
func TestHeaderMarks(t *testing.T) {
	nested := headerBlock("From: a@example.com", "Subject: fwd", `Content-Type: multipart/mixed; boundary="b1"`)
	nested = strings.TrimSuffix(nested, "body\r\n") +
		"--b1\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--b1\r\nContent-Type: message/rfc822\r\n\r\n" +
		"Auto-Submitted: auto-replied\r\nList-Id: <inner.example.com>\r\nSubject: inner\r\n\r\ninner body\r\n" +
		"--b1--\r\n"

	// A bodiless message whose last, unterminated line exactly fills the
	// 4096-byte bufio buffer makes ReadHeader return io.EOF alongside the
	// fields read before that line.
	bufferFilling := "Auto-Submitted: auto-replied\r\nX-Filler: " + strings.Repeat("a", 4096-len("X-Filler: "))

	tests := []struct {
		name    string
		raw     string
		want    HeaderMarks
		wantErr bool
	}{
		{name: "auto-replied", raw: headerBlock("Auto-Submitted: auto-replied"), want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}},
		{name: "keyword is case-insensitive", raw: headerBlock("Auto-Submitted: Auto-Generated"), want: HeaderMarks{AutoSubmitted: AutoSubmittedGenerated}},
		{name: "field name is case-insensitive", raw: headerBlock("AUTO-SUBMITTED: auto-replied"), want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}},
		{name: "parameters follow the keyword", raw: headerBlock(`Auto-Submitted: auto-replied; owner-email="a@b"`), want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}},
		{name: "leading comment is skipped", raw: headerBlock("Auto-Submitted: (vacation) auto-replied"), want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}},
		{name: "nested comment with a quoted pair is skipped", raw: headerBlock(`Auto-Submitted: ((nested) note \) still) auto-generated`), want: HeaderMarks{AutoSubmitted: AutoSubmittedGenerated}},
		{name: "folded value", raw: headerBlock("Auto-Submitted:", " auto-generated"), want: HeaderMarks{AutoSubmitted: AutoSubmittedGenerated}},
		{name: "auto-notified", raw: headerBlock("Auto-Submitted: auto-notified; owner-token=x"), want: HeaderMarks{AutoSubmitted: AutoSubmittedNotified}},
		{name: "unregistered keyword is other", raw: headerBlock("Auto-Submitted: x-custom"), want: HeaderMarks{AutoSubmitted: AutoSubmittedOther}},
		{name: "no then auto-generated", raw: headerBlock("Auto-Submitted: no", "Auto-Submitted: auto-generated"), want: HeaderMarks{AutoSubmitted: AutoSubmittedGenerated}},
		{name: "first marked keyword wins", raw: headerBlock("Auto-Submitted: auto-replied", "Auto-Submitted: auto-generated"), want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}},
		{name: "List-Id", raw: headerBlock("List-Id: Family <family.lists.example.com>"), want: HeaderMarks{Bulk: true}},
		{name: "List-Help", raw: headerBlock("List-Help: <mailto:help@example.com>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "List-Unsubscribe", raw: headerBlock("List-Unsubscribe: <mailto:leave@example.com>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "List-Subscribe", raw: headerBlock("List-Subscribe: <mailto:join@example.com>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "List-Post", raw: headerBlock("List-Post: <mailto:list@example.com>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "List-Owner", raw: headerBlock("List-Owner: <mailto:owner@example.com>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "List-Archive", raw: headerBlock("List-Archive: <https://lists.example.com/archive>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "lower-case list field name", raw: headerBlock("list-archive: <https://lists.example.com/archive>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarListFields}},
		{name: "Precedence bulk padded and upper-case", raw: headerBlock("Precedence:  BULK "), want: HeaderMarks{Bulk: true}},
		{name: "Precedence list", raw: headerBlock("Precedence: list"), want: HeaderMarks{Bulk: true}},
		{name: "Precedence junk", raw: headerBlock("Precedence: junk"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarJunk}},
		{name: "Precedence junk beside a list field", raw: headerBlock("Precedence: junk", "List-Id: <family.lists.example.com>"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarJunk}},
		{name: "Precedence junk beside Precedence bulk", raw: headerBlock("Precedence: junk", "Precedence: bulk"), want: HeaderMarks{Bulk: true, listReplyBar: listReplyBarJunk}},
		{name: "lower-case precedence field name", raw: headerBlock("precedence: bulk"), want: HeaderMarks{Bulk: true}},
		{name: "both marks", raw: headerBlock("Auto-Submitted: auto-generated", "List-Id: <alerts.example.com>"), want: HeaderMarks{AutoSubmitted: AutoSubmittedGenerated, Bulk: true}},
		{name: "a malformed later line keeps the marks read before it", raw: headerBlock("Auto-Submitted: auto-replied", "this line has no colon", "List-Id: <x.example.com>"), want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}, wantErr: true},
		{name: "a bodiless message ending on a buffer-filling line is not an error", raw: bufferFilling, want: HeaderMarks{AutoSubmitted: AutoSubmittedReplied}},

		{name: "no", raw: headerBlock("Auto-Submitted: no")},
		{name: "No with a comment", raw: headerBlock("Auto-Submitted: No (human)")},
		{name: "comment then no", raw: headerBlock("Auto-Submitted: (x) no")},
		{name: "empty value", raw: headerBlock("Auto-Submitted:")},
		{name: "only a comment", raw: headerBlock("Auto-Submitted: (only a comment)")},
		{name: "unclosed comment", raw: headerBlock("Auto-Submitted: (vacation auto-replied")},
		{name: "no relevant headers", raw: headerBlock("From: a@example.com", "Subject: hello")},
		{name: "Precedence first-class", raw: headerBlock("Precedence: first-class")},
		{name: "vendor X-Auto headers are not counted", raw: headerBlock("X-Autoreply: yes", "X-Auto-Response-Suppress: All", "X-Autorespond: yes")},
		{name: "header text in a subject", raw: headerBlock("Subject: Auto-Submitted: auto-replied")},
		{name: "header text in the body", raw: "From: a@example.com\r\nSubject: hi\r\n\r\nAuto-Submitted: auto-replied\r\nList-Id: <x.example.com>\r\n"},
		{name: "nested message/rfc822 part", raw: nested},
		{name: "malformed first line", raw: " folded first line\r\nAuto-Submitted: auto-replied\r\n\r\nbody\r\n", wantErr: true},
		{name: "nil raw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			if tt.raw != "" {
				raw = []byte(tt.raw)
			}
			got, err := headerMarks(raw)
			if got != tt.want {
				t.Errorf("headerMarks = %+v, want %+v", got, tt.want)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("headerMarks error = %v, want error %v", err, tt.wantErr)
			}
			if got.Marked() != (tt.want != HeaderMarks{}) {
				t.Errorf("Marked = %v for %+v", got.Marked(), got)
			}
		})
	}
}

// TestHeaderMarksDescribe pins the refusal phrase for each mark.
func TestHeaderMarksDescribe(t *testing.T) {
	tests := []struct {
		marks HeaderMarks
		want  string
	}{
		{HeaderMarks{AutoSubmitted: AutoSubmittedReplied}, "an automatic reply (auto-replied)"},
		{HeaderMarks{AutoSubmitted: AutoSubmittedGenerated}, "a machine-generated message (auto-generated)"},
		{HeaderMarks{AutoSubmitted: AutoSubmittedNotified}, "an automatic notification (auto-notified)"},
		{HeaderMarks{AutoSubmitted: AutoSubmittedOther}, "an automatically submitted message (other)"},
		{HeaderMarks{Bulk: true}, "mailing-list or bulk mail"},
		{HeaderMarks{AutoSubmitted: AutoSubmittedReplied, Bulk: true}, "an automatic reply (auto-replied) and mailing-list or bulk mail"},
	}
	for _, tt := range tests {
		if got := tt.marks.describe(); got != tt.want {
			t.Errorf("describe(%+v) = %q, want %q", tt.marks, got, tt.want)
		}
	}
}

// TestReadMessageKeepsHeaderMarksThroughABodyParseError pins that a full
// read reports a message's header marks even when its body fails the
// MIME parse, which ReadMessage only logs.
func TestReadMessageKeepsHeaderMarksThroughABodyParseError(t *testing.T) {
	mem := newMemIMAP(t)
	raw := "From: Alerts <alerts@example.com>\r\nTo: thane@example.com\r\nSubject: broken\r\n" +
		"Message-ID: <broken@example.com>\r\nAuto-Submitted: auto-generated\r\nPrecedence: bulk\r\n" +
		"Content-Type: multipart/mixed\r\n\r\nno boundary anywhere\r\n"
	uid := mem.append("INBOX", raw)
	c := mem.newClient("primary")

	if err := c.parseBody(&Message{}, bytes.NewReader([]byte(raw))); err == nil {
		t.Fatal("precondition: the fixture must fail the MIME parse")
	}
	msg, err := c.ReadMessage(context.Background(), ReadOptions{Folder: "INBOX", UID: uid, Peek: true})
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if want := (HeaderMarks{AutoSubmitted: AutoSubmittedGenerated, Bulk: true}); msg.HeaderMarks != want {
		t.Errorf("HeaderMarks = %+v, want %+v", msg.HeaderMarks, want)
	}
}
