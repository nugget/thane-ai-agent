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

	for _, name := range []string{"set_next_sleep", "update_metacognitive_state"} {
		if reg.Get(name) == nil {
			t.Errorf("%s should be registered", name)
		}
	}

	// append_ego_observation was removed in #575 — verify it's gone.
	if reg.Get("append_ego_observation") != nil {
		t.Error("append_ego_observation should not be registered")
	}
}
