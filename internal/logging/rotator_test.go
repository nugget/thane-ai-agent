package logging

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRotator_BasicWrite(t *testing.T) {
	dir := t.TempDir()

	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	msg := []byte("hello log\n")
	n, err := r.Write(msg)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(msg) {
		t.Errorf("wrote %d bytes, want %d", n, len(msg))
	}

	got, err := os.ReadFile(filepath.Join(dir, activeLogName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(msg) {
		t.Errorf("file contains %q, want %q", got, msg)
	}
}

func TestRotator_RotatesOnDateChange(t *testing.T) {
	dir := t.TempDir()

	yesterday := time.Date(2026, 3, 9, 23, 59, 0, 0, time.Local)
	today := time.Date(2026, 3, 10, 0, 1, 0, 0, time.Local)

	// Start "yesterday".
	nowFunc = func() time.Time { return yesterday }
	defer func() { nowFunc = time.Now }()

	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Write([]byte("yesterday's log\n")); err != nil {
		t.Fatal(err)
	}

	// Advance to "today".
	nowFunc = func() time.Time { return today }

	if _, err := r.Write([]byte("today's log\n")); err != nil {
		t.Fatal(err)
	}
	r.Close()

	// Yesterday's log should be archived.
	archived := filepath.Join(dir, "thane-2026-03-09.log")
	got, err := os.ReadFile(archived)
	if err != nil {
		t.Fatalf("archived file not found: %v", err)
	}
	if string(got) != "yesterday's log\n" {
		t.Errorf("archived content = %q, want %q", got, "yesterday's log\n")
	}

	// Today's active log should have today's entry.
	active, err := os.ReadFile(filepath.Join(dir, activeLogName))
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != "today's log\n" {
		t.Errorf("active content = %q, want %q", active, "today's log\n")
	}
}

func TestRotator_CompressesOnRotation(t *testing.T) {
	dir := t.TempDir()

	yesterday := time.Date(2026, 3, 9, 23, 59, 0, 0, time.Local)
	today := time.Date(2026, 3, 10, 0, 1, 0, 0, time.Local)

	nowFunc = func() time.Time { return yesterday }
	defer func() { nowFunc = time.Now }()

	r, err := Open(dir, true) // compress=true
	if err != nil {
		t.Fatal(err)
	}

	payload := "compressed log entry\n"
	if _, err := r.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}

	nowFunc = func() time.Time { return today }
	if _, err := r.Write([]byte("new day\n")); err != nil {
		t.Fatal(err)
	}
	r.Close()

	// Compressed archive should exist.
	gzPath := filepath.Join(dir, "thane-2026-03-09.log.gz")
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("compressed file not found: %v", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer gz.Close()

	got, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("decompressed content = %q, want %q", got, payload)
	}

	// Uncompressed archive should NOT exist.
	if _, err := os.Stat(filepath.Join(dir, "thane-2026-03-09.log")); !os.IsNotExist(err) {
		t.Error("uncompressed archive should not exist when compress=true")
	}
}

func TestRotator_StartupRotatesStaleFile(t *testing.T) {
	dir := t.TempDir()

	// Write a file with yesterday's modification time.
	activePath := filepath.Join(dir, activeLogName)
	if err := os.WriteFile(activePath, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Date(2026, 3, 9, 15, 0, 0, 0, time.Local)
	if err := os.Chtimes(activePath, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}

	today := time.Date(2026, 3, 10, 8, 0, 0, 0, time.Local)
	nowFunc = func() time.Time { return today }
	defer func() { nowFunc = time.Now }()

	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Stale file should have been archived.
	archived := filepath.Join(dir, "thane-2026-03-09.log")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("stale file not rotated on startup: %v", err)
	}
}

func TestRotator_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()

	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var wg sync.WaitGroup
	const goroutines = 10
	const writes = 100

	for i := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for range writes {
				if _, err := r.Write([]byte("line\n")); err != nil {
					t.Errorf("goroutine %d: %v", id, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()

	got, err := os.ReadFile(filepath.Join(dir, activeLogName))
	if err != nil {
		t.Fatal(err)
	}

	// Count lines — should be goroutines * writes.
	lines := 0
	for _, b := range got {
		if b == '\n' {
			lines++
		}
	}
	want := goroutines * writes
	if lines != want {
		t.Errorf("got %d lines, want %d", lines, want)
	}
}

func TestRotator_LineCount(t *testing.T) {
	dir := t.TempDir()

	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh file starts at zero.
	if got := r.LineCount(); got != 0 {
		t.Errorf("initial LineCount = %d, want 0", got)
	}

	// Each write with one newline increments by one.
	for i := 1; i <= 3; i++ {
		if _, err := r.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
		if got := r.LineCount(); got != i {
			t.Errorf("after %d writes: LineCount = %d, want %d", i, got, i)
		}
	}
	r.Close()

	// Reopen the same-day file for append — counter should resume.
	r2, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()

	if got := r2.LineCount(); got != 3 {
		t.Errorf("after reopen: LineCount = %d, want 3", got)
	}

	if _, err := r2.Write([]byte("fourth\n")); err != nil {
		t.Fatal(err)
	}
	if got := r2.LineCount(); got != 4 {
		t.Errorf("after fourth write: LineCount = %d, want 4", got)
	}
}

func TestRotator_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")

	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}
