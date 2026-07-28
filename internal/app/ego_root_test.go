package app

import (
	"path/filepath"
	"testing"
)

// The ego loop writes self:ego.md and the interactive agent injects ego.md
// beside axioms, persona, and mission. Those two facts are wired in different
// files, and when they disagree the failure is silent: the loop keeps writing,
// the prompt section simply stops appearing, and the agent composes
// self-reflection it never reads back.
//
// So this pins the pairing rather than the path. axioms, persona, and mission
// are what the operator declares Thane to be and resolve under core; ego.md is
// what Thane has made of that and resolves under self.
func TestCorePromptFilesResolveUnderTheRootThatOwnsThem(t *testing.T) {
	const workspace = "/tmp/workspace"

	tests := []struct {
		name     string
		got      string
		wantRoot string
	}{
		{"axioms is declared by the operator", coreFilePath(workspace, "axioms.md"), "core"},
		{"persona is declared by the operator", coreFilePath(workspace, "persona.md"), "core"},
		{"mission is declared by the operator", coreFilePath(workspace, "mission.md"), "core"},
		{"ego is written by the agent", selfFilePath(workspace, "ego.md"), "self"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantDir := filepath.Join(workspace, tc.wantRoot)
			if dir := filepath.Dir(tc.got); dir != wantDir {
				t.Errorf("resolved to %s, want a file under %s", tc.got, wantDir)
			}
		})
	}
}

// A workspace-less config must not synthesize a path, or startup verification
// would be handed a relative path that resolves against the process working
// directory rather than failing honestly.
func TestSelfFilePathIsEmptyWithoutAWorkspace(t *testing.T) {
	if got := selfFilePath("", "ego.md"); got != "" {
		t.Errorf("selfFilePath with no workspace = %q, want empty", got)
	}
	if got := selfFilePath("/tmp/workspace", ""); got != "" {
		t.Errorf("selfFilePath with no name = %q, want empty", got)
	}
}
