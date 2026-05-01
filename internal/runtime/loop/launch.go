package loop

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// Launch describes a single loops-ng launch request. It is separate
// from [Spec] so per-launch overrides and delivery hooks can grow here
// over time without turning [Spec] itself into an ephemeral run object.
type Launch struct {
	Spec           Spec                                   `yaml:"spec,omitempty" json:"spec,omitempty"`
	Task           string                                 `yaml:"task,omitempty" json:"task,omitempty"`
	ParentID       string                                 `yaml:"parent_id,omitempty" json:"parent_id,omitempty"`
	Metadata       map[string]string                      `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	ConversationID string                                 `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	ChannelBinding *memory.ChannelBinding                 `yaml:"channel_binding,omitempty" json:"channel_binding,omitempty"`
	Model          string                                 `yaml:"model,omitempty" json:"model,omitempty"`
	Hints          map[string]string                      `yaml:"hints,omitempty" json:"hints,omitempty"`
	AllowedTools   []string                               `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	ExcludeTools   []string                               `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`
	InitialTags    []string                               `yaml:"initial_tags,omitempty" json:"initial_tags,omitempty"`
	OnProgress     func(kind string, data map[string]any) `yaml:"-" json:"-"`
	RunTimeout     time.Duration                          `yaml:"run_timeout,omitempty" json:"run_timeout,omitempty"`
	// CompletionConversationID names the live conversation that should
	// receive detached completion delivery when Spec.Completion is
	// CompletionConversation.
	CompletionConversationID string `yaml:"completion_conversation_id,omitempty" json:"completion_conversation_id,omitempty"`
	// CompletionChannel identifies the interactive channel target that
	// should receive detached completion delivery when Spec.Completion
	// is CompletionChannel.
	CompletionChannel *CompletionChannelTarget `yaml:"completion_channel,omitempty" json:"completion_channel,omitempty"`
	SkipContext       bool                     `yaml:"skip_context,omitempty" json:"skip_context,omitempty"`
	SkipTagFilter     bool                     `yaml:"skip_tag_filter,omitempty" json:"skip_tag_filter,omitempty"`
	SystemPrompt      string                   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	FallbackContent   string                   `yaml:"fallback_content,omitempty" json:"fallback_content,omitempty"`
	PromptMode        agentctx.PromptMode      `yaml:"prompt_mode,omitempty" json:"prompt_mode,omitempty"`
	MaxIterations     int                      `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
	MaxOutputTokens   int                      `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	ToolTimeout       time.Duration            `yaml:"tool_timeout,omitempty" json:"tool_timeout,omitempty"`
	UsageRole         string                   `yaml:"usage_role,omitempty" json:"usage_role,omitempty"`
	UsageTaskName     string                   `yaml:"usage_task_name,omitempty" json:"usage_task_name,omitempty"`

	// SuppressAlwaysContext drops the always-on context bucket from
	// the system prompt assembler for this run. Default false matches
	// main-loop behavior (include presence, episodic memory, working
	// memory, notification history, etc.). Delegates set true so the
	// child agent sees only tag-scoped context appropriate to the
	// bounded task.
	SuppressAlwaysContext bool `yaml:"suppress_always_context,omitempty" json:"suppress_always_context,omitempty"`
}

type launchJSON struct {
	Spec                     Spec                     `json:"spec,omitempty"`
	Task                     string                   `json:"task,omitempty"`
	ParentID                 string                   `json:"parent_id,omitempty"`
	Metadata                 map[string]string        `json:"metadata,omitempty"`
	ConversationID           string                   `json:"conversation_id,omitempty"`
	ChannelBinding           *memory.ChannelBinding   `json:"channel_binding,omitempty"`
	Model                    string                   `json:"model,omitempty"`
	Hints                    map[string]string        `json:"hints,omitempty"`
	AllowedTools             []string                 `json:"allowed_tools,omitempty"`
	ExcludeTools             []string                 `json:"exclude_tools,omitempty"`
	InitialTags              []string                 `json:"initial_tags,omitempty"`
	RunTimeout               string                   `json:"run_timeout,omitempty"`
	CompletionConversationID string                   `json:"completion_conversation_id,omitempty"`
	CompletionChannel        *CompletionChannelTarget `json:"completion_channel,omitempty"`
	SkipContext              bool                     `json:"skip_context,omitempty"`
	SkipTagFilter            bool                     `json:"skip_tag_filter,omitempty"`
	SystemPrompt             string                   `json:"system_prompt,omitempty"`
	FallbackContent          string                   `json:"fallback_content,omitempty"`
	PromptMode               agentctx.PromptMode      `json:"prompt_mode,omitempty"`
	MaxIterations            int                      `json:"max_iterations,omitempty"`
	MaxOutputTokens          int                      `json:"max_output_tokens,omitempty"`
	ToolTimeout              string                   `json:"tool_timeout,omitempty"`
	UsageRole                string                   `json:"usage_role,omitempty"`
	UsageTaskName            string                   `json:"usage_task_name,omitempty"`
	SuppressAlwaysContext    bool                     `json:"suppress_always_context,omitempty"`
}

func (l Launch) MarshalJSON() ([]byte, error) {
	wire := launchJSON{
		Spec:                     l.Spec,
		Task:                     l.Task,
		ParentID:                 l.ParentID,
		Metadata:                 cloneStringMap(l.Metadata),
		ConversationID:           l.ConversationID,
		ChannelBinding:           l.ChannelBinding.Clone(),
		Model:                    l.Model,
		Hints:                    cloneStringMap(l.Hints),
		AllowedTools:             append([]string(nil), l.AllowedTools...),
		ExcludeTools:             append([]string(nil), l.ExcludeTools...),
		InitialTags:              append([]string(nil), l.InitialTags...),
		RunTimeout:               durationString(l.RunTimeout),
		CompletionConversationID: l.CompletionConversationID,
		CompletionChannel:        CloneCompletionChannelTarget(l.CompletionChannel),
		SkipContext:              l.SkipContext,
		SkipTagFilter:            l.SkipTagFilter,
		SystemPrompt:             l.SystemPrompt,
		FallbackContent:          l.FallbackContent,
		PromptMode:               l.PromptMode,
		MaxIterations:            l.MaxIterations,
		MaxOutputTokens:          l.MaxOutputTokens,
		ToolTimeout:              durationString(l.ToolTimeout),
		UsageRole:                l.UsageRole,
		UsageTaskName:            l.UsageTaskName,
		SuppressAlwaysContext:    l.SuppressAlwaysContext,
	}
	return json.Marshal(wire)
}

func (l *Launch) UnmarshalJSON(data []byte) error {
	if l == nil {
		return fmt.Errorf("loop: nil launch")
	}
	var wire launchJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	runTimeout, err := parseOptionalDuration(wire.RunTimeout)
	if err != nil {
		return fmt.Errorf("loop: run_timeout: %w", err)
	}
	toolTimeout, err := parseOptionalDuration(wire.ToolTimeout)
	if err != nil {
		return fmt.Errorf("loop: tool_timeout: %w", err)
	}
	*l = Launch{
		Spec:                     wire.Spec,
		Task:                     wire.Task,
		ParentID:                 wire.ParentID,
		Metadata:                 cloneStringMap(wire.Metadata),
		ConversationID:           wire.ConversationID,
		ChannelBinding:           wire.ChannelBinding.Clone(),
		Model:                    wire.Model,
		Hints:                    cloneStringMap(wire.Hints),
		AllowedTools:             append([]string(nil), wire.AllowedTools...),
		ExcludeTools:             append([]string(nil), wire.ExcludeTools...),
		InitialTags:              append([]string(nil), wire.InitialTags...),
		RunTimeout:               runTimeout,
		CompletionConversationID: wire.CompletionConversationID,
		CompletionChannel:        CloneCompletionChannelTarget(wire.CompletionChannel),
		SkipContext:              wire.SkipContext,
		SkipTagFilter:            wire.SkipTagFilter,
		SystemPrompt:             wire.SystemPrompt,
		FallbackContent:          wire.FallbackContent,
		PromptMode:               wire.PromptMode,
		MaxIterations:            wire.MaxIterations,
		MaxOutputTokens:          wire.MaxOutputTokens,
		ToolTimeout:              toolTimeout,
		UsageRole:                wire.UsageRole,
		UsageTaskName:            wire.UsageTaskName,
		SuppressAlwaysContext:    wire.SuppressAlwaysContext,
	}
	return nil
}

// Validate checks that the launch is well-formed.
func (l *Launch) Validate() error {
	if l == nil {
		return fmt.Errorf("loop: launch is nil")
	}
	if l.RunTimeout < 0 {
		return fmt.Errorf("loop: run timeout must be >= 0")
	}
	if !l.PromptMode.Valid() {
		return fmt.Errorf("loop: invalid prompt_mode %q", l.PromptMode)
	}
	if l.Spec.Completion == CompletionConversation && strings.TrimSpace(l.CompletionConversationID) == "" {
		return fmt.Errorf("loop: completion conversation ID is required for conversation completion")
	}
	if l.Spec.Completion == CompletionChannel {
		if err := l.CompletionChannel.Validate(); err != nil {
			return err
		}
	}
	spec := l.Spec
	if l.Task != "" && spec.Task == "" && spec.TaskBuilder == nil && spec.TurnBuilder == nil && spec.Handler == nil {
		spec.Task = l.Task
	}
	return spec.Validate()
}

func (l *Launch) requestOverride() Request {
	if l == nil {
		return Request{}
	}
	return Request{
		Model:                 l.Model,
		ConversationID:        l.ConversationID,
		ChannelBinding:        l.ChannelBinding.Clone(),
		SkipContext:           l.SkipContext,
		AllowedTools:          append([]string(nil), l.AllowedTools...),
		ExcludeTools:          append([]string(nil), l.ExcludeTools...),
		SkipTagFilter:         l.SkipTagFilter,
		Hints:                 cloneStringMap(l.Hints),
		InitialTags:           append([]string(nil), l.InitialTags...),
		OnProgress:            l.OnProgress,
		FallbackContent:       l.FallbackContent,
		MaxIterations:         l.MaxIterations,
		MaxOutputTokens:       l.MaxOutputTokens,
		ToolTimeout:           l.ToolTimeout,
		UsageRole:             l.UsageRole,
		UsageTaskName:         l.UsageTaskName,
		SystemPrompt:          l.SystemPrompt,
		PromptMode:            l.PromptMode,
		SuppressAlwaysContext: l.SuppressAlwaysContext,
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
