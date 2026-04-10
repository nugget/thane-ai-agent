package metacognitive

import (
	"path/filepath"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestRegisterTools_RegistersExpectedTools(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	if reg.Get("update_metacognitive_state") == nil {
		t.Error("update_metacognitive_state should be registered")
	}

	// append_ego_observation was removed in #575 — verify it's gone.
	if reg.Get("append_ego_observation") != nil {
		t.Error("append_ego_observation should not be registered")
	}
}
