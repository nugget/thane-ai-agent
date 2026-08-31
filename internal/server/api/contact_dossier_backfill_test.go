package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
)

func TestHandleContactDossierBackfill(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		server := &Server{logger: testAPILogger()}
		recorder := httptest.NewRecorder()
		server.handleContactDossierBackfill(recorder, httptest.NewRequest(http.MethodPost,
			"/v1/archive/contact-dossier-backfill", nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("invalid limit", func(t *testing.T) {
		server := &Server{logger: testAPILogger()}
		server.ConfigureContactDossierBackfill(func(_ context.Context, _ int) (archivist.ContactDossierBackfillResult, error) {
			return archivist.ContactDossierBackfillResult{}, nil
		})
		recorder := httptest.NewRecorder()
		server.handleContactDossierBackfill(recorder, httptest.NewRequest(http.MethodPost,
			"/v1/archive/contact-dossier-backfill?limit=201", nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
	})

	t.Run("advances one batch", func(t *testing.T) {
		server := &Server{logger: testAPILogger()}
		var gotLimit int
		want := archivist.ContactDossierBackfillResult{
			Phase:     "contacts",
			NextPhase: "sessions",
			Cutoff:    time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
			Scanned:   2,
			Enqueued:  2,
		}
		server.ConfigureContactDossierBackfill(func(_ context.Context, limit int) (archivist.ContactDossierBackfillResult, error) {
			gotLimit = limit
			return want, nil
		})
		recorder := httptest.NewRecorder()
		server.handleContactDossierBackfill(recorder, httptest.NewRequest(http.MethodPost,
			"/v1/archive/contact-dossier-backfill?limit=2", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		if gotLimit != 2 {
			t.Fatalf("limit = %d, want 2", gotLimit)
		}
		if got := recorder.Body.String(); !strings.Contains(got, `"next_phase":"sessions"`) {
			t.Fatalf("body = %s", got)
		}
	})

	t.Run("reports advancement failure", func(t *testing.T) {
		server := &Server{logger: testAPILogger()}
		server.ConfigureContactDossierBackfill(func(_ context.Context, _ int) (archivist.ContactDossierBackfillResult, error) {
			return archivist.ContactDossierBackfillResult{}, errors.New("queue offline")
		})
		recorder := httptest.NewRecorder()
		server.handleContactDossierBackfill(recorder, httptest.NewRequest(http.MethodPost,
			"/v1/archive/contact-dossier-backfill", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
	})
}
