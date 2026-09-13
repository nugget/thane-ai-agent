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
	out, err := marshalListResponse(newListResponse("primary", ListResult{Folder: "INBOX", TotalMatched: 100, Envelopes: envs}, time.Now()))
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
	out, err := renderRead(newReadResponse("primary", "INBOX", msg, false, time.Now()), msg)
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
	out, _ = renderRead(newReadResponse("primary", "INBOX", short, false, time.Now()), short)
	if strings.Contains(out, "body cut") || strings.Contains(out, `"body_truncated":true`) {
		t.Errorf("a small read must not be marked cut: %s", out)
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
