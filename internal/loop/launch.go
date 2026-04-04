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
	Spec                     Spec
	Task                     string
	ParentID                 string
	Metadata                 map[string]string
	ConversationID           string
	Model                    string
	Hints                    map[string]string
	AllowedTools             []string
	ExcludeTools             []string
	InitialTags              []string
	OnProgress               func(kind string, data map[string]any) `json:"-"`
	RunTimeout               time.Duration
	CompletionConversationID string
	SkipContext              bool
	SkipTagFilter            bool
	SystemPrompt             string
	MaxIterations            int
	MaxOutputTokens          int
	ToolTimeout              time.Duration
	UsageRole                string
	UsageTaskName            string
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
	LoopID      string    `json:"loop_id"`
	Operation   Operation `json:"operation"`
	Detached    bool      `json:"detached"`
	Response    *Response `json:"response,omitempty"`
	FinalStatus *Status   `json:"final_status,omitempty"`
}
