package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

type fakeLoopReg struct{ statuses []looppkg.Status }

func (f fakeLoopReg) Statuses() []looppkg.Status { return f.statuses }
func (f fakeLoopReg) Get(string) *looppkg.Loop   { return nil }

func quietServer(reg LoopStatusReader) *Server {
	return &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		loopRegistry: reg,
	}
}

func TestHandleLoops(t *testing.T) {
	s := quietServer(fakeLoopReg{statuses: []looppkg.Status{
		{ID: "a", Name: "alpha", State: looppkg.StateSleeping},
		{ID: "b", Name: "beta", State: looppkg.StateError},
	}})

	rr := httptest.NewRecorder()
	s.handleLoops(rr, httptest.NewRequest(http.MethodGet, "/v1/loops", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var all []looppkg.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered loops = %d, want 2", len(all))
	}

	rr2 := httptest.NewRecorder()
	s.handleLoops(rr2, httptest.NewRequest(http.MethodGet, "/v1/loops?state=sleeping", nil))
	var filtered []looppkg.Status
	if err := json.Unmarshal(rr2.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "a" {
		t.Errorf("?state=sleeping = %+v, want exactly [a]", filtered)
	}
}

func TestHandleLoops_Unconfigured(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rr := httptest.NewRecorder()
	s.handleLoops(rr, httptest.NewRequest(http.MethodGet, "/v1/loops", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when registry unset", rr.Code)
	}
}

func TestHandleLoop_NotFound(t *testing.T) {
	s := quietServer(fakeLoopReg{})
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/nope", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	s.handleLoop(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
