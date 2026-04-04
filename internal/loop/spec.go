package loop

import (
	"context"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

// Operation describes the runtime pattern a loop is expected to
// follow. The zero value is accepted while loops-ng adoption is
// incremental.
type Operation string

const (
	// OperationRequestReply is a one-shot run that is expected to
	// conclude with a direct result for the caller.
	OperationRequestReply Operation = "request_reply"
	// OperationBackgroundTask is a detached task whose result is
	// delivered later through a non-blocking completion path.
	OperationBackgroundTask Operation = "background_task"
	// OperationService is a persistent loop such as metacognition,
	// ego, or a long-running watcher.
	OperationService Operation = "service"
)

// Completion describes how a loop's result should be delivered.
// The zero value is accepted while loops-ng adoption is incremental.
type Completion string

const (
	// CompletionReturn delivers the result directly to the caller.
	CompletionReturn Completion = "return"
	// CompletionConversation injects the result into a conversation.
	CompletionConversation Completion = "conversation"
	// CompletionChannel delivers the result to a channel integration.
	CompletionChannel Completion = "channel"
	// CompletionNone means the loop has no outward completion delivery.
	CompletionNone Completion = "none"
)

var validOperations = map[Operation]bool{
	"":                      true,
	OperationRequestReply:   true,
	OperationBackgroundTask: true,
	OperationService:        true,
}

var validCompletions = map[Completion]bool{
	"":                     true,
	CompletionReturn:       true,
	CompletionConversation: true,
	CompletionChannel:      true,
	CompletionNone:         true,
}

// Spec is the loops-ng contract for describing a loop. It carries
// both the current engine-facing config fields and the forward-looking
// loops-ng semantics. Today it compiles to [Config], while [Profile]
// already shapes requests for loops created via [NewFromSpec].
// [Operation] and [Completion] are retained for the upcoming RunV2
// work.
type Spec struct {
	// Name is the unique identifier for the loop. Required.
	Name string

	// Task is the static prompt for each iteration. Ignored when
	// TaskBuilder is set.
	Task string

	// Profile shapes loop execution: routing hints, context-injection
	// tags, tool exclusions, and related request-shaping guidance.
	Profile router.LoopProfile

	// Operation describes the runtime pattern expected for the loop.
	Operation Operation

	// Completion describes how results should be delivered back to a
	// caller, conversation, or channel.
	Completion Completion

	// Tags are capability tags for tool scoping. When non-empty,
	// the loop's tool registry is filtered to tools matching these
	// tags (plus always-active tags).
	Tags []string

	// ExcludeTools lists tool names to exclude from the loop's
	// available tools.
	ExcludeTools []string

	// SleepMin is the minimum sleep duration between iterations.
	SleepMin time.Duration
	// SleepMax is the maximum sleep duration between iterations.
	SleepMax time.Duration
	// SleepDefault is the initial sleep duration before the loop
	// self-adjusts.
	SleepDefault time.Duration
	// Jitter randomizes sleep durations to break periodicity.
	Jitter *float64

	// MaxDuration is the maximum wall-clock time the loop may run.
	MaxDuration time.Duration
	// MaxIter is the maximum number of iteration attempts the loop
	// may make.
	MaxIter int

	// Supervisor enables frontier model dice rolls.
	Supervisor bool
	// SupervisorProb is the probability of using the supervisor model.
	SupervisorProb float64
	// QualityFloor is the minimum model quality rating for normal
	// iterations.
	QualityFloor int
	// SupervisorContext is prepended during supervisor iterations.
	SupervisorContext string
	// SupervisorQualityFloor is the quality floor for supervisor
	// iterations.
	SupervisorQualityFloor int

	// OnRetrigger determines behavior when the loop is triggered again
	// while already running.
	OnRetrigger RetriggerMode

	// TaskBuilder generates a prompt per-iteration.
	TaskBuilder func(ctx context.Context, isSupervisor bool) (string, error) `json:"-"`

	// PostIterate runs after each successful iteration.
	PostIterate func(ctx context.Context, result IterationResult) error `json:"-"`

	// WaitFunc blocks until an external event arrives.
	WaitFunc func(ctx context.Context) (any, error) `json:"-"`

	// Handler processes an iteration directly without an LLM call.
	Handler func(ctx context.Context, event any) error `json:"-"`

	// Hints are merged into Request hints for each iteration.
	Hints map[string]string

	// Setup is called by the registry spawn helpers after [New] or
	// [NewFromSpec] but before [Loop.Start].
	Setup func(l *Loop) `json:"-"`

	// Metadata holds arbitrary key/value pairs for the loop.
	Metadata map[string]string

	// ParentID is the parent loop ID, if any.
	ParentID string
}

// Validate checks that the loops-ng-facing fields and the current
// engine-facing configuration are internally consistent.
func (s *Spec) Validate() error {
	if s == nil {
		return fmt.Errorf("loop: spec is nil")
	}
	if !validOperations[s.Operation] {
		return fmt.Errorf("loop: unsupported operation %q", s.Operation)
	}
	if !validCompletions[s.Completion] {
		return fmt.Errorf("loop: unsupported completion %q", s.Completion)
	}
	if err := s.Profile.Validate(); err != nil {
		return fmt.Errorf("loop: profile: %w", err)
	}
	cfg := s.ToConfig()
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	return nil
}

// ToConfig compiles the current engine-facing portion of a Spec
// into today's [Config] shape. This is intentionally conservative:
// loops-ng-specific fields such as [Spec.Operation] and
// [Spec.Completion] are retained on the spec for future RunV2
// wiring rather than forced into today's engine. [Spec.Profile] is
// applied by [NewFromSpec] as per-iteration request shaping.
func (s *Spec) ToConfig() Config {
	if s == nil {
		return Config{}
	}
	return Config{
		Name:                   s.Name,
		Task:                   s.Task,
		Tags:                   append([]string(nil), s.Tags...),
		ExcludeTools:           append([]string(nil), s.ExcludeTools...),
		SleepMin:               s.SleepMin,
		SleepMax:               s.SleepMax,
		SleepDefault:           s.SleepDefault,
		Jitter:                 cloneFloat64Ptr(s.Jitter),
		MaxDuration:            s.MaxDuration,
		MaxIter:                s.MaxIter,
		Supervisor:             s.Supervisor,
		SupervisorProb:         s.SupervisorProb,
		QualityFloor:           s.QualityFloor,
		SupervisorContext:      s.SupervisorContext,
		SupervisorQualityFloor: s.SupervisorQualityFloor,
		OnRetrigger:            s.OnRetrigger,
		TaskBuilder:            s.TaskBuilder,
		PostIterate:            s.PostIterate,
		WaitFunc:               s.WaitFunc,
		Handler:                s.Handler,
		Hints:                  cloneStringMap(s.Hints),
		Setup:                  s.Setup,
		Metadata:               cloneStringMap(s.Metadata),
		ParentID:               s.ParentID,
	}
}

func (s *Spec) profileRequest() Request {
	if s == nil {
		return Request{}
	}
	opts := s.Profile.RequestOptions()
	return Request{
		Model:        opts.Model,
		Hints:        opts.Hints,
		ExcludeTools: opts.ExcludeTools,
		SeedTags:     opts.SeedTags,
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneFloat64Ptr(src *float64) *float64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
