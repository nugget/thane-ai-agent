package homeassistant_test

// External test package: contextfmt imports homeassistant, so wiring the
// real canonical translator into the state window can only be exercised
// from outside the package — exactly the shape the app wiring uses.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/contextfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// The prod garage-bay scenario end-to-end with the real vocabulary: a
// binary_sensor shown as garage_door must render closed→open in the
// ambient state window, matching every other entity-emitting surface.
func TestStateWindow_ContextfmtVocabularyEndToEnd(t *testing.T) {
	p := homeassistant.NewStateWindowProvider(10, 30*time.Minute, contextfmt.SemanticState, nil)

	p.HandleStateChange("binary_sensor.zone25_garage_bay_3", "off", "on", "garage_door")
	p.HandleStateChange("binary_sensor.leak", "off", "on", "moisture")
	p.HandleStateChange("light.office", "off", "on", "")

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, `"from":"closed"`) || !strings.Contains(got, `"to":"open"`) {
		t.Errorf("garage_door transition = want closed→open, got:\n%s", got)
	}
	if !strings.Contains(got, `"from":"dry"`) || !strings.Contains(got, `"to":"wet"`) {
		t.Errorf("moisture transition = want dry→wet, got:\n%s", got)
	}
	// A light has no binary_sensor translation — raw on/off passes through.
	if !strings.Contains(got, `{"entity":"light.office","from":"off","to":"on"`) {
		t.Errorf("light transition should stay raw off→on, got:\n%s", got)
	}
}
