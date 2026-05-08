package attachments

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// newTestStore creates a Store in a temporary directory for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	rootDir := filepath.Join(dir, "store")

	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewStore(db, rootDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestIngest_HappyPath(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	content := []byte("hello, world")
	now := time.Now().UTC().Truncate(time.Millisecond)

	rec, err := store.Ingest(ctx, IngestParams{
		Source:         bytes.NewReader(content),
		OriginalName:   "greeting.txt",
		ContentType:    "text/plain",
		Size:           int64(len(content)),
		Channel:        "signal",
		Sender:         "+15551234567",
		ConversationID: "conv-001",
		ReceivedAt:     now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify record fields.
	if rec.ID == "" {
		t.Error("record ID should not be empty")
	}
	if rec.Hash == "" {
		t.Error("record Hash should not be empty")
	}
	if rec.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", rec.Size, len(content))
	}
	if rec.OriginalName != "greeting.txt" {
		t.Errorf("OriginalName = %q, want %q", rec.OriginalName, "greeting.txt")
	}
	if rec.Channel != "signal" {
		t.Errorf("Channel = %q, want %q", rec.Channel, "signal")
	}

	// Verify file exists at expected path.
	absPath := store.AbsPath(rec)
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("stored content = %q, want %q", data, content)
	}

	// Verify store path structure: hash[0:2]/hash[2:4]/hash.ext
	parts := strings.Split(rec.StorePath, string(filepath.Separator))
	if len(parts) != 3 {
		t.Fatalf("StorePath = %q, want 3 path components", rec.StorePath)
	}
	if parts[0] != rec.Hash[:2] || parts[1] != rec.Hash[2:4] {
		t.Errorf("StorePath prefix = %s/%s, want %s/%s", parts[0], parts[1], rec.Hash[:2], rec.Hash[2:4])
	}
}

func TestIngest_Dedup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	content := []byte("duplicate content")
	now := time.Now().UTC()

	rec1, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader(content),
		ContentType: "text/plain",
		Channel:     "signal",
		Sender:      "+15551111111",
		ReceivedAt:  now,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec2, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader(content),
		ContentType: "text/plain",
		Channel:     "email",
		Sender:      "user@example.com",
		ReceivedAt:  now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same hash, different IDs.
	if rec1.Hash != rec2.Hash {
		t.Errorf("hashes differ: %q vs %q", rec1.Hash, rec2.Hash)
	}
	if rec1.ID == rec2.ID {
		t.Error("IDs should be different for deduped content")
	}

	// Same file on disk.
	if rec1.StorePath != rec2.StorePath {
		t.Errorf("store paths differ: %q vs %q", rec1.StorePath, rec2.StorePath)
	}

	// Only one file on disk.
	absPath := store.AbsPath(rec1)
	if _, err := os.Stat(absPath); err != nil {
		t.Errorf("expected file at %s: %v", absPath, err)
	}
}

func TestIngest_DifferentContent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec1, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader([]byte("content A")),
		ContentType: "text/plain",
		ReceivedAt:  now,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec2, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader([]byte("content B")),
		ContentType: "text/plain",
		ReceivedAt:  now,
	})
	if err != nil {
		t.Fatal(err)
	}

	if rec1.Hash == rec2.Hash {
		t.Error("different content should produce different hashes")
	}
	if rec1.StorePath == rec2.StorePath {
		t.Error("different content should have different store paths")
	}
}

func TestIngest_ExtensionDerivation(t *testing.T) {
	tests := []struct {
		name         string
		contentType  string
		originalName string
		wantExt      string
	}{
		{"jpeg from MIME", "image/jpeg", "photo.jpg", ".jpg"},
		{"png from MIME", "image/png", "", ".png"},
		{"pdf from MIME", "application/pdf", "", ".pdf"},
		{"fallback to original name", "application/x-unknown-type-7891", "report.xlsx", ".xlsx"},
		{"no extension available", "application/x-unknown-type-7891", "noext", ""},
		{"empty content type, name ext", "", "data.csv", ".csv"},
	}

	store := newTestStore(t)
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := store.Ingest(ctx, IngestParams{
				Source:       bytes.NewReader([]byte(tt.name)), // unique content per test
				ContentType:  tt.contentType,
				OriginalName: tt.originalName,
				ReceivedAt:   time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}

			gotExt := filepath.Ext(rec.StorePath)
			if gotExt != tt.wantExt {
				t.Errorf("extension = %q, want %q (StorePath = %q)", gotExt, tt.wantExt, rec.StorePath)
			}
		})
	}
}

func TestByHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader([]byte("lookup by hash")),
		ContentType: "text/plain",
		Channel:     "signal",
		ReceivedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	found, err := store.ByHash(ctx, rec.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("ByHash returned nil for existing hash")
	}
	if found.ID != rec.ID {
		t.Errorf("ByHash ID = %q, want %q", found.ID, rec.ID)
	}

	// Non-existent hash.
	missing, err := store.ByHash(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Error("ByHash should return nil for non-existent hash")
	}
}

func TestByID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec, err := store.Ingest(ctx, IngestParams{
		Source:      bytes.NewReader([]byte("lookup by id")),
		ContentType: "text/plain",
		ReceivedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	found, err := store.ByID(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("ByID returned nil for existing ID")
	}
	if found.Hash != rec.Hash {
		t.Errorf("ByID Hash = %q, want %q", found.Hash, rec.Hash)
	}

	// Non-existent ID.
	missing, err := store.ByID(ctx, "nonexistent-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Error("ByID should return nil for non-existent ID")
	}
}

func TestIngest_ReaderError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Ingest(ctx, IngestParams{
		Source:      &failReader{},
		ContentType: "text/plain",
		ReceivedAt:  time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}

	// Verify no temp files left behind.
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

func TestAbsPath(t *testing.T) {
	store := newTestStore(t)

	rec := &Record{
		StorePath: filepath.Join("ab", "cd", "abcd1234.jpg"),
	}
	got := store.AbsPath(rec)
	want := filepath.Join(store.rootDir, "ab", "cd", "abcd1234.jpg")
	if got != want {
		t.Errorf("AbsPath = %q, want %q", got, want)
	}
}

func TestIngest_MetadataPreserved(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	rec, err := store.Ingest(ctx, IngestParams{
		Source:         bytes.NewReader([]byte("metadata test")),
		OriginalName:   "photo.jpg",
		ContentType:    "image/jpeg",
		Size:           13,
		Width:          1920,
		Height:         1080,
		Channel:        "signal",
		Sender:         "+15559876543",
		ConversationID: "conv-meta-001",
		ReceivedAt:     now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Round-trip through database.
	found, err := store.ByID(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}

	if found.OriginalName != "photo.jpg" {
		t.Errorf("OriginalName = %q, want %q", found.OriginalName, "photo.jpg")
	}
	if found.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want %q", found.ContentType, "image/jpeg")
	}
	if found.Width != 1920 {
		t.Errorf("Width = %d, want 1920", found.Width)
	}
	if found.Height != 1080 {
		t.Errorf("Height = %d, want 1080", found.Height)
	}
	if found.Channel != "signal" {
		t.Errorf("Channel = %q, want %q", found.Channel, "signal")
	}
	if found.Sender != "+15559876543" {
		t.Errorf("Sender = %q, want %q", found.Sender, "+15559876543")
	}
	if found.ConversationID != "conv-meta-001" {
		t.Errorf("ConversationID = %q, want %q", found.ConversationID, "conv-meta-001")
	}
}

func TestExtensionForType(t *testing.T) {
	tests := []struct {
		contentType  string
		originalName string
		want         string
	}{
		{"image/jpeg", "", ".jpg"},
		{"image/png", "", ".png"},
		{"application/pdf", "", ".pdf"},
		{"audio/ogg", "", ".ogg"},
		{"video/mp4", "", ".mp4"},
		{"", "data.csv", ".csv"},
		{"", "UPPER.TXT", ".txt"},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.contentType+"/"+tt.originalName, func(t *testing.T) {
			got := extensionForType(tt.contentType, tt.originalName)
			if got != tt.want {
				t.Errorf("extensionForType(%q, %q) = %q, want %q", tt.contentType, tt.originalName, got, tt.want)
			}
		})
	}
}

// failReader is an io.Reader that always returns an error.
type failReader struct{}

func (*failReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
