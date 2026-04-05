package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLaunchMarshalJSONUsesDurationStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(Launch{
		Task:         "test launch",
		RunTimeout:   2 * time.Minute,
		ToolTimeout:  45 * time.Second,
		AllowedTools: []string{"get_state"},
	})
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"run_timeout":"2m0s"`) {
		t.Fatalf("marshal output missing string run_timeout: %s", body)
	}
	if !strings.Contains(body, `"tool_timeout":"45s"`) {
		t.Fatalf("marshal output missing string tool_timeout: %s", body)
	}
}

func TestLaunchUnmarshalJSONParsesDurationStrings(t *testing.T) {
	t.Parallel()

	var launch Launch
	if err := json.Unmarshal([]byte(`{
		"task":"test launch",
		"run_timeout":"90s",
		"tool_timeout":"30s",
		"completion_channel":{"channel":"owu","conversation_id":"conv-1"}
	}`), &launch); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if launch.RunTimeout != 90*time.Second {
		t.Fatalf("RunTimeout = %v, want 90s", launch.RunTimeout)
	}
	if launch.ToolTimeout != 30*time.Second {
		t.Fatalf("ToolTimeout = %v, want 30s", launch.ToolTimeout)
	}
	if launch.CompletionChannel == nil || launch.CompletionChannel.Channel != "owu" || launch.CompletionChannel.ConversationID != "conv-1" {
		t.Fatalf("CompletionChannel = %#v", launch.CompletionChannel)
	}
}

func TestLaunchUnmarshalJSONRejectsInvalidDurationStrings(t *testing.T) {
	t.Parallel()

	var launch Launch
	err := json.Unmarshal([]byte(`{"run_timeout":"definitely-not-a-duration"}`), &launch)
	if err == nil || !strings.Contains(err.Error(), "run_timeout") {
		t.Fatalf("UnmarshalJSON error = %v, want run_timeout parse error", err)
	}
}
