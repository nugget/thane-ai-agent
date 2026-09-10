package email

import (
	"strings"
	"testing"
	"time"
)

// TestParseMarkAction exercises the args→MarkAction translation that
// HandleMark uses. The omitted-`add`-defaults-to-true row is the
// regression guard for #930: a handler that reverted to
// `toolargs.Bool(args, "add")` (false-default) would silently flip this
// row's expected Add from true back to false.
func TestParseMarkAction(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want MarkAction
	}{
		{
			name: "omitted add defaults to true (#930 regression guard)",
			args: map[string]any{"uid": float64(123), "flag": "seen"},
			want: MarkAction{Flag: "seen", Add: true, UIDs: []uint32{123}},
		},
		{
			name: "explicit add=false overrides default",
			args: map[string]any{"uid": float64(123), "flag": "seen", "add": false},
			want: MarkAction{Flag: "seen", Add: false, UIDs: []uint32{123}},
		},
		{
			name: "explicit add=true matches default",
			args: map[string]any{"uid": float64(123), "flag": "seen", "add": true},
			want: MarkAction{Flag: "seen", Add: true, UIDs: []uint32{123}},
		},
		{
			name: "uids array preferred over uid",
			args: map[string]any{
				"uids": []any{float64(10), float64(20)},
				"flag": "flagged",
			},
			want: MarkAction{Flag: "flagged", Add: true, UIDs: []uint32{10, 20}},
		},
		{
			name: "missing uids leaves UIDs nil for handler to reject",
			args: map[string]any{"flag": "seen"},
			want: MarkAction{Flag: "seen", Add: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMarkAction(tt.args)
			if got.Flag != tt.want.Flag {
				t.Errorf("Flag = %q, want %q", got.Flag, tt.want.Flag)
			}
			if got.Add != tt.want.Add {
				t.Errorf("Add = %v, want %v", got.Add, tt.want.Add)
			}
			if !uint32SliceEqual(got.UIDs, tt.want.UIDs) {
				t.Errorf("UIDs = %v, want %v", got.UIDs, tt.want.UIDs)
			}
		})
	}
}

func uint32SliceEqual(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFormatEnvelopeList(t *testing.T) {
	listed := ListResult{
		Folder:       "INBOX",
		TotalMatched: 2,
		Envelopes: []Envelope{
			{
				UID:     100,
				From:    Address{Name: "Alice", Address: "alice@example.com"},
				Subject: "Hello",
				Date:    time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
				Flags:   []string{`\Seen`},
				Size:    1024,
			},
			{
				UID:     99,
				From:    Address{Address: "bob@example.com"},
				Subject: "Meeting",
				Date:    time.Date(2025, 1, 14, 8, 0, 0, 0, time.UTC),
				Size:    512,
			},
		},
	}

	result := formatEnvelopeList(listed)

	mustContain(t, result, "Found 2 message(s) in INBOX", "UID: 100", `"Alice" <alice@example.com>`, `\Seen`, "UID: 99", "1024 bytes")
}

func TestFormatEnvelopeList_TruncatedSaysSo(t *testing.T) {
	listed := ListResult{Folder: "Archive", TotalMatched: 5, Envelopes: []Envelope{{UID: 1}, {UID: 2}}}
	result := formatEnvelopeList(listed)
	mustContain(t, result, "Showing 2 of 5 message(s) in Archive")
}

func TestFormatMessage(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:       42,
			From:      Address{Name: "Alice", Address: "alice@example.com"},
			To:        []Address{{Address: "bob@example.com"}, {Address: "carol@example.com"}},
			Cc:        []Address{{Address: "dave@example.com"}},
			ReplyTo:   []Address{{Address: "alice-work@example.com"}},
			Subject:   "Test Subject",
			Date:      time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
			Flags:     []string{`\Seen`, `\Flagged`},
			Size:      2048,
			MessageID: "abc123@example.com",
		},
		TextBody:   "Hello, this is the body.",
		BodySource: "text",
		Attachments: []Attachment{
			{Filename: "q3.pdf", ContentType: "application/pdf", Size: 1234},
			{ContentType: "image/png", Size: 99, Inline: true},
		},
	}

	result := formatMessage(msg)

	mustContain(t, result,
		`From: "Alice" <alice@example.com>`,
		"bob@example.com, carol@example.com",
		"Cc: dave@example.com",
		"Reply-To: alice-work@example.com",
		"Message-ID: abc123@example.com",
		"Test Subject",
		"UID: 42",
		"Hello, this is the body.",
		`\Seen`,
		"q3.pdf (application/pdf, 1234 bytes, attachment)",
		"(unnamed) (image/png, 99 bytes, inline)",
	)
}

func TestFormatMessage_NoCcNoMessageID(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     10,
			From:    Address{Address: "sender@example.com"},
			Subject: "Simple",
			Date:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		TextBody:   "body",
		BodySource: "text",
	}

	result := formatMessage(msg)

	if strings.Contains(result, "Cc:") {
		t.Error("should not contain Cc header when empty")
	}
	if strings.Contains(result, "Message-ID:") {
		t.Error("should not contain Message-ID header when empty")
	}
	if strings.Contains(result, "Attachments:") {
		t.Error("should not list attachments when there are none")
	}
}

func TestFormatMessage_HTMLRendered(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     10,
			From:    Address{Address: "sender@example.com"},
			Subject: "HTML Only",
			Date:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		HTMLBody:   "<p>Hello</p>",
		TextBody:   "Hello",
		BodySource: "html",
	}

	result := formatMessage(msg)

	mustContain(t, result, "[body rendered from HTML]", "Hello")
	if strings.Contains(result, "<p>") {
		t.Error("raw HTML must never reach the model")
	}
}

func TestFormatMessage_NoBody(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     10,
			From:    Address{Address: "sender@example.com"},
			Subject: "Empty",
			Date:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	result := formatMessage(msg)

	if !strings.Contains(result, "[No text content available]") {
		t.Error("should indicate no content")
	}
}

func TestFormatMessage_TruncatedBodyFlagged(t *testing.T) {
	msg := &Message{Envelope: Envelope{UID: 1, From: Address{Address: "a@example.com"}}, TextBody: "head", BodySource: "text", BodyTruncated: true}
	mustContain(t, formatMessage(msg), "[truncated — body exceeds 32 KB]")
}

func TestFormatFolderList(t *testing.T) {
	folders := []Folder{
		{Name: "INBOX", Role: RoleInbox, Selectable: true, Messages: 150, Unseen: 5},
		{Name: "Sent", Role: RoleSent, Selectable: true, Messages: 42, Unseen: 0},
		{Name: "Drafts", Role: RoleDrafts, Selectable: true, Messages: 3, Unseen: 0},
		{Name: "[Gmail]", Selectable: false},
	}

	result := formatFolderList(folders)

	mustContain(t, result, "Found 4 folder(s)", "INBOX", "(5 unseen)", "[inbox]", "[sent]", "[drafts]", "[not selectable]")
	for line := range strings.SplitSeq(result, "\n") {
		if strings.Contains(line, "Sent") && strings.Contains(line, "unseen") {
			t.Error("should not show unseen annotation for zero unseen")
		}
	}
}

func TestFormatEnvelopeList_Empty(t *testing.T) {
	result := formatEnvelopeList(ListResult{Folder: "INBOX"})
	if !strings.Contains(result, "Found 0 message(s) in INBOX") {
		t.Error("should handle an empty result")
	}
}

func TestFormatFolderList_Empty(t *testing.T) {
	result := formatFolderList(nil)
	if !strings.Contains(result, "Found 0 folder(s)") {
		t.Error("should handle nil folder slice")
	}
}

func TestParseSearchDate(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"2026-09-01", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), false},
		{"2026-09-01T08:00:00Z", time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), false},
		{"-7d", now.Add(-7 * 24 * time.Hour), false},
		{"yesterday", time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSearchDate(tc.in, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !got.Equal(tc.want) {
				t.Errorf("parseSearchDate(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMissingUIDs(t *testing.T) {
	got := missingUIDs([]uint32{1, 2, 3}, []uint32{1, 3})
	if fmtUIDs(got) != fmtUIDs([]uint32{2}) {
		t.Errorf("missingUIDs = %v", got)
	}
}
