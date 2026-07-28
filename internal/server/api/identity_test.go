package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/identity"
)

func TestHandleIdentity(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	s.UseIdentityEvidence(func(context.Context) (identity.Evidence, error) {
		return identity.Evidence{
			SchemaVersion: 1,
			Instance: identity.InstanceEvidence{
				ID: "thane:ed25519:SHA256:test",
			},
		}, nil
	})

	rr := httptest.NewRecorder()
	s.handleIdentity(rr, httptest.NewRequest(http.MethodGet, "/v1/identity", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body identity.Evidence
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Instance.ID != "thane:ed25519:SHA256:test" {
		t.Errorf("instance id = %q", body.Instance.ID)
	}
}

func TestHandleIdentityUnavailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reader IdentityEvidenceReader
	}{
		{name: "not configured"},
		{
			name: "observation failed",
			reader: func(context.Context) (identity.Evidence, error) {
				return identity.Evidence{}, errors.New("private path must not reach client")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{logger: testAPILogger(), identityEvidence: tc.reader}
			rr := httptest.NewRecorder()
			s.handleIdentity(rr, httptest.NewRequest(http.MethodGet, "/v1/identity", nil))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rr.Code)
			}
			if body := rr.Body.String(); body == "" || body == "private path must not reach client" {
				t.Fatalf("body leaked internal error or was empty: %q", body)
			}
		})
	}
}
