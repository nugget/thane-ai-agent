package prompts

import (
	"strings"
	"testing"
)

func TestMetacognitivePrompt_Normal(t *testing.T) {
	state := "## Current Sense\nAll quiet."
	result := MetacognitivePrompt(state, false)

	if !strings.Contains(result, "All quiet.") {
		t.Error("prompt should contain state file content")
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

	if !strings.Contains(result, "Monitoring garage.") {
		t.Error("supervisor prompt should contain state file content")
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
}

func TestMetacognitivePrompt_EmptyState(t *testing.T) {
	result := MetacognitivePrompt("", false)

	if !strings.Contains(result, "does not exist yet") {
		t.Error("empty state should produce first-iteration placeholder")
	}
	if !strings.Contains(result, "update_metacognitive_state") {
		t.Error("first-iteration placeholder should mention update_metacognitive_state")
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

	if !strings.Contains(result, "update_metacognitive_state") {
		t.Error("prompt should mention update_metacognitive_state tool")
	}
	if strings.Contains(result, "file_write") {
		t.Error("prompt should not mention file_write (replaced by update_metacognitive_state)")
	}
}

func TestMetacognitivePrompt_FileToolsNotAvailable(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "file tools are NOT available") {
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
	if !strings.Contains(result, "File tools, exec, and") {
		t.Error("prompt should explicitly list unavailable tool categories")
	}
}

func TestMetacognitivePrompt_OnlyWriteMechanism(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "ONLY tool that writes your state file") {
		t.Error("prompt should clarify update_metacognitive_state is the only write mechanism")
	}
}
