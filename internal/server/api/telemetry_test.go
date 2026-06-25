package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

func TestHandleTelemetry_Unconfigured(t *testing.T) {
	s := &Server{logger: testAPILogger()} // no router, no memory store

	rr := httptest.NewRecorder()
	s.handleRouterTelemetry(rr, httptest.NewRequest(http.MethodGet, "/v1/telemetry/router", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("router telemetry: status = %d, want 503 when router unset", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.handleToolTelemetry(rr2, httptest.NewRequest(http.MethodGet, "/v1/telemetry/tools", nil))
	if rr2.Code != http.StatusServiceUnavailable {
		t.Errorf("tool telemetry: status = %d, want 503 when memory store unset", rr2.Code)
	}
}

func TestHandleCapabilities_Unconfigured(t *testing.T) {
	s := &Server{logger: testAPILogger()} // capSurface nil
	rr := httptest.NewRecorder()
	s.handleCapabilities(rr, httptest.NewRequest(http.MethodGet, "/v1/telemetry/capabilities", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when capability surface unset", rr.Code)
	}
}

func TestHandleCapability_NotFoundAndMissingTag(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	s.UseCapabilitySurface(func() []toolcatalog.CapabilitySurface {
		return []toolcatalog.CapabilitySurface{{Tag: "ha"}}
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/telemetry/capabilities/nope", nil)
	req.SetPathValue("tag", "nope")
	rr := httptest.NewRecorder()
	s.handleCapability(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown tag: status = %d, want 404", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/telemetry/capabilities/", nil)
	req2.SetPathValue("tag", "")
	rr2 := httptest.NewRecorder()
	s.handleCapability(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("missing tag: status = %d, want 400", rr2.Code)
	}
}
