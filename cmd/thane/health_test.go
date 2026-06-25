package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunHealth(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "healthy 200", status: http.StatusOK, wantErr: false},
		{name: "no content 204", status: http.StatusNoContent, wantErr: false},
		{name: "service unavailable 503", status: http.StatusServiceUnavailable, wantErr: true},
		{name: "server error 500", status: http.StatusInternalServerError, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			var out bytes.Buffer
			err := runHealth(context.Background(), &out, []string{srv.URL + "/health"})
			if tt.wantErr && err == nil {
				t.Fatalf("runHealth(status=%d) = nil error, want error", tt.status)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("runHealth(status=%d) = %v, want nil", tt.status, err)
			}
			if !tt.wantErr && !strings.Contains(out.String(), "ok") {
				t.Errorf("runHealth(status=%d) output = %q, want it to contain \"ok\"", tt.status, out.String())
			}
		})
	}
}

func TestRunHealth_Unreachable(t *testing.T) {
	// Port 1 is reserved and never listens — connection is refused fast.
	var out bytes.Buffer
	if err := runHealth(context.Background(), &out, []string{"http://127.0.0.1:1/health"}); err == nil {
		t.Fatal("runHealth against an unreachable endpoint = nil error, want error")
	}
}
