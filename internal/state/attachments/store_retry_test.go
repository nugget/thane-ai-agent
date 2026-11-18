package attachments

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestIngestReusesTheRecordOfARetriedReceipt checks that ingesting the
// same receipt twice, as a retried message does, returns the first
// record instead of adding a row, while anything that makes it a
// different receipt still gets its own record.
func TestIngestReusesTheRecordOfARetriedReceipt(t *testing.T) {
	received := time.UnixMilli(1794960952450)
	base := func() IngestParams {
		return IngestParams{
			Source:         bytes.NewReader([]byte("photo bytes")),
			OriginalName:   "photo.jpg",
			ContentType:    "image/jpeg",
			Channel:        "signal",
			Sender:         "+15551234567",
			ConversationID: "signal-15551234567",
			ReceivedAt:     received,
		}
	}
	noReceiptTime := func(p *IngestParams) { p.ReceivedAt = time.Time{} }
	tests := []struct {
		name      string
		first     func(p *IngestParams) // nil: the base receipt
		second    func(p *IngestParams)
		wantReuse bool
	}{
		{"same receipt", nil, func(*IngestParams) {}, true},
		{"later message with the same file", nil, func(p *IngestParams) { p.ReceivedAt = received.Add(time.Second) }, false},
		{"different file name", nil, func(p *IngestParams) { p.OriginalName = "other.jpg" }, false},
		{"different content", nil, func(p *IngestParams) { p.Source = bytes.NewReader([]byte("other bytes")) }, false},
		{"different sender", nil, func(p *IngestParams) { p.Sender = "+15559876543" }, false},
		{"different conversation", nil, func(p *IngestParams) { p.ConversationID = "signal-group" }, false},
		{"different channel", nil, func(p *IngestParams) { p.Channel = "email" }, false},
		{"neither has a receipt time", noReceiptTime, noReceiptTime, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			firstParams := base()
			if tt.first != nil {
				tt.first(&firstParams)
			}
			first, err := store.Ingest(ctx, firstParams)
			if err != nil {
				t.Fatal(err)
			}
			params := base()
			tt.second(&params)
			second, err := store.Ingest(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			total, _, _, err := store.TelemetryStats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantReuse {
				if second.ID != first.ID || total != 1 {
					t.Errorf("retried receipt: ids %s / %s, rows %d; want one record reused", first.ID, second.ID, total)
				}
				return
			}
			if second.ID == first.ID || total != 2 {
				t.Errorf("distinct receipt: ids %s / %s, rows %d; want two records", first.ID, second.ID, total)
			}
		})
	}
}

// TestIngestRetryPrefersTheAnalyzedRecord checks that when earlier
// failures already left several records of one receipt, a retry gets
// the one with a description, so the turn does not analyze again.
func TestIngestRetryPrefersTheAnalyzedRecord(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	received := time.UnixMilli(1794960952450)
	params := func() IngestParams {
		return IngestParams{
			Source: bytes.NewReader([]byte("photo bytes")), OriginalName: "photo.jpg", ContentType: "image/jpeg",
			Channel: "signal", Sender: "+15551234567", ConversationID: "signal-15551234567", ReceivedAt: received,
		}
	}
	first, err := store.Ingest(ctx, params())
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate row like the ones repeated crashes left before ingest
	// reused records.
	dup := *first
	dup.ID = "00000000-0000-0000-0000-000000000000"
	if err := store.insertRecord(ctx, &dup); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateVision(ctx, first.ID, "A dog on a porch.", "vision-model"); err != nil {
		t.Fatal(err)
	}

	again, err := store.Ingest(ctx, params())
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Description != "A dog on a porch." {
		t.Errorf("retry got record %s with description %q; want the analyzed record %s", again.ID, again.Description, first.ID)
	}
}
