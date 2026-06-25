package archivist

import (
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestParseConfig_Valid(t *testing.T) {
	jitter, supervisor := 0.2, 0.1
	raw := config.ArchivistConfig{
		Enabled:               true,
		MinSleep:              "15m",
		MaxSleep:              "12h",
		DefaultSleep:          "1h",
		Jitter:                &jitter,
		SupervisorProbability: &supervisor,
		Router:                config.RouterConfig{QualityFloor: 5},
		SupervisorRouter:      config.RouterConfig{QualityFloor: 8},
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	if cfg.MinSleep != 15*time.Minute {
		t.Errorf("MinSleep = %v, want 15m", cfg.MinSleep)
	}
	if cfg.MaxSleep != 12*time.Hour {
		t.Errorf("MaxSleep = %v, want 12h", cfg.MaxSleep)
	}
	if cfg.DefaultSleep != time.Hour {
		t.Errorf("DefaultSleep = %v, want 1h", cfg.DefaultSleep)
	}
	if cfg.StateFile != stateFileName {
		t.Errorf("StateFile = %q, want %q", cfg.StateFile, stateFileName)
	}
	if cfg.QualityFloor != 5 {
		t.Errorf("QualityFloor = %d, want 5", cfg.QualityFloor)
	}
	if cfg.SupervisorQualityFloor != 8 {
		t.Errorf("SupervisorQualityFloor = %d, want 8", cfg.SupervisorQualityFloor)
	}
	if cfg.SupervisorProbability != 0.1 {
		t.Errorf("SupervisorProbability = %v, want 0.1", cfg.SupervisorProbability)
	}
}

func TestParseConfig_InvalidDuration(t *testing.T) {
	cases := []struct {
		name string
		raw  config.ArchivistConfig
	}{
		{"bad_min", config.ArchivistConfig{MinSleep: "junk", MaxSleep: "12h", DefaultSleep: "1h"}},
		{"bad_max", config.ArchivistConfig{MinSleep: "15m", MaxSleep: "junk", DefaultSleep: "1h"}},
		{"bad_default", config.ArchivistConfig{MinSleep: "15m", MaxSleep: "12h", DefaultSleep: "junk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConfig(tc.raw); err == nil {
				t.Fatal("ParseConfig: want error, got nil")
			}
		})
	}
}

func TestDefinitionSpec_Outputs(t *testing.T) {
	cfg := Config{
		Enabled:                true,
		MinSleep:               15 * time.Minute,
		MaxSleep:               12 * time.Hour,
		DefaultSleep:           time.Hour,
		Jitter:                 0.2,
		SupervisorProbability:  0.1,
		QualityFloor:           5,
		SupervisorQualityFloor: 8,
	}
	spec := DefinitionSpec(cfg)

	if spec.Name != DefinitionName {
		t.Errorf("Name = %q, want %q", spec.Name, DefinitionName)
	}
	if spec.Operation != loop.OperationService {
		t.Errorf("Operation = %q, want %q", spec.Operation, loop.OperationService)
	}
	if len(spec.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(spec.Outputs))
	}
	out := spec.Outputs[0]
	if out.Ref != "core:archivist.md" {
		t.Errorf("Outputs[0].Ref = %q, want core:archivist.md", out.Ref)
	}
	if out.Type != loop.OutputTypeMaintainedDocument {
		t.Errorf("Outputs[0].Type = %q, want maintained_document", out.Type)
	}
	if out.EffectiveMode() != loop.OutputModeReplace {
		t.Errorf("Outputs[0].Mode = %q, want replace", out.EffectiveMode())
	}
	if !strings.HasPrefix(out.ToolName(), "replace_output_") {
		t.Errorf("ToolName = %q, want replace_output_* prefix", out.ToolName())
	}
	if !spec.Supervisor {
		t.Error("Supervisor should be enabled when SupervisorProbability > 0")
	}
	if spec.SleepMin != 15*time.Minute {
		t.Errorf("SleepMin = %v, want 15m", spec.SleepMin)
	}
	if spec.Profile.Mission != "archivist" {
		t.Errorf("Profile.Mission = %q, want archivist", spec.Profile.Mission)
	}
	if spec.Profile.DelegationGating != "disabled" {
		t.Errorf("Profile.DelegationGating = %q, want disabled", spec.Profile.DelegationGating)
	}
	// Tags = the archivist's initial capability surface. "archivist" is
	// the identity tag; the rest are the tool families the prompt
	// promises (and tag_activate is excluded, so this is the full
	// boot-time set the archivist can use).
	wantTags := map[string]bool{
		"archivist": true, "documents": true, "archive": true,
		"memory": true, "contacts": true,
	}
	if len(spec.Tags) != len(wantTags) {
		t.Errorf("Tags = %v, want %d entries", spec.Tags, len(wantTags))
	}
	for _, tag := range spec.Tags {
		if !wantTags[tag] {
			t.Errorf("unexpected Tag %q in spec.Tags = %v", tag, spec.Tags)
		}
	}
	for tag := range wantTags {
		found := false
		for _, t := range spec.Tags {
			if t == tag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing Tag %q from spec.Tags = %v", tag, spec.Tags)
		}
	}
	if spec.Profile.ExtraHints["source"] != "archivist" {
		t.Errorf("Profile.ExtraHints[source] = %q, want archivist", spec.Profile.ExtraHints["source"])
	}
}

func TestSpec_DeclarativePrompt(t *testing.T) {
	cfg := Config{Enabled: true, SupervisorProbability: 0.1}
	spec := HydrateSpec(DefinitionSpec(cfg), cfg)

	// The archivist loop is declarative: no TaskBuilder closure (its one
	// genuine runtime hook, the work-queue tools, is attached at app
	// hydration, not here).
	if spec.TaskBuilder != nil {
		t.Error("archivist loop is declarative; HydrateSpec should attach no TaskBuilder")
	}
	if !strings.Contains(spec.Task, "Archivist loop iteration") {
		t.Errorf("spec.Task should be the archivist base prompt, got %q", spec.Task)
	}
	if strings.Contains(spec.Task, "Supervisor Review") {
		t.Error("base Task should not include the supervisor section")
	}
	if spec.SupervisorProfile == nil || !strings.Contains(spec.SupervisorProfile.Instructions, "Supervisor Review") {
		t.Error("supervisor-turn prefix should live in SupervisorProfile.Instructions")
	}
}

// TestExcludeTools_LocksDownHumanEgressFileWritesAndSpawns verifies the
// archivist can't reach for tools outside its declared surface. The
// archivist's job is silent synthesis: no Signal/notification sends, no
// bare workspace file writes (managed output handles state, the
// documents tools handle dossiers), no tag manipulation, no session
// control, and — as a background-class consumer (#1024) — no loop
// spawning. Documents tools and read-side tools (archive_search,
// recall_fact, contact_lookup) are deliberately NOT excluded.
func TestExcludeTools_LocksDownHumanEgressFileWritesAndSpawns(t *testing.T) {
	spec := DefinitionSpec(Config{})

	excluded := make(map[string]bool, len(spec.ExcludeTools))
	for _, name := range spec.ExcludeTools {
		excluded[name] = true
	}

	mustExclude := []string{
		"file_write", "file_edit", "exec",
		"conversation_reset", "session_close",
		"tag_activate", "tag_deactivate",
		// Zero spawn rights for the background class (#1024).
		"spawn_loop", "thane_now", "thane_assign", "thane_curate", "thane_create_container",
	}
	for _, tool := range mustExclude {
		if !excluded[tool] {
			t.Errorf("expected %q in ExcludeTools", tool)
		}
	}

	// Tools the archivist DOES need access to should not be excluded.
	mustNotExclude := []string{
		"archive_search", "recall_fact", "contact_lookup",
		"set_next_sleep",
	}
	for _, tool := range mustNotExclude {
		if excluded[tool] {
			t.Errorf("%q should remain available to the archivist", tool)
		}
	}
}
