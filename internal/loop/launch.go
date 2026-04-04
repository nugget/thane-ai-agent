package loop

import (
	"fmt"
	"strings"
	"time"
)

// Launch describes a single loops-ng launch request. It is separate
// from [Spec] so per-launch overrides and delivery hooks can grow here
// over time without turning [Spec] itself into an ephemeral run object.
type Launch struct {
	Spec                     Spec                                   `yaml:"spec,omitempty" json:"spec,omitempty"`
	Task                     string                                 `yaml:"task,omitempty" json:"task,omitempty"`
	ParentID                 string                                 `yaml:"parent_id,omitempty" json:"parent_id,omitempty"`
	Metadata                 map[string]string                      `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	ConversationID           string                                 `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	Model                    string                                 `yaml:"model,omitempty" json:"model,omitempty"`
	Hints                    map[string]string                      `yaml:"hints,omitempty" json:"hints,omitempty"`
	AllowedTools             []string                               `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	ExcludeTools             []string                               `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`
	InitialTags              []string                               `yaml:"initial_tags,omitempty" json:"initial_tags,omitempty"`
	OnProgress               func(kind string, data map[string]any) `yaml:"-" json:"-"`
	RunTimeout               time.Duration                          `yaml:"run_timeout,omitempty" json:"run_timeout,omitempty"`
	CompletionConversationID string                                 `yaml:"completion_conversation_id,omitempty" json:"completion_conversation_id,omitempty"`
	SkipContext              bool                                   `yaml:"skip_context,omitempty" json:"skip_context,omitempty"`
	SkipTagFilter            bool                                   `yaml:"skip_tag_filter,omitempty" json:"skip_tag_filter,omitempty"`
	SystemPrompt             string                                 `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	MaxIterations            int                                    `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
	MaxOutputTokens          int                                    `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	ToolTimeout              time.Duration                          `yaml:"tool_timeout,omitempty" json:"tool_timeout,omitempty"`
	UsageRole                string                                 `yaml:"usage_role,omitempty" json:"usage_role,omitempty"`
	UsageTaskName            string                                 `yaml:"usage_task_name,omitempty" json:"usage_task_name,omitempty"`
}

// Validate checks that the launch is well-formed.
func (l *Launch) Validate() error {
	if l == nil {
		return fmt.Errorf("loop: launch is nil")
	}
	if l.RunTimeout < 0 {
		return fmt.Errorf("loop: run timeout must be >= 0")
	}
	if l.Spec.Completion == CompletionConversation && strings.TrimSpace(l.CompletionConversationID) == "" {
		return fmt.Errorf("loop: completion conversation ID is required for conversation completion")
	}
	spec := l.Spec
	if l.Task != "" && spec.Task == "" && spec.TaskBuilder == nil && spec.Handler == nil {
		spec.Task = l.Task
	}
	return spec.Validate()
}

func (l *Launch) requestOverride() Request {
	if l == nil {
		return Request{}
	}
	return Request{
		Model:           l.Model,
		ConversationID:  l.ConversationID,
		SkipContext:     l.SkipContext,
		AllowedTools:    append([]string(nil), l.AllowedTools...),
		ExcludeTools:    append([]string(nil), l.ExcludeTools...),
		SkipTagFilter:   l.SkipTagFilter,
		Hints:           cloneStringMap(l.Hints),
		InitialTags:     append([]string(nil), l.InitialTags...),
		OnProgress:      l.OnProgress,
		MaxIterations:   l.MaxIterations,
		MaxOutputTokens: l.MaxOutputTokens,
		ToolTimeout:     l.ToolTimeout,
		UsageRole:       l.UsageRole,
		UsageTaskName:   l.UsageTaskName,
		SystemPrompt:    l.SystemPrompt,
	}
}

// LaunchResult is the outcome of starting a loop via [Registry.Launch].
// Request/reply launches wait for completion and return a final status;
// detached launches return immediately with the new loop ID.
type LaunchResult struct {
	LoopID      string    `yaml:"loop_id,omitempty" json:"loop_id"`
	Operation   Operation `yaml:"operation,omitempty" json:"operation"`
	Detached    bool      `yaml:"detached,omitempty" json:"detached"`
	Response    *Response `yaml:"response,omitempty" json:"response,omitempty"`
	FinalStatus *Status   `yaml:"final_status,omitempty" json:"final_status,omitempty"`
}
