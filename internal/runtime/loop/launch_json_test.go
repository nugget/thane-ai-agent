package loop

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestLaunchMarshalJSONUsesDurationStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(Launch{
		Task:            "test launch",
		RunTimeout:      2 * time.Minute,
		ToolTimeout:     45 * time.Second,
		AllowedTools:    []string{"get_state"},
		FallbackContent: "please try again",
		PromptMode:      agentctx.PromptModeTask,
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
	if !strings.Contains(body, `"fallback_content":"please try again"`) {
		t.Fatalf("marshal output missing fallback_content: %s", body)
	}
	if !strings.Contains(body, `"prompt_mode":"task"`) {
		t.Fatalf("marshal output missing prompt_mode: %s", body)
	}
}

func TestLaunchUnmarshalJSONParsesDurationStrings(t *testing.T) {
	t.Parallel()

	var launch Launch
	if err := json.Unmarshal([]byte(`{
		"task":"test launch",
		"run_timeout":"90s",
		"tool_timeout":"30s",
		"fallback_content":"please try again",
		"prompt_mode":"task",
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
	if launch.FallbackContent != "please try again" {
		t.Fatalf("FallbackContent = %q, want %q", launch.FallbackContent, "please try again")
	}
	if launch.PromptMode != agentctx.PromptModeTask {
		t.Fatalf("PromptMode = %q, want task", launch.PromptMode)
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

func TestLaunchValidateRejectsInvalidPromptMode(t *testing.T) {
	t.Parallel()

	launch := Launch{
		Spec: Spec{
			Name: "bad-prompt-mode",
			Task: "do work",
		},
		PromptMode: agentctx.PromptMode("everything"),
	}
	err := launch.Validate()
	if err == nil || !strings.Contains(err.Error(), "prompt_mode") {
		t.Fatalf("Validate error = %v, want prompt_mode validation error", err)
	}
}

func TestLaunchHasOverrides(t *testing.T) {
	t.Parallel()

	t.Run("nil", func(t *testing.T) {
		var l *Launch
		if l.HasOverrides() {
			t.Fatal("nil launch reported overrides")
		}
	})
	t.Run("zero-value", func(t *testing.T) {
		var l Launch
		if l.HasOverrides() {
			t.Fatal("zero-value launch reported overrides")
		}
	})
	t.Run("spec-only", func(t *testing.T) {
		// Caller-supplied Spec counts as an override: on the running-
		// service early-return path the runtime drops it silently, so
		// HasOverrides has to flag it. The normal launch path
		// overwrites it; flagging it there is harmless because no
		// caller of HasOverrides currently runs on that path.
		l := Launch{Spec: Spec{Name: "x", Task: "y"}}
		if !l.HasOverrides() {
			t.Fatal("spec-only launch did not report overrides")
		}
	})
	cases := []struct {
		name  string
		mut   func(*Launch)
		field string
	}{
		{"task", func(l *Launch) { l.Task = "x" }, "Task"},
		{"routing_factors", func(l *Launch) { l.RoutingFactors = map[string]string{"k": "v"} }, "RoutingFactors"},
		{"metadata", func(l *Launch) { l.Metadata = map[string]string{"k": "v"} }, "Metadata"},
		{"allowed_tools", func(l *Launch) { l.AllowedTools = []string{"t"} }, "AllowedTools"},
		{"exclude_tools", func(l *Launch) { l.ExcludeTools = []string{"t"} }, "ExcludeTools"},
		{"initial_tags", func(l *Launch) { l.InitialTags = []string{"t"} }, "InitialTags"},
		{"max_iterations", func(l *Launch) { l.MaxIterations = 3 }, "MaxIterations"},
		{"max_output_tokens", func(l *Launch) { l.MaxOutputTokens = 256 }, "MaxOutputTokens"},
		{"run_timeout", func(l *Launch) { l.RunTimeout = time.Second }, "RunTimeout"},
		{"tool_timeout", func(l *Launch) { l.ToolTimeout = time.Second }, "ToolTimeout"},
		{"conversation_id", func(l *Launch) { l.ConversationID = "c" }, "ConversationID"},
		{"parent_id", func(l *Launch) { l.ParentID = "p" }, "ParentID"},
		{"system_prompt", func(l *Launch) { l.SystemPrompt = "s" }, "SystemPrompt"},
		{"fallback_content", func(l *Launch) { l.FallbackContent = "f" }, "FallbackContent"},
		{"skip_context", func(l *Launch) { l.SkipContext = true }, "SkipContext"},
		{"skip_tag_filter", func(l *Launch) { l.SkipTagFilter = true }, "SkipTagFilter"},
		{"suppress_always_context", func(l *Launch) { l.SuppressAlwaysContext = true }, "SuppressAlwaysContext"},
		{"prompt_mode", func(l *Launch) { l.PromptMode = agentctx.PromptModeTask }, "PromptMode"},
		{"usage_role", func(l *Launch) { l.UsageRole = "r" }, "UsageRole"},
		{"usage_task_name", func(l *Launch) { l.UsageTaskName = "n" }, "UsageTaskName"},
		{"completion_conversation_id", func(l *Launch) { l.CompletionConversationID = "c" }, "CompletionConversationID"},
		{"completion_channel", func(l *Launch) { l.CompletionChannel = &CompletionChannelTarget{Channel: "signal"} }, "CompletionChannel"},
		{"channel_binding", func(l *Launch) { l.ChannelBinding = &memory.ChannelBinding{Channel: "signal"} }, "ChannelBinding"},
		{"spec", func(l *Launch) { l.Spec = Spec{Name: "x"} }, "Spec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var l Launch
			tc.mut(&l)
			if !l.HasOverrides() {
				t.Fatalf("HasOverrides returned false after setting %s", tc.field)
			}
		})
	}

	// Forcing function: every caller-supplied field on Launch (anything
	// with a non-empty JSON tag, excluding Spec which the runtime
	// overwrites) must have a matching case above. Without this, adding
	// a new override field would silently bypass the active-service-loop
	// guard.
	t.Run("covers-all-fields", func(t *testing.T) {
		covered := make(map[string]bool, len(cases))
		for _, tc := range cases {
			covered[tc.field] = true
		}
		typ := reflect.TypeOf(Launch{})
		var missing []string
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			if !covered[field.Name] {
				missing = append(missing, field.Name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Fatalf("Launch fields without HasOverrides coverage: %v — add a case above and update Launch.HasOverrides", missing)
		}
	})
}
