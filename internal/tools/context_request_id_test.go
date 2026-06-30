package tools

import (
	"context"
	"testing"
)

// TestRequestIDContext_RoundTrip covers the request_id carrier added so tools
// can record which model turn authored a side effect (#1106 C2 loop origin).
func TestRequestIDContext_RoundTrip(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("bare context request id = %q, want empty", got)
	}
	ctx := WithRequestID(context.Background(), "r_abc123")
	if got := RequestIDFromContext(ctx); got != "r_abc123" {
		t.Errorf("request id = %q, want r_abc123", got)
	}
}
