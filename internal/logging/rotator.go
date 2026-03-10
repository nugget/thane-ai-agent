package logging

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// activeLogName is the filename for the current day's log.
const activeLogName = "thane.log"

// nowFunc is the time source, overridable in tests.
var nowFunc = time.Now

// Rotator is a file-backed [io.WriteCloser] that rotates log files at
// date boundaries. Each day's log is written to thane.log; on rotation
// the previous day's file is renamed to thane-YYYY-MM-DD.log (and
// optionally gzip-compressed to thane-YYYY-MM-DD.log.gz).
//
// Rotator is safe for concurrent use.
type Rotator struct {
	dir      string
	compress bool

	mu        sync.Mutex
	file      *os.File
	curDate   string // YYYY-MM-DD of the currently open file
	lineCount int    // lines written to the current active file
}

// Open creates a [Rotator] that writes to dir/thane.log. The directory
// is created if it does not exist. If compress is true, rotated files
// are gzip-compressed.
//
// On open, if thane.log already exists and belongs to a previous date
// (determined by the file's last modification time), it is rotated
// immediately.
func Open(dir string, compress bool) (*Rotator, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory %s: %w", dir, err)
	}

	r := &Rotator{
		dir:      dir,
		compress: compress,
	}

	if err := r.rotateIfNeeded(); err != nil {
		return nil, err
	}

	return r, nil
}

// Write writes p to the active log file, rotating first if the date
// has changed since the last write.
func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	today := nowFunc().Format(time.DateOnly)
	if today != r.curDate {
		if err := r.rotateLocked(); err != nil {
			return 0, fmt.Errorf("rotate log: %w", err)
		}
	}

	n, err := r.file.Write(p)
	if err == nil {
		r.lineCount++
	}
	return n, err
}

// LineCount returns the number of lines written to the current active
// file since it was opened (or last rotated). Caller must hold no lock;
// the count is read under the internal mutex.
func (r *Rotator) LineCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lineCount
}

// ActiveFile returns the filename (not full path) of the current active
// log file. This is always [activeLogName] ("thane.log").
func (r *Rotator) ActiveFile() string {
	return activeLogName
}

// Close closes the active log file.
func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}

// rotateIfNeeded checks whether the existing thane.log belongs to a
// previous date and rotates it if so. Called once during Open.
func (r *Rotator) rotateIfNeeded() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	activePath := filepath.Join(r.dir, activeLogName)

	info, err := os.Stat(activePath)
	if err == nil {
		// File exists — check if it belongs to a previous date.
		fileDate := info.ModTime().Format(time.DateOnly)
		today := nowFunc().Format(time.DateOnly)

		if fileDate != today {
			// Rotate the stale file before opening a new one.
			if err := r.archiveFile(activePath, fileDate); err != nil {
				return err
			}
		}
	}

	return r.openActive()
}

// rotateLocked closes the current file and archives it under yesterday's
// date, then opens a fresh thane.log. Caller must hold r.mu.
func (r *Rotator) rotateLocked() error {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}

	activePath := filepath.Join(r.dir, activeLogName)

	// Archive under the date we were writing (r.curDate), not today.
	if r.curDate != "" {
		if err := r.archiveFile(activePath, r.curDate); err != nil {
			return err
		}
	}

	return r.openActive()
}

// openActive opens (or creates) thane.log for append and records today's date.
func (r *Rotator) openActive() error {
	activePath := filepath.Join(r.dir, activeLogName)

	f, err := os.OpenFile(activePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", activePath, err)
	}

	r.file = f
	r.curDate = nowFunc().Format(time.DateOnly)
	r.lineCount = 0
	return nil
}

// archiveFile renames src to a date-stamped name and optionally
// compresses it. The date parameter determines the archive filename.
func (r *Rotator) archiveFile(src string, date string) error {
	if r.compress {
		return r.compressFile(src, date)
	}

	dst := filepath.Join(r.dir, fmt.Sprintf("thane-%s.log", date))
	return os.Rename(src, dst)
}

// compressFile gzip-compresses src into thane-{date}.log.gz and removes
// the original.
func (r *Rotator) compressFile(src string, date string) error {
	dst := filepath.Join(r.dir, fmt.Sprintf("thane-%s.log.gz", date))

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open log for compression: %w", err)
	}
	// No defer — we close explicitly before os.Remove so the file
	// handle is released (required on Windows).

	out, err := os.Create(dst)
	if err != nil {
		in.Close()
		return fmt.Errorf("create compressed log %s: %w", dst, err)
	}

	gz := gzip.NewWriter(out)

	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = in.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("compress log: %w", err)
	}

	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = in.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("finalize compressed log: %w", err)
	}

	if err := out.Close(); err != nil {
		_ = in.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("close compressed log: %w", err)
	}

	if err := in.Close(); err != nil {
		return fmt.Errorf("close source log: %w", err)
	}

	return os.Remove(src)
}
