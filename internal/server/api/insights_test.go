package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleInsights_Unconfigured(t *testing.T) {
	s := &Server{logger: testAPILogger()} // no router, no memory store

	rr := httptest.NewRecorder()
	s.handleRouterInsights(rr, httptest.NewRequest(http.MethodGet, "/v1/insights/router", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("router insights: status = %d, want 503 when router unset", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.handleToolInsights(rr2, httptest.NewRequest(http.MethodGet, "/v1/insights/tools", nil))
	if rr2.Code != http.StatusServiceUnavailable {
		t.Errorf("tool insights: status = %d, want 503 when memory store unset", rr2.Code)
	}
}
