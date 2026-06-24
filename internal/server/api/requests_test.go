package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

type fakeRequestReader struct {
	detail *logging.RequestDetail
	err    error
}

func (f fakeRequestReader) QueryRequestDetail(string) (*logging.RequestDetail, error) {
	return f.detail, f.err
}

func reqServer(rr RequestReader) *Server {
	return &Server{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		requestReader: rr,
	}
}

func getByID(path, id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("id", id)
	return req
}

func TestHandleRequest_Found(t *testing.T) {
	s := reqServer(fakeRequestReader{detail: &logging.RequestDetail{RequestID: "r_1", Model: "m"}})
	rr := httptest.NewRecorder()
	s.handleRequest(rr, getByID("/v1/requests/r_1", "r_1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var d logging.RequestDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.RequestID != "r_1" {
		t.Errorf("request_id = %q, want r_1", d.RequestID)
	}
}

func TestHandleRequest_NotFound(t *testing.T) {
	s := reqServer(fakeRequestReader{detail: nil})
	rr := httptest.NewRecorder()
	s.handleRequest(rr, getByID("/v1/requests/missing", "missing"))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHandleRequest_NoReader(t *testing.T) {
	s := reqServer(nil)
	rr := httptest.NewRecorder()
	s.handleRequest(rr, getByID("/v1/requests/r_1", "r_1"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when reader unset", rr.Code)
	}
}

func TestHandleRequestTools_BareArray(t *testing.T) {
	s := reqServer(fakeRequestReader{detail: &logging.RequestDetail{
		ToolCalls: []logging.ToolDetail{{ToolName: "search"}, {ToolName: "fetch"}},
	}})
	rr := httptest.NewRecorder()
	s.handleRequestTools(rr, getByID("/v1/requests/r_1/tools", "r_1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if b := strings.TrimSpace(rr.Body.String()); !strings.HasPrefix(b, "[") {
		t.Fatalf("body = %s, want a bare JSON array", b)
	}
	var tools []logging.ToolDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &tools); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tools) != 2 || tools[0].ToolName != "search" {
		t.Errorf("tools = %+v, want [search, fetch]", tools)
	}
}

func TestHandleRequestTools_EmptyIsArray(t *testing.T) {
	s := reqServer(fakeRequestReader{detail: &logging.RequestDetail{}}) // ToolCalls nil
	rr := httptest.NewRecorder()
	s.handleRequestTools(rr, getByID("/v1/requests/r_1/tools", "r_1"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if b := strings.TrimSpace(rr.Body.String()); b != "[]" {
		t.Errorf("empty tools body = %q, want %q", b, "[]")
	}
}

func TestHandleRequestRouting_Unconfigured(t *testing.T) {
	s := reqServer(fakeRequestReader{}) // router left nil
	rr := httptest.NewRecorder()
	s.handleRequestRouting(rr, getByID("/v1/requests/r_1/routing", "r_1"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when router unset", rr.Code)
	}
}
