package loop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
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

func effectiveOperation(op Operation) Operation {
	if op == "" {
		return OperationRequestReply
	}
	return op
}

// Spec is the loops-ng contract for describing a loop. It carries
// both the current engine-facing config fields and the forward-looking
// loops-ng semantics. Today it compiles to [Config], while [Profile]
// already shapes requests for loops created via [NewFromSpec].
// [Operation] and [Completion] are retained for the upcoming RunV2
// work.
type Spec struct {
	// Name is the unique identifier for the loop. Required.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Enabled marks the definition as eligible for runtime lifecycle
	// management. Service definitions only auto-start when enabled.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Task is the static prompt for each iteration. Ignored when
	// TaskBuilder or TurnBuilder is set.
	Task string `yaml:"task,omitempty" json:"task,omitempty"`

	// Profile shapes loop execution: routing hints, context-injection
	// tags, tool exclusions, and related request-shaping guidance.
	Profile router.LoopProfile `yaml:"profile,omitempty" json:"profile,omitempty"`

	// Operation describes the runtime pattern expected for the loop.
	Operation Operation `yaml:"operation,omitempty" json:"operation,omitempty"`

	// Completion describes how results should be delivered back to a
	// caller, conversation, or channel.
	Completion Completion `yaml:"completion,omitempty" json:"completion,omitempty"`

	// Outputs declare durable documents this loop is allowed to
	// maintain through scoped runtime tools.
	Outputs []OutputSpec `yaml:"outputs,omitempty" json:"outputs,omitempty"`

	// Conditions constrain when the definition is currently eligible to
	// run or launch. When empty, the definition is always eligible
	// unless blocked by policy.
	Conditions Conditions `yaml:"conditions,omitempty" json:"conditions,omitempty"`

	// Tags are the capability tags scoping this loop. They are
	// activated at iteration 0 — seeding the active-tag set used for
	// tool-registry scope filtering, KB article exposure, and any
	// other tag-driven context surface — and remain active across
	// iterations unless the model deactivates them. Per-invocation
	// runtime overrides layer on top via [Launch.InitialTags].
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// ExcludeTools lists tool names to exclude from the loop's
	// available tools.
	ExcludeTools []string `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`

	// SleepMin is the minimum sleep duration between iterations.
	SleepMin time.Duration `yaml:"sleep_min,omitempty" json:"sleep_min,omitempty"`
	// SleepMax is the maximum sleep duration between iterations.
	SleepMax time.Duration `yaml:"sleep_max,omitempty" json:"sleep_max,omitempty"`
	// SleepDefault is the initial sleep duration before the loop
	// self-adjusts.
	SleepDefault time.Duration `yaml:"sleep_default,omitempty" json:"sleep_default,omitempty"`
	// Jitter randomizes sleep durations to break periodicity.
	Jitter *float64 `yaml:"jitter,omitempty" json:"jitter,omitempty"`

	// MaxDuration is the maximum wall-clock time the loop may run.
	MaxDuration time.Duration `yaml:"max_duration,omitempty" json:"max_duration,omitempty"`
	// MaxIter is the maximum number of iteration attempts the loop
	// may make.
	MaxIter int `yaml:"max_iter,omitempty" json:"max_iter,omitempty"`

	// Supervisor enables frontier model dice rolls.
	Supervisor bool `yaml:"supervisor,omitempty" json:"supervisor,omitempty"`
	// SupervisorProb is the probability of using the supervisor model.
	SupervisorProb float64 `yaml:"supervisor_prob,omitempty" json:"supervisor_prob,omitempty"`
	// QualityFloor is the minimum model quality rating for normal
	// iterations.
	QualityFloor int `yaml:"quality_floor,omitempty" json:"quality_floor,omitempty"`
	// SupervisorContext is prepended during supervisor iterations.
	SupervisorContext string `yaml:"supervisor_context,omitempty" json:"supervisor_context,omitempty"`
	// SupervisorQualityFloor is the quality floor for supervisor
	// iterations.
	SupervisorQualityFloor int `yaml:"supervisor_quality_floor,omitempty" json:"supervisor_quality_floor,omitempty"`

	// OnRetrigger determines behavior when the loop is triggered again
	// while already running.
	OnRetrigger RetriggerMode `yaml:"on_retrigger,omitempty" json:"on_retrigger,omitempty"`

	// TaskBuilder generates a prompt per-iteration. The loop adapts
	// the prompt into the common TurnBuilder execution path so task
	// loops and custom turn builders share request preparation and
	// runner execution.
	TaskBuilder func(ctx context.Context, isSupervisor bool) (string, error) `yaml:"-" json:"-"`

	// TurnBuilder prepares an agent request per wake while leaving
	// execution in the loop runtime. It is runtime-only because it
	// captures Go dependencies and cannot be persisted.
	TurnBuilder TurnBuilder `yaml:"-" json:"-"`

	// PostIterate runs after each successful iteration.
	PostIterate func(ctx context.Context, result IterationResult) error `yaml:"-" json:"-"`

	// WaitFunc blocks until an external event arrives.
	WaitFunc func(ctx context.Context) (any, error) `yaml:"-" json:"-"`

	// Handler processes an iteration directly without an LLM call.
	Handler func(ctx context.Context, event any) error `yaml:"-" json:"-"`

	// Hints are merged into Request hints for each iteration.
	Hints map[string]string `yaml:"hints,omitempty" json:"hints,omitempty"`

	// FallbackContent is static text used when the loop completes a
	// request/reply run without any user-visible content. Interactive
	// loops can set this to guarantee a reply.
	FallbackContent string `yaml:"fallback_content,omitempty" json:"fallback_content,omitempty"`

	// Setup is called by the registry spawn helpers after [New] or
	// [NewFromSpec] but before [Loop.Start].
	Setup func(l *Loop) `yaml:"-" json:"-"`

	// RuntimeTools are request-scoped tools attached during hydration.
	RuntimeTools []RuntimeTool `yaml:"-" json:"-"`

	// OutputContextBuilder renders model-facing context for [Outputs].
	OutputContextBuilder OutputContextBuilder `yaml:"-" json:"-"`

	// Metadata holds arbitrary key/value pairs for the loop.
	Metadata map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`

	// ParentID is the parent loop ID, if any.
	ParentID string `yaml:"parent_id,omitempty" json:"parent_id,omitempty"`
}

// Validate checks that the loops-ng-facing fields and the current
// engine-facing configuration are internally consistent.
func (s *Spec) Validate() error {
	if s == nil {
		return fmt.Errorf("loop: spec is nil")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("loop: spec name is required")
	}
	if !validOperations[s.Operation] {
		return fmt.Errorf("loop: unsupported operation %q", s.Operation)
	}
	if !validCompletions[s.Completion] {
		return fmt.Errorf("loop: unsupported completion %q", s.Completion)
	}
	if err := s.Conditions.Validate(); err != nil {
		return fmt.Errorf("loop: conditions: %w", err)
	}
	if err := s.Profile.Validate(); err != nil {
		return fmt.Errorf("loop: profile: %w", err)
	}
	if err := validateOutputs(s.Outputs); err != nil {
		return fmt.Errorf("loop: %w", err)
	}
	cfg := s.ToConfig()
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	return nil
}

// ValidatePersistable checks that the spec is valid and safe to store in
// config or a persistent overlay. Persisted loop definitions are data, not
// code, so runtime-only hooks must remain nil.
func (s *Spec) ValidatePersistable() error {
	if err := s.Validate(); err != nil {
		return err
	}
	switch {
	case s.TaskBuilder != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set TaskBuilder", s.Name)
	case s.TurnBuilder != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set TurnBuilder", s.Name)
	case s.PostIterate != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set PostIterate", s.Name)
	case s.WaitFunc != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set WaitFunc", s.Name)
	case s.Handler != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set Handler", s.Name)
	case s.Setup != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set Setup", s.Name)
	case len(s.RuntimeTools) > 0:
		return fmt.Errorf("loop: persistable spec %q cannot set RuntimeTools", s.Name)
	case s.OutputContextBuilder != nil:
		return fmt.Errorf("loop: persistable spec %q cannot set OutputContextBuilder", s.Name)
	default:
		return nil
	}
}

// ToConfig compiles the current engine-facing portion of a Spec
// into today's [Config] shape. [Spec.Operation] and
// [Spec.Completion] already flow through into the runtime config,
// while [Spec.Profile] remains a request-shaping layer applied by
// [NewFromSpec] rather than a field on [Config].
func (s *Spec) ToConfig() Config {
	if s == nil {
		return Config{}
	}
	ns := s.normalized()
	return Config{
		Name:                   ns.Name,
		Task:                   ns.Task,
		Operation:              ns.Operation,
		Completion:             ns.Completion,
		Outputs:                cloneOutputs(ns.Outputs),
		Tags:                   append([]string(nil), ns.Tags...),
		ExcludeTools:           append([]string(nil), ns.ExcludeTools...),
		SleepMin:               ns.SleepMin,
		SleepMax:               ns.SleepMax,
		SleepDefault:           ns.SleepDefault,
		Jitter:                 cloneFloat64Ptr(ns.Jitter),
		MaxDuration:            ns.MaxDuration,
		MaxIter:                ns.MaxIter,
		Supervisor:             ns.Supervisor,
		SupervisorProb:         ns.SupervisorProb,
		QualityFloor:           ns.QualityFloor,
		SupervisorContext:      ns.SupervisorContext,
		SupervisorQualityFloor: ns.SupervisorQualityFloor,
		OnRetrigger:            ns.OnRetrigger,
		TaskBuilder:            ns.TaskBuilder,
		TurnBuilder:            ns.TurnBuilder,
		PostIterate:            ns.PostIterate,
		WaitFunc:               ns.WaitFunc,
		Handler:                ns.Handler,
		Hints:                  cloneStringMap(ns.Hints),
		FallbackContent:        ns.FallbackContent,
		Setup:                  ns.Setup,
		RuntimeTools:           cloneRuntimeTools(ns.RuntimeTools),
		OutputContextBuilder:   ns.OutputContextBuilder,
		Metadata:               cloneStringMap(ns.Metadata),
		ParentID:               ns.ParentID,
	}
}

// EffectiveConfig returns the engine-facing configuration for this spec
// with loop runtime defaults applied. This is useful for inspection,
// linting, and warning surfaces that need to explain what a partially
// specified definition will actually do at runtime.
func (s *Spec) EffectiveConfig() Config {
	cfg := s.ToConfig()
	cfg.applyDefaults()
	return cfg
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
		InitialTags:  append([]string(nil), s.Tags...),
		RuntimeTools: cloneRuntimeTools(s.RuntimeTools),
	}
}

func (s *Spec) normalized() Spec {
	if s == nil {
		return Spec{}
	}
	ns := *s

	switch effectiveOperation(ns.Operation) {
	case OperationRequestReply, OperationBackgroundTask:
		if ns.MaxIter == 0 {
			ns.MaxIter = 1
		}
		if ns.SleepMin == 0 {
			ns.SleepMin = time.Millisecond
		}
		if ns.SleepMax == 0 {
			ns.SleepMax = time.Millisecond
		}
		if ns.SleepDefault == 0 {
			ns.SleepDefault = time.Millisecond
		}
		if ns.Jitter == nil {
			ns.Jitter = Float64Ptr(0)
		}
	}

	return ns
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
