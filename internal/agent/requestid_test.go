package agent

import (
	"strings"
	"testing"
)

func TestGenerateRequestID(t *testing.T) {
	id := generateRequestID()

	if !strings.HasPrefix(id, "r_") {
		t.Errorf("request ID %q missing r_ prefix", id)
	}

	// r_ prefix + 8 hex chars = 10 total
	if len(id) != 10 {
		t.Errorf("request ID %q length = %d, want 10", id, len(id))
	}

	// Hex chars only after prefix.
	for _, c := range id[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("request ID %q contains non-hex char %q", id, string(c))
		}
	}
}

func TestGenerateRequestID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateRequestID()
		if seen[id] {
			t.Errorf("duplicate request ID %q after %d iterations", id, i)
		}
		seen[id] = true
	}
}
