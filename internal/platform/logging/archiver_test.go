package logging

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestArchiver_Basic(t *testing.T) {
	db := openTestDB(t)
	archiveDir := t.TempDir()
	ctx := context.Background()
	logger := slog.Default()

	w, err := NewContentWriter(db, 0, logger)
	if err != nil {
		t.Fatalf("NewContentWriter: %v", err)
	}
	defer w.Close()

	// Write two requests.
	for _, rc := range []RequestContent{
		{
			RequestID:    "req-old",
			SystemPrompt: "sys",
			UserContent:  "ping",
			Model:        "test",
			Messages: []llm.Message{
				{Role: "user", Content: "ping"},
				{Role: "assistant", Content: "pong"},
			},
		},
		{
			RequestID:    "req-new",
			SystemPrompt: "sys",
			UserContent:  "hello",
			Model:        "test",
			Messages:     []llm.Message{},
		},
	} {
		w.WriteRequest(ctx, rc)
	}

	// Backdate req-old to 100 days ago so it falls below the archive cutoff.
	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`UPDATE log_request_content SET created_at = ? WHERE request_id = 'req-old'`,
		oldTime,
	); err != nil {
		t.Fatalf("backdate req-old: %v", err)
	}

	archiver := NewArchiver(db, archiveDir, logger)
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	n, err := archiver.Archive(ctx, cutoff)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if n != 1 {
		t.Errorf("Archive returned %d, want 1", n)
	}

	// req-new should still be in the database.
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM log_request_content WHERE request_id = 'req-new'`,
	).Scan(&count); err != nil {
		t.Fatalf("count req-new: %v", err)
	}
	if count != 1 {
		t.Errorf("req-new count = %d, want 1 (should not have been archived)", count)
	}

	// req-old should have been removed from the database.
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM log_request_content WHERE request_id = 'req-old'`,
	).Scan(&count); err != nil {
		t.Fatalf("count req-old: %v", err)
	}
	if count != 0 {
		t.Errorf("req-old count = %d, want 0 (should have been archived)", count)
	}

	// Find the JSONL archive file.
	monthKey := time.Now().UTC().Add(-100 * 24 * time.Hour).Format("2006-01")
	archivePath := filepath.Join(archiveDir, monthKey+".jsonl")
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive file %s: %v", archivePath, err)
	}
	defer f.Close()

	var lines []RequestDetail
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for sc.Scan() {
		var rd RequestDetail
		if err := json.Unmarshal(sc.Bytes(), &rd); err != nil {
			t.Fatalf("unmarshal archive line: %v", err)
		}
		lines = append(lines, rd)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan archive: %v", err)
	}

	if len(lines) != 1 {
		t.Fatalf("archive file has %d lines, want 1", len(lines))
	}
	if lines[0].RequestID != "req-old" {
		t.Errorf("archived request_id = %q, want %q", lines[0].RequestID, "req-old")
	}
	if lines[0].SystemPrompt != "sys" {
		t.Errorf("archived system_prompt = %q, want %q", lines[0].SystemPrompt, "sys")
	}
}

func TestArchiver_NothingToArchive(t *testing.T) {
	db := openTestDB(t)
	archiveDir := t.TempDir()
	archiver := NewArchiver(db, archiveDir, slog.Default())

	n, err := archiver.Archive(context.Background(), time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if n != 0 {
		t.Errorf("Archive returned %d, want 0 (empty database)", n)
	}
}

func TestArchiver_ToolCallsPreserved(t *testing.T) {
	db := openTestDB(t)
	archiveDir := t.TempDir()
	ctx := context.Background()
	logger := slog.Default()

	w, err := NewContentWriter(db, 0, logger)
	if err != nil {
		t.Fatalf("NewContentWriter: %v", err)
	}
	defer w.Close()

	w.WriteRequest(ctx, RequestContent{
		RequestID:    "req-tools",
		SystemPrompt: "sys",
		UserContent:  "use a tool",
		Model:        "test",
		Messages: []llm.Message{
			{Role: "user", Content: "use a tool"},
			{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{Name: "search", Arguments: map[string]any{"q": "test"}}},
				},
			},
			{Role: "tool", ToolCallID: "tc1", Content: "result data"},
			{Role: "assistant", Content: "done"},
		},
	})

	// Backdate the row.
	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`UPDATE log_request_content SET created_at = ? WHERE request_id = 'req-tools'`, oldTime,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	archiver := NewArchiver(db, archiveDir, logger)
	if _, err := archiver.Archive(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	monthKey := time.Now().UTC().Add(-100 * 24 * time.Hour).Format("2006-01")
	archivePath := filepath.Join(archiveDir, monthKey+".jsonl")
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	var rd RequestDetail
	if err := json.Unmarshal(data[:len(data)-1], &rd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rd.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(rd.ToolCalls))
	}
	if rd.ToolCalls[0].ToolName != "search" {
		t.Errorf("tool name = %q, want %q", rd.ToolCalls[0].ToolName, "search")
	}
	if rd.ToolCalls[0].Result != "result data" {
		t.Errorf("tool result = %q, want %q", rd.ToolCalls[0].Result, "result data")
	}
}

func TestMonthKeyFor(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"2025-03-15T10:23:45Z", "2025-03", false},
		{"2025-03-15T10:23:45.123456789Z", "2025-03", false},
		{"2024-12-01T00:00:00Z", "2024-12", false},
		{"not-a-date", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		got, err := monthKeyFor(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("monthKeyFor(%q): want error, got %q", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("monthKeyFor(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("monthKeyFor(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
