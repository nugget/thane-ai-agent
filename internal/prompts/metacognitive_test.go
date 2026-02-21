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
	if !strings.Contains(result, "file_write") {
		t.Error("first-iteration placeholder should mention file_write")
	}
}

func TestMetacognitivePrompt_SetNextSleepMentioned(t *testing.T) {
	result := MetacognitivePrompt("some state", false)

	if !strings.Contains(result, "set_next_sleep") {
		t.Error("prompt should mention set_next_sleep tool")
	}
}
