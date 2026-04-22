package logging

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessResponseWriter_DefaultStatusAndBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewAccessResponseWriter(rec)

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := w.StatusCode(); got != http.StatusOK {
		t.Errorf("StatusCode() = %d, want %d", got, http.StatusOK)
	}
	if got := w.BytesWritten(); got != 5 {
		t.Errorf("BytesWritten() = %d, want 5", got)
	}
	if body := rec.Body.String(); body != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestAccessResponseWriter_WriteHeaderOverridesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewAccessResponseWriter(rec)

	w.WriteHeader(http.StatusCreated)
	if _, err := w.Write([]byte("created")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := w.StatusCode(); got != http.StatusCreated {
		t.Errorf("StatusCode() = %d, want %d", got, http.StatusCreated)
	}
	if got := w.BytesWritten(); got != int64(len("created")) {
		t.Errorf("BytesWritten() = %d, want %d", got, len("created"))
	}
	if !strings.Contains(rec.Body.String(), "created") {
		t.Errorf("body = %q, want it to contain %q", rec.Body.String(), "created")
	}
}

// TestAccessResponseWriter_Unwrap verifies that http.NewResponseController
// can walk through the middleware wrapper to reach the underlying
// ResponseWriter. Without Unwrap, streaming handlers (SSE, long-poll)
// behind this middleware cannot adjust their read/write deadlines.
func TestAccessResponseWriter_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewAccessResponseWriter(rec)

	if got := w.Unwrap(); got != http.ResponseWriter(rec) {
		t.Errorf("Unwrap() = %v, want %v", got, rec)
	}

	// http.NewResponseController uses Unwrap to walk wrappers. The
	// important signal is that SetWriteDeadline reaches *past* the
	// middleware — httptest.ResponseRecorder itself does not implement
	// deadlines, so the error should be http.ErrNotSupported (from the
	// underlying writer) rather than propagating a wrapper problem.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("SetWriteDeadline() unexpected error = %v", err)
	}
}
