package contextfmt

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestAttachMetadataAppendsWithoutReorderingBasePayload(t *testing.T) {
	t.Parallel()

	formatted := `{"entity":"sensor.office","state":"72","since":"-1s"}`
	metadata := &homeassistant.EntityMetadata{
		Area: &homeassistant.EntityAreaMetadata{
			ID:   "office",
			Name: "Office",
		},
	}

	got := AttachMetadata(formatted, metadata)
	want := `{"entity":"sensor.office","state":"72","since":"-1s","metadata":{"area":{"id":"office","name":"Office"}}}`
	if got != want {
		t.Fatalf("AttachMetadata() = %s, want %s", got, want)
	}
}

func TestAttachMetadataSkipsNonJSONObject(t *testing.T) {
	t.Parallel()

	formatted := `["sensor.office"]`
	metadata := &homeassistant.EntityMetadata{
		Area: &homeassistant.EntityAreaMetadata{ID: "office"},
	}

	if got := AttachMetadata(formatted, metadata); got != formatted {
		t.Fatalf("AttachMetadata() = %s, want original payload", got)
	}
}
