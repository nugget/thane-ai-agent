package prompts

import (
	"strings"
	"testing"
)

func TestFarewellPrompt(t *testing.T) {
	result := FarewellPrompt("idle timeout", "duration: 45m", "user: hello\nassistant: hi there")

	if !strings.Contains(result, "idle timeout") {
		t.Error("prompt should contain close reason")
	}
	if !strings.Contains(result, "duration: 45m") {
		t.Error("prompt should contain session stats")
	}
	if !strings.Contains(result, "user: hello") {
		t.Error("prompt should contain transcript")
	}
	if !strings.Contains(result, "farewell") {
		t.Error("prompt should mention farewell field")
	}
	if !strings.Contains(result, "carry_forward") {
		t.Error("prompt should mention carry_forward field")
	}
}
