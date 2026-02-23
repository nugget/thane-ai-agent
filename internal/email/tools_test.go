package email

import (
	"strings"
	"testing"
	"time"
)

func TestStringArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want string
	}{
		{"present", map[string]any{"folder": "INBOX"}, "folder", "INBOX"},
		{"missing", map[string]any{}, "folder", ""},
		{"wrong type", map[string]any{"folder": 42}, "folder", ""},
		{"nil args", nil, "folder", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringArg(tt.args, tt.key); got != tt.want {
				t.Errorf("stringArg() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIntArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want int
	}{
		{"float64", map[string]any{"uid": float64(10)}, "uid", 10},
		{"int", map[string]any{"uid": int(42)}, "uid", 42},
		{"string numeric", map[string]any{"uid": "395"}, "uid", 395},
		{"string non-numeric", map[string]any{"uid": "abc"}, "uid", 0},
		{"missing", map[string]any{}, "uid", 0},
		{"wrong type", map[string]any{"uid": true}, "uid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := intArg(tt.args, tt.key); got != tt.want {
				t.Errorf("intArg() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestUint32SliceArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want []uint32
	}{
		{
			"float64 array",
			map[string]any{"uids": []any{float64(1), float64(2), float64(3)}},
			"uids",
			[]uint32{1, 2, 3},
		},
		{
			"string array",
			map[string]any{"uids": []any{"100", "200"}},
			"uids",
			[]uint32{100, 200},
		},
		{
			"mixed array",
			map[string]any{"uids": []any{float64(1), "2", int(3)}},
			"uids",
			[]uint32{1, 2, 3},
		},
		{
			"single float64",
			map[string]any{"uids": float64(42)},
			"uids",
			[]uint32{42},
		},
		{
			"single int",
			map[string]any{"uids": int(99)},
			"uids",
			[]uint32{99},
		},
		{
			"single string",
			map[string]any{"uids": "395"},
			"uids",
			[]uint32{395},
		},
		{
			"missing key",
			map[string]any{},
			"uids",
			nil,
		},
		{
			"invalid string",
			map[string]any{"uids": "abc"},
			"uids",
			nil,
		},
		{
			"array with invalid strings skipped",
			map[string]any{"uids": []any{float64(1), "bad", float64(3)}},
			"uids",
			[]uint32{1, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uint32SliceArg(tt.args, tt.key)
			if len(got) != len(tt.want) {
				t.Errorf("uint32SliceArg() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("uint32SliceArg()[%d] = %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBoolArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want bool
	}{
		{"true", map[string]any{"unseen": true}, "unseen", true},
		{"false", map[string]any{"unseen": false}, "unseen", false},
		{"missing", map[string]any{}, "unseen", false},
		{"wrong type", map[string]any{"unseen": "yes"}, "unseen", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boolArg(tt.args, tt.key); got != tt.want {
				t.Errorf("boolArg() = %v, want %v", got, tt.want)
			}
		})
	}
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

func TestStringSliceArg(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		key  string
		want []string
	}{
		{
			"json array",
			map[string]any{"to": []any{"a@example.com", "b@example.com"}},
			"to",
			[]string{"a@example.com", "b@example.com"},
		},
		{
			"single string",
			map[string]any{"to": "a@example.com"},
			"to",
			[]string{"a@example.com"},
		},
		{
			"empty string",
			map[string]any{"to": ""},
			"to",
			nil,
		},
		{
			"missing key",
			map[string]any{},
			"to",
			nil,
		},
		{
			"wrong type",
			map[string]any{"to": 42},
			"to",
			nil,
		},
		{
			"mixed types in array",
			map[string]any{"to": []any{"a@example.com", 42, "b@example.com"}},
			"to",
			[]string{"a@example.com", "b@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringSliceArg(tt.args, tt.key)
			if len(got) != len(tt.want) {
				t.Errorf("stringSliceArg() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("stringSliceArg()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
