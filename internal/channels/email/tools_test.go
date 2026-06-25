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
	envelopes := []Envelope{
		{
			UID:     100,
			From:    "Alice <alice@example.com>",
			Subject: "Hello",
			Date:    time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			Flags:   []string{`\Seen`},
			Size:    1024,
		},
		{
			UID:     99,
			From:    "bob@example.com",
			Subject: "Meeting",
			Date:    time.Date(2025, 1, 14, 8, 0, 0, 0, time.UTC),
			Size:    512,
		},
	}

	result := formatEnvelopeList(envelopes)

	if !strings.Contains(result, "Found 2 message(s)") {
		t.Error("should contain message count")
	}
	if !strings.Contains(result, "UID: 100") {
		t.Error("should contain first UID")
	}
	if !strings.Contains(result, "Alice <alice@example.com>") {
		t.Error("should contain first sender")
	}
	if !strings.Contains(result, `\Seen`) {
		t.Error("should contain flags when present")
	}
	if !strings.Contains(result, "UID: 99") {
		t.Error("should contain second UID")
	}
	if !strings.Contains(result, "1024 bytes") {
		t.Error("should contain message size")
	}
}

func TestFormatMessage(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     42,
			From:    "Alice <alice@example.com>",
			To:      []string{"bob@example.com", "carol@example.com"},
			Subject: "Test Subject",
			Date:    time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
			Flags:   []string{`\Seen`, `\Flagged`},
			Size:    2048,
		},
		Cc:        []string{"dave@example.com"},
		MessageID: "abc123@example.com",
		TextBody:  "Hello, this is the body.",
	}

	result := formatMessage(msg)

	if !strings.Contains(result, "From: Alice <alice@example.com>") {
		t.Error("should contain From header")
	}
	if !strings.Contains(result, "bob@example.com, carol@example.com") {
		t.Error("should contain all To recipients")
	}
	if !strings.Contains(result, "Cc: dave@example.com") {
		t.Error("should contain Cc header")
	}
	if !strings.Contains(result, "Message-ID: abc123@example.com") {
		t.Error("should contain Message-ID header")
	}
	if !strings.Contains(result, "Test Subject") {
		t.Error("should contain subject")
	}
	if !strings.Contains(result, "UID: 42") {
		t.Error("should contain UID")
	}
	if !strings.Contains(result, "Hello, this is the body.") {
		t.Error("should contain text body")
	}
	if !strings.Contains(result, `\Seen`) {
		t.Error("should contain flags")
	}
}

func TestFormatMessage_NoCcNoMessageID(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     10,
			From:    "sender@example.com",
			Subject: "Simple",
			Date:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		TextBody: "body",
	}

	result := formatMessage(msg)

	if strings.Contains(result, "Cc:") {
		t.Error("should not contain Cc header when empty")
	}
	if strings.Contains(result, "Message-ID:") {
		t.Error("should not contain Message-ID header when empty")
	}
}

func TestFormatMessage_HTMLOnly(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     10,
			From:    "sender@example.com",
			Subject: "HTML Only",
			Date:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		HTMLBody: "<p>Hello</p>",
	}

	result := formatMessage(msg)

	if !strings.Contains(result, "[HTML content") {
		t.Error("should indicate HTML-only content")
	}
	if !strings.Contains(result, "<p>Hello</p>") {
		t.Error("should contain HTML body")
	}
}

func TestFormatMessage_NoBody(t *testing.T) {
	msg := &Message{
		Envelope: Envelope{
			UID:     10,
			From:    "sender@example.com",
			Subject: "Empty",
			Date:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	result := formatMessage(msg)

	if !strings.Contains(result, "[No text content available]") {
		t.Error("should indicate no content")
	}
}

func TestFormatFolderList(t *testing.T) {
	folders := []Folder{
		{Name: "INBOX", Messages: 150, Unseen: 5},
		{Name: "Sent", Messages: 42, Unseen: 0},
		{Name: "Drafts", Messages: 3, Unseen: 0},
	}

	result := formatFolderList(folders)

	if !strings.Contains(result, "Found 3 folder(s)") {
		t.Error("should contain folder count")
	}
	if !strings.Contains(result, "INBOX") {
		t.Error("should contain INBOX")
	}
	if !strings.Contains(result, "(5 unseen)") {
		t.Error("should show unseen count for INBOX")
	}
	if !strings.Contains(result, "Sent") {
		t.Error("should contain Sent")
	}
	// Sent has 0 unseen — should not show unseen annotation.
	for line := range strings.SplitSeq(result, "\n") {
		if strings.Contains(line, "Sent") && strings.Contains(line, "unseen") {
			t.Error("should not show unseen annotation for zero unseen")
		}
	}
}

func TestFormatEnvelopeList_Empty(t *testing.T) {
	result := formatEnvelopeList(nil)
	if !strings.Contains(result, "Found 0 message(s)") {
		t.Error("should handle nil envelope slice")
	}
}

func TestFormatFolderList_Empty(t *testing.T) {
	result := formatFolderList(nil)
	if !strings.Contains(result, "Found 0 folder(s)") {
		t.Error("should handle nil folder slice")
	}
}
