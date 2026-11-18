package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func retryParams(sourceID string, content []byte) IngestParams {
	return IngestParams{
		Source:         bytes.NewReader(content),
		OriginalName:   "photo.jpg",
		ContentType:    "image/jpeg",
		Channel:        "signal",
		Sender:         "+15551234567",
		ConversationID: "signal-15551234567",
		ReceivedAt:     time.UnixMilli(1794960952450),
		SourceID:       sourceID,
	}
}

// TestIngestReusesTheRecordOfARetriedAttachment checks that ingesting
// the same attachment twice, as a retried message does, returns the
// first record instead of adding a row, while a different attachment,
// even with identical bytes, name and message, gets its own record.
func TestIngestReusesTheRecordOfARetriedAttachment(t *testing.T) {
	photo := []byte("photo bytes")
	tests := []struct {
		name      string
		first     IngestParams
		second    IngestParams
		wantReuse bool
	}{
		{"same attachment retried", retryParams("att-1", photo), retryParams("att-1", photo), true},
		{"second attachment with identical bytes in the same message", retryParams("att-1", photo), retryParams("att-2", photo), false},
		{"same id with different content", retryParams("att-1", photo), retryParams("att-1", []byte("other bytes")), false},
		{"same id on another channel", retryParams("att-1", photo), func() IngestParams {
			p := retryParams("att-1", photo)
			p.Channel = "email"
			return p
		}(), false},
		{"same upload in a later message", retryParams("att-1", photo), func() IngestParams {
			p := retryParams("att-1", photo)
			p.ReceivedAt = p.ReceivedAt.Add(time.Second)
			return p
		}(), false},
		{"same upload from another sender", retryParams("att-1", photo), func() IngestParams {
			p := retryParams("att-1", photo)
			p.Sender = "+15559876543"
			return p
		}(), false},
		{"same upload in another conversation", retryParams("att-1", photo), func() IngestParams {
			p := retryParams("att-1", photo)
			p.ConversationID = "signal-group"
			return p
		}(), false},
		{"no source id", retryParams("", photo), retryParams("", photo), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			first, err := store.Ingest(ctx, tt.first)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Ingest(ctx, tt.second)
			if err != nil {
				t.Fatal(err)
			}
			total, _, _, err := store.TelemetryStats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantReuse {
				if second.ID != first.ID || total != 1 {
					t.Errorf("retried attachment: ids %s / %s, rows %d; want one record reused", first.ID, second.ID, total)
				}
				return
			}
			if second.ID == first.ID || total != 2 {
				t.Errorf("distinct attachments: ids %s / %s, rows %d; want two records", first.ID, second.ID, total)
			}
		})
	}
}

// TestIngestRecordsTheSourceID checks the source id is stored and read
// back with the record.
func TestIngestRecordsTheSourceID(t *testing.T) {
	store := newTestStore(t)
	rec, err := store.Ingest(context.Background(), retryParams("att-1", []byte("photo bytes")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ByID(context.Background(), rec.ID)
	if err != nil || got == nil || got.SourceID != "att-1" {
		t.Fatalf("ByID = %+v, %v; want SourceID att-1", got, err)
	}
}

// TestIngestRetryPrefersTheAnalyzedRecord checks that when earlier
// failures already left several records of one attachment, a retry gets
// the one with a description, so the turn does not analyze again.
func TestIngestRetryPrefersTheAnalyzedRecord(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first, err := store.Ingest(ctx, retryParams("att-1", []byte("photo bytes")))
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

	again, err := store.Ingest(ctx, retryParams("att-1", []byte("photo bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Description != "A dog on a porch." {
		t.Errorf("retry got record %s with description %q; want the analyzed record %s", again.ID, again.Description, first.ID)
	}
}

// TestIngestRemovesANewFileWhenTheLookupFails checks that an ingest
// which stored new content and then failed to look up an earlier record
// removes that content, as a failed insert does, so no file is left
// without a metadata row.
func TestIngestRemovesANewFileWhenTheLookupFails(t *testing.T) {
	store := newTestStore(t)
	content := []byte("never stored before")
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Ingest(context.Background(), retryParams("att-1", content)); err == nil {
		t.Fatal("Ingest succeeded with a closed database; want an error")
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	blob := filepath.Join(store.rootDir, hash[:2], hash[2:4], hash+".jpg")
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Errorf("stored content left behind at %s (stat err %v)", blob, err)
	}
	entries, err := os.ReadDir(store.rootDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ingest-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestConcurrentIngestKeepsEveryRecordedFile ingests identical content
// from many receipts at once, half of them failing after the file is
// stored (their context is already canceled, so the lookup or insert
// fails and rolls back). Every record that was made must still find its
// file: a failing ingest may only remove a file no other ingest relies
// on.
func TestConcurrentIngestKeepsEveryRecordedFile(t *testing.T) {
	for round := 0; round < 20; round++ {
		store := newTestStore(t)
		content := []byte(fmt.Sprintf("shared content %d", round))
		canceled, cancel := context.WithCancel(context.Background())
		cancel()

		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			recs []*Record
		)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ctx := context.Background()
				if i%2 == 1 {
					ctx = canceled
				}
				params := retryParams(fmt.Sprintf("att-%d", i), content)
				params.ReceivedAt = params.ReceivedAt.Add(time.Duration(i) * time.Millisecond)
				rec, err := store.Ingest(ctx, params)
				if err != nil {
					return
				}
				mu.Lock()
				recs = append(recs, rec)
				mu.Unlock()
			}(i)
		}
		wg.Wait()

		if len(recs) == 0 {
			t.Fatalf("round %d: no ingest succeeded", round)
		}
		for _, rec := range recs {
			if _, err := os.Stat(store.AbsPath(rec)); err != nil {
				t.Fatalf("round %d: record %s lost its file: %v", round, rec.ID, err)
			}
		}
	}
}
