package email

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestListResultIsHeldTo16KB pins the list bound: oversized subjects
// are cut, address lists are capped with a count, and the result drops
// trailing messages until it fits, saying so.
func TestListResultIsHeldTo16KB(t *testing.T) {
	envs := make([]Envelope, 0, 100)
	for i := 0; i < 100; i++ {
		to := make([]Address, 0, 30)
		for j := 0; j < 30; j++ {
			to = append(to, Address{Address: fmt.Sprintf("r%02d@example.com", j)})
		}
		envs = append(envs, Envelope{UID: uint32(i + 1), From: Address{Address: "a@example.com"}, To: to, Subject: strings.Repeat("s", 4000), Date: time.Now()})
	}
	out, err := marshalListResponse(newListResponse("primary", ListResult{Folder: "INBOX", TotalMatched: 100, Envelopes: envs}, nil, time.Now()))
	if err != nil {
		t.Fatalf("marshalListResponse: %v", err)
	}
	if len(out) > maxListOutput {
		t.Errorf("list result = %d bytes, cap %d", len(out), maxListOutput)
	}
	var resp listResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("capped result is not JSON: %v", err)
	}
	if !resp.Truncated || resp.Count != len(resp.Messages) || resp.Count >= 100 || resp.Count == 0 {
		t.Errorf("capped list = count %d, messages %d, truncated %v", resp.Count, len(resp.Messages), resp.Truncated)
	}
	first := resp.Messages[0]
	if len(first.To) != maxSummaryAddresses || first.AddressesOmitted != 20 || len(first.Subject) > maxSubjectOutput {
		t.Errorf("summary bounds: to %d, omitted %d, subject %d bytes", len(first.To), first.AddressesOmitted, len(first.Subject))
	}
}

// TestReadResultIsHeldTo32KB pins the read bound: capped address lists
// and attachments with counts, and a body cut to fit with the flag set.
func TestReadResultIsHeldTo32KB(t *testing.T) {
	to := make([]Address, 0, 3000)
	for i := 0; i < 3000; i++ {
		to = append(to, Address{Address: fmt.Sprintf("r%04d@example.com", i)})
	}
	msg := &Message{
		Envelope:           Envelope{UID: 1, From: Address{Address: "a@example.com"}, To: to},
		TextBody:           strings.Repeat("b", maxBodySize),
		BodySource:         "text",
		Attachments:        make([]Attachment, maxAttachments),
		AttachmentsOmitted: 10,
	}
	out, err := renderRead(newReadResponse("primary", "INBOX", msg, false, AbsentAuthentication(), nil, time.Now()), msg)
	if err != nil {
		t.Fatalf("renderRead: %v", err)
	}
	if len(out) > maxReadOutput {
		t.Errorf("read result = %d bytes, cap %d", len(out), maxReadOutput)
	}
	headerJSON, body, found := strings.Cut(out, bodySeparator)
	if !found {
		t.Fatal("result lacks the body separator")
	}
	var header readResponse
	if err := json.Unmarshal([]byte(headerJSON), &header); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	if !header.BodyTruncated || len(header.To) != maxHeaderAddresses || header.AddressesOmitted != 3000-maxHeaderAddresses || header.AttachmentsOmitted != 10 {
		t.Errorf("header bounds = %+v", header)
	}
	mustContain(t, body, "[body cut to keep this result within 32 KB]")

	short := &Message{Envelope: Envelope{UID: 2, From: Address{Address: "a@example.com"}}, TextBody: "hi", BodySource: "text"}
	out, _ = renderRead(newReadResponse("primary", "INBOX", short, false, AbsentAuthentication(), nil, time.Now()), short)
	if strings.Contains(out, "body cut") || strings.Contains(out, `"body_truncated":true`) {
		t.Errorf("a small read must not be marked cut: %s", out)
	}
}

// TestDraftGetResultIsHeldTo32KB pins email_draft_get's bound when the
// header, not the bodies, is what is large. Every variable-length field
// is clipped on a rune boundary with a marker and the lists are capped,
// so the header keeps to its 16 KB share; a header past its share even
// so gives way to the minimal header, which keeps draft_id and counts
// the rest; and either way the bodies are cut to fit what is left.
func TestDraftGetResultIsHeldTo32KB(t *testing.T) {
	huge := strings.Repeat("ü", 20000) // two bytes per rune, so a cut off a rune boundary shows
	addresses := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf("%s <r%04d@example.com>", huge, i))
		}
		return out
	}
	tests := []struct {
		name        string
		versions    int
		recipients  int
		wantMinimal bool
	}{
		{"clipped fields fit the header's share", 3, 2, false},
		{"a header past its share even clipped gives way to the minimal header", 60, 60, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := draftEntry{ID: "0192e0a0-0000-7000-8000-000000000001", Account: "primary", Stage: DraftStageOpen, Folder: "Drafts", UID: 7,
				UIDValidity: 1, MessageID: huge, From: huge, To: addresses(tt.recipients), Cc: addresses(tt.recipients), Subject: huge, InReplyTo: huge, Revisions: tt.versions - 1,
				Original: &draftOriginal{MessageID: huge, Folder: huge, From: Address{Name: huge, Address: huge}, Subject: huge}}
			for range tt.versions {
				e.History = append(e.History, draftRevision{By: huge, At: time.Now(), Note: huge})
			}
			cfg := AccountConfig{DefaultFrom: huge}
			cfg.Mailbox.Voice = huge

			out, err := renderDraftGet(newDraftGetHeader(e, cfg, nil, time.Now()), huge, huge)
			if err != nil {
				t.Fatalf("renderDraftGet: %v", err)
			}
			if len(out) > maxReadOutput || strings.ToValidUTF8(out, "?") != out {
				t.Fatalf("result = %d bytes, valid UTF-8 %v; want at most %d and valid", len(out), strings.ToValidUTF8(out, "?") == out, maxReadOutput)
			}
			parts := strings.Split(out, bodySeparator)
			if len(parts) != 3 {
				t.Fatalf("result has %d sections, want header, draft, original", len(parts))
			}
			if len(parts[0]) > maxDraftGetHeaderOutput {
				t.Errorf("header = %d bytes, share %d", len(parts[0]), maxDraftGetHeaderOutput)
			}
			for i, body := range parts[1:] {
				if !strings.HasSuffix(body, draftCutMarker) {
					t.Errorf("body %d does not end with the cut marker", i+1)
				}
			}

			var fields map[string]any
			if err := json.Unmarshal([]byte(parts[0]), &fields); err != nil {
				t.Fatalf("header is not JSON: %v", err)
			}
			if fields["draft_id"] != e.ID || fields["draft_body_truncated"] != true || fields["original_body_truncated"] != true {
				t.Errorf("header draft_id %v, body flags %v %v", fields["draft_id"], fields["draft_body_truncated"], fields["original_body_truncated"])
			}
			if tt.wantMinimal {
				var minimal draftGetMinimalHeader
				if err := json.Unmarshal([]byte(parts[0]), &minimal); err != nil {
					t.Fatalf("minimal header: %v", err)
				}
				if !minimal.HeaderTruncated || minimal.HistoryOmitted != tt.versions || minimal.AddressesOmitted != 2*tt.recipients || minimal.UID != 7 || minimal.DraftsFolder != "Drafts" {
					t.Errorf("minimal header = %+v", minimal)
				}
				if _, has := fields["subject"]; has {
					t.Error("the minimal header carries subject")
				}
				return
			}
			var header draftGetHeader
			if err := json.Unmarshal([]byte(parts[0]), &header); err != nil {
				t.Fatalf("header: %v", err)
			}
			clipped := map[string]struct {
				value string
				limit int
			}{
				"subject":          {header.Subject, maxSubjectOutput},
				"voice":            {header.Voice, maxDraftVoiceOutput},
				"from":             {header.From, maxDraftAddressOutput},
				"writes_as":        {header.WritesAs, maxDraftAddressOutput},
				"to[0]":            {header.To[0], maxDraftAddressOutput},
				"message_id":       {header.MessageID, maxMessageIDOutput},
				"history[0].by":    {header.History[0].By, maxNameOutput},
				"history[0].note":  {header.History[0].Note, maxDraftNoteBytes},
				"original.subject": {header.Original.Subject, maxSubjectOutput},
				"original.from":    {header.Original.From.Address, maxDraftAddressOutput},
			}
			for name, c := range clipped {
				if len(c.value) > c.limit || !strings.HasSuffix(c.value, fieldCutMarker) {
					t.Errorf("%s = %d bytes, want at most %d ending with %q", name, len(c.value), c.limit, fieldCutMarker)
				}
			}
			if len(header.To) != tt.recipients || len(header.History) != tt.versions || header.HistoryOmitted != 0 || header.AddressesOmitted != 0 {
				t.Errorf("header lists = to %d history %d omitted %d/%d", len(header.To), len(header.History), header.HistoryOmitted, header.AddressesOmitted)
			}
		})
	}
}

// TestMoveReportsUIDsNotFound pins the confirmed-move rule in the JSON
// result: a stale UID is listed as not found, not as moved.
func TestMoveReportsUIDsNotFound(t *testing.T) {
	svc, primary, _ := twoAccountService(t)
	uid := primary.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	out, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uids": []any{float64(uid), float64(uid + 500)}, "destination": "Archive"})
	if err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	var resp moveResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("move result is not JSON: %v", err)
	}
	if !resp.DestinationUIDsKnown || fmtUIDs(resp.UIDs) != fmtUIDs([]uint32{uid}) || fmtUIDs(resp.UIDsNotFound) != fmtUIDs([]uint32{uid + 500}) {
		t.Errorf("move result = %+v", resp)
	}
}

// TestSummaryCutsOversizedNamesAndMessageIDs pins the per-field bounds a
// list summary applies before the byte budget runs.
func TestSummaryCutsOversizedNamesAndMessageIDs(t *testing.T) {
	env := Envelope{UID: 1, From: Address{Name: strings.Repeat("N", 5000), Address: "a@example.com"}, MessageID: strings.Repeat("m", 5000) + "@example.com", Date: time.Now()}
	resp := newListResponse("primary", ListResult{Folder: "INBOX", TotalMatched: 1, Envelopes: []Envelope{env}}, nil, time.Now())
	if got := resp.Messages[0]; len(got.From.Name) > maxNameOutput || len(got.MessageID) > maxMessageIDOutput || got.From.Address != "a@example.com" {
		t.Errorf("summary = name %d bytes, message_id %d bytes, address %q", len(got.From.Name), len(got.MessageID), got.From.Address)
	}
}
