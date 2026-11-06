package fleet_test

import (
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// A caller replaying a recorded call builds the client the way
// production does, then overrides the serving settings with the ones
// the recording was made under rather than the local config's.
func ExampleNewResourceClient() {
	cfg := &config.Config{Models: config.ModelsConfig{Resources: map[string]config.ModelServerConfig{
		"local": {ChatTemplateKwargs: map[string]any{"enable_thinking": false}},
	}}}
	res := fleet.Resource{ID: "local", Provider: "lmstudio", URL: "http://127.0.0.1:1234", IdleTTLSeconds: 600}

	rc, err := fleet.NewResourceClient(res, cfg,
		fleet.WithIdleTTLSeconds(300),
		fleet.WithChatTemplateKwargs(nil),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("lmstudio:", rc.LMStudio != nil, "watchable:", rc.Health != nil)
	// Output: lmstudio: true watchable: true
}
