package forge

import (
	"strings"
	"testing"
)

// TestForgeAccountDescriptionTeachesBinding pins the tool surface and
// the loop-spec surface to the same story.
//
// The spec schema tells a bound loop that an omitted account resolves
// to its binding. If the tool's own account parameter says the default
// is the primary account, a model holding both statements has no way
// to tell which is true — and it is likelier to act on the one
// attached to the tool it is calling. This drifted once already: the
// forge refactor consolidated nineteen per-tool descriptions into one
// constant and reintroduced the primary-account wording, which is
// exactly the kind of quiet regression a description is prone to.
func TestForgeAccountDescriptionTeachesBinding(t *testing.T) {
	t.Parallel()

	if !strings.Contains(forgeAccountDescription, "bound account") {
		t.Errorf("forgeAccountDescription does not mention the bound account: %q", forgeAccountDescription)
	}
	if strings.Contains(forgeAccountDescription, "Omit to use the primary") {
		t.Errorf("forgeAccountDescription promises the primary account unconditionally, which a binding falsifies: %q", forgeAccountDescription)
	}
}

// TestEveryForgeToolAccountParamUsesTheSharedDescription guards the
// other half: the constant is only worth pinning if every tool
// actually uses it. A tool that spells its own account description
// inline would drift silently.
func TestEveryForgeToolAccountParamUsesTheSharedDescription(t *testing.T) {
	t.Parallel()

	tools := &Tools{}
	var checked int
	for _, tool := range tools.coreToolDefinitions() {
		if tool == nil {
			continue
		}
		props, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			continue
		}
		raw, ok := props["account"]
		if !ok {
			continue
		}
		account, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("%s: account parameter is not an object schema", tool.Name)
			continue
		}
		checked++
		if desc, _ := account["description"].(string); desc != forgeAccountDescription {
			t.Errorf("%s: account description is not the shared constant: %q", tool.Name, desc)
		}
	}
	if checked == 0 {
		t.Fatal("no forge tools with an account parameter were checked; the registration path changed")
	}
}
