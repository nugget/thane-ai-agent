package prompts

import (
	"strings"
	"testing"
)

func TestMetacognitivePrompt_Normal(t *testing.T) {
	state := "## Current Sense\nAll quiet."
	result := MetacognitivePrompt(state, false)

	if strings.Contains(result, "All quiet.") {
		t.Error("prompt should not inline state file content")
	}
	if !strings.Contains(result, "Declared Durable") {
		t.Error("prompt should point to declared output context")
	}
	if !strings.Contains(result, "Metacognitive loop iteration") {
		t.Error("prompt should contain base template header")
	}
	if strings.Contains(result, "Supervisor Review") {
		t.Error("normal prompt should not contain supervisor augmentation")
	}
}

func TestMetacognitivePrompt_Supervisor(t *testing.T) {
	state := "## Current Sense\nMonitoring garage."
	result := MetacognitivePrompt(state, true)

	if strings.Contains(result, "Monitoring garage.") {
		t.Error("supervisor prompt should not inline state file content")
	}
	if !strings.Contains(result, "Supervisor Review") {
		t.Error("supervisor prompt should contain supervisor augmentation")
	}
	if !strings.Contains(result, "Blind spots") {
		t.Error("supervisor prompt should contain blind spots instruction")
	}
	if !strings.Contains(result, "Drift detection") {
		t.Error("supervisor prompt should contain drift detection instruction")
	}
	// append_ego_observation was removed in #575 — ego updates belong
	// in the interactive agent context, not the metacog loop.
	if strings.Contains(result, "append_ego_observation") {
		t.Error("supervisor prompt should not reference removed append_ego_observation tool")
	}
}

func TestMetacognitivePrompt_EmptyState(t *testing.T) {
	result := MetacognitivePrompt("", false)

	if !strings.Contains(result, "Declared Durable") {
		t.Error("empty state prompt should rely on declared output context")
	}
	if !strings.Contains(result, "replace_output_metacognitive_state") {
		t.Error("prompt should mention generated output tool")
	}
}

func TestMetacognitivePrompt_SetNextSleepMentioned(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "set_next_sleep") {
		t.Error("prompt should mention set_next_sleep tool")
	}
}

func TestMetacognitivePrompt_MentionsUpdateTool(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "replace_output_metacognitive_state") {
		t.Error("prompt should mention generated output tool")
	}
	if strings.Contains(result, "file_write") {
		t.Error("prompt should not mention file_write")
	}
}

func TestMetacognitivePrompt_FileToolsNotAvailable(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "File tools, exec, and session management") {
		t.Error("prompt should explicitly state file tools are not available")
	}
	if strings.Contains(result, "Read it carefully") {
		t.Error("prompt should not contain ambiguous 'Read it carefully' phrasing")
	}
}

func TestMetacognitivePrompt_ToolBoundary(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "exactly two special tools") {
		t.Error("prompt should state the two special tools available")
	}
	if strings.Contains(result, "append_ego_observation") {
		t.Error("prompt should not mention removed append_ego_observation tool")
	}
	if !strings.Contains(result, "File tools, exec,") {
		t.Error("prompt should explicitly list unavailable tool categories")
	}
}

func TestMetacognitivePrompt_TimestampConversionGuidance(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "relative deltas") {
		t.Error("prompt should explain that context timestamps are relative deltas")
	}
	if !strings.Contains(result, "absolute format (RFC3339") {
		t.Error("prompt should instruct conversion to absolute RFC3339 format")
	}
	if !strings.Contains(result, "Deltas become meaningless") {
		t.Error("prompt should explain why deltas must not be persisted")
	}
}

func TestMetacognitivePrompt_OnlyWriteMechanism(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "sanctioned interface") {
		t.Error("prompt should clarify the generated output tool is the only write mechanism")
	}
}
