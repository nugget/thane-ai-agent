package logging

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
