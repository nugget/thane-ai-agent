package loop

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// Operation describes the runtime pattern a loop is expected to
// follow. The zero value is accepted and defaults to
// [OperationRequestReply].
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
	// OperationContainer is a non-executing node in the loop graph
	// used to group related loops and hold inheritable state (tags,
	// entity subscriptions) that descendants pick up at iteration
	// time. Container loops occupy registry entries, take a
	// parent_id, carry metadata, but never wake and never run a Task.
	// See [Spec.Validate] for the shape contract.
	//
	// The container with the well-known name [CoreLoopName] is the
	// graph's structural root — pid-0 equivalent. Core is not a
	// separate operation kind; it's a container with a few extra
	// invariants enforced by the registry and bootstrap (singleton,
	// auto-created on startup, refused for delete, default parent
	// for orphan loops). See [Loop.IsCore].
	OperationContainer Operation = "container"
	// OperationEventDriven is a persistent loop that wakes only on
	// external triggers — inter-loop notifications (the message bus
	// + pendingNotifies machinery) or a runtime [Config.WaitFunc]
	// channel. Unlike [OperationService], it carries no periodic
	// sleep envelope; it sits quiescent until something asks it to
	// run.
	//
	// Use it for trigger handlers: a loop that does nothing until
	// an MQTT message / feed entry / event-source envelope arrives,
	// then handles the event, then returns to quiescent. Validation
	// rejects [Spec.SleepMin] / [Spec.SleepMax] / [Spec.SleepDefault]
	// on this operation — those fields only make sense for
	// timer-driven service loops.
	//
	// Otherwise event-driven loops participate fully in the
	// Spec/cascade/SupervisorProfile system. Profile, Tags,
	// Subscriptions, ParentName, Conditions all apply. Per-iteration
	// behavior is identical to service loops: each wake gets a
	// fresh conversation ID and runs one iteration of the Task /
	// TaskBuilder / TurnBuilder.
	//
	// The runtime substrate already exists — [Config.WaitFunc] +
	// [Config.Handler] service loops (ha-state-watcher, etc.) have
	// done this shape for ages. OperationEventDriven exposes it
	// declaratively so persisted overlay specs and YAML-defined
	// loops can opt into the "no timer, wake only on notification"
	// shape without needing runtime hooks.
	OperationEventDriven Operation = "event_driven"
)

// CoreLoopName is the well-known name reserved for the singleton
// structural root container. Auto-created at startup if absent;
// orphan loops default-parent to it; cannot be stopped via the
// operator-facing kill switch. Operators and tools that want to
// distinguish the root from other containers compare against this
// constant rather than re-spelling the string.
const CoreLoopName = "core"

// ChannelsContainerName is the well-known name of the built-in grouping
// container for interactive counterparty channel loops (signal, owu, …).
// Spawned eagerly under core at startup so dynamically-created channel loops
// can attach to it. Shared here so the app bootstrap and the channel
// integrations reference one constant instead of re-spelling the string.
const ChannelsContainerName = "channels"

// Completion describes how a loop's result should be delivered.
// The zero value is accepted and means "no outward delivery declared".
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
	OperationContainer:      true,
	OperationEventDriven:    true,
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

// Spec is the contract for describing a loop. It compiles to
// [Config] for the runtime, and [Profile] shapes requests for loops
// created via [NewFromSpec].
type Spec struct {
	// Name is the unique identifier for the loop. Required.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Enabled marks the definition as eligible for runtime lifecycle
	// management. Service definitions only auto-start when enabled.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Task is the static prompt for each iteration. Ignored when
	// TaskBuilder or TurnBuilder is set.
	Task string `yaml:"task,omitempty" json:"task,omitempty"`

	// Intent is a short (one- or two-sentence) statement of why this loop
	// exists — its purpose, distinct from Task (the per-iteration prompt).
	// First-class as of #1106 (promoted out of the metadata["intent"] bag): the
	// runtime prefers this field and falls back to metadata["intent"] for one
	// release (see [Spec.ToConfig]). Surfaced verbatim as LoopView.Intent.
	Intent string `yaml:"intent,omitempty" json:"intent,omitempty"`

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

	// Subscriptions are entities this loop wants to see in context
	// every iteration. Each iteration's effective subscription list
	// is the union of this loop's Subscriptions and every container
	// ancestor's (see [Registry.AncestorSubscriptions]).
	//
	// This is the structural successor to the scope_tag-and-watchlist-
	// row binding the codebase used before the container loops rollout.
	// The scope tag is gone; the parent_id graph is the binding.
	Subscriptions []EntitySubscription `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`

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

	// Supervisor enables periodic supervisor turns: a Bernoulli trial
	// at each wake decides whether this iteration uses the more
	// capable model and the SupervisorProfile overrides defined below.
	Supervisor bool `yaml:"supervisor,omitempty" json:"supervisor,omitempty"`
	// SupervisorProb is the per-wake probability [0.0, 1.0] of a
	// supervisor turn when Supervisor is true.
	SupervisorProb float64 `yaml:"supervisor_prob,omitempty" json:"supervisor_prob,omitempty"`
	// SupervisorProfile carries the per-turn-mode overrides applied
	// during supervisor turns. It is an OVERLAY on Profile: any field
	// set here wins, any field left empty falls back to Profile.
	// Notably, SupervisorProfile.QualityFloor lets a loop demand a
	// higher rating during review turns, and
	// SupervisorProfile.Instructions replaces the (now-retired)
	// SupervisorContext as the prompt-prefix path. Like
	// [Profile.Instructions], SupervisorProfile.Instructions is
	// self-only — it does not cascade through container ancestors.
	//
	// Nil means "no SupervisorProfile overrides declared." Two
	// hardcoded baseline behaviors still apply to every supervisor
	// turn even with a nil SupervisorProfile, because they're the
	// reason the supervisor mechanism exists at all:
	//
	//   - the `supervisor` routing factor is stamped to "true" so
	//     the router can see this is a review turn;
	//   - the `local_only` routing factor is forced to "false" so a
	//     supervisor turn doesn't end up running on a cheap local
	//     model (which would defeat the purpose of supervising).
	//
	// A non-nil SupervisorProfile can override either (e.g. set
	// `local_only: "true"` to explicitly keep supervisor turns
	// local), but the *defaults* are not "same as Profile" — they're
	// "Profile plus those two flips." See [prepareAgentTurnRequest]
	// for the merge order.
	SupervisorProfile *router.LoopProfile `yaml:"supervisor_profile,omitempty" json:"supervisor_profile,omitempty"`

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

	// RoutingFactors are merged into each iteration's Request routing
	// factors.
	RoutingFactors map[string]string `yaml:"routing_factors,omitempty" json:"routing_factors,omitempty"`

	// DelegationGating sets the typed feature switch on each
	// iteration's Request. "disabled" gives the model direct tool
	// access. Most loops leave this empty (default gating) and rely on
	// Profile.DelegationGating for spec-level configuration.
	DelegationGating string `yaml:"delegation_gating,omitempty" json:"delegation_gating,omitempty"`

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

	// ParentID is the parent loop ID, if any. Set on the runtime spec at
	// launch time (live loop IDs change per launch, so this field is not
	// the durable parent reference). Persisted specs leave it empty.
	ParentID string `yaml:"parent_id,omitempty" json:"parent_id,omitempty"`

	// ParentName is the durable name of the parent loop. Survives
	// restart because names — unlike loop IDs — are stable across
	// hydration cycles. Hydration resolves [ParentName] to the live
	// parent's loop ID before launching, so descendants land under the
	// correct registry node and pick up tag inheritance immediately.
	// Today only container parents are honored (children of services
	// have no inheritance semantics).
	ParentName string `yaml:"parent_name,omitempty" json:"parent_name,omitempty"`

	// Origin is the creation provenance of this definition: pointers (plain
	// foreign keys) back to the request, conversation, and loop that authored
	// it, plus when. Runtime-stamped at the durable commit chokepoint (#1106
	// C2) and authoritative — a model-supplied origin is overwritten, and the
	// original creation provenance is preserved across later updates/replaces.
	// Resolved on demand against the request/conversation stores; no turn
	// snapshot is stored here. Nil for definitions authored outside a runtime
	// authoring context (e.g. config-sourced loops).
	//
	// `yaml:"-"`: origin is not config-authorable — it is runtime-stamped and
	// would otherwise surface in the generated config example as an invalid
	// zero-time placeholder. It still persists in the dynamic overlay, which is
	// JSON (see loopDefinitionStore), routed through the custom specJSON wire.
	Origin *OriginInfo `yaml:"-" json:"origin,omitempty"`
}

// OriginInfo records who/what/when created a loop definition — pointer-only
// provenance (#1106 C2). The ids are foreign keys resolved on demand against
// the primary-source stores (request store, conversation store), not copies of
// any content. JSON-only: origin is never YAML-serialized (its Spec field is
// `yaml:"-"`), and the overlay that persists it is JSON.
type OriginInfo struct {
	// RequestID is the model request (r_…) whose turn authored the definition.
	RequestID string `json:"request_id,omitempty"`
	// ConversationID is the conversation that turn belonged to.
	ConversationID string `json:"conversation_id,omitempty"`
	// CreatedByLoopID is the live loop ID that ran the authoring turn.
	CreatedByLoopID string `json:"created_by_loop_id,omitempty"`
	// CreatedAt is when the definition was first committed.
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Clone returns a copy of the origin (or nil). OriginInfo holds only value
// types, so a shallow copy is a deep copy; the method exists to make the
// preserve-across-update intent explicit at call sites.
func (o *OriginInfo) Clone() *OriginInfo {
	if o == nil {
		return nil
	}
	cp := *o
	return &cp
}

// IsZero reports whether the spec is the zero value (no fields set).
// Used by guards that need to detect "did the caller send a spec at
// all" without enumerating every field. Uses [reflect.DeepEqual]
// against the zero value so new fields are covered automatically.
func (s Spec) IsZero() bool {
	return reflect.DeepEqual(s, Spec{})
}

// Validate checks that the spec fields and derived engine-facing
// configuration are internally consistent.
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
	// The name "core" is reserved for the singleton structural root.
	// Anything else with that name would shadow [Registry.Core]'s
	// lookup and produce a confusing graph. Containers may use it
	// (they ARE the core when so named); services / request-reply /
	// background-task definitions may not. A non-empty ParentName on
	// the core also makes no sense — the core sits above the tree.
	if s.Name == CoreLoopName {
		if s.Operation != OperationContainer {
			return fmt.Errorf("loop: name %q is reserved for the singleton root container; refuse operation=%q", CoreLoopName, s.Operation)
		}
		if strings.TrimSpace(s.ParentName) != "" || strings.TrimSpace(s.ParentID) != "" {
			return fmt.Errorf("loop: core container %q cannot declare a parent — it is the structural root by definition", CoreLoopName)
		}
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
	if s.Operation == OperationContainer {
		// Containers are inert nodes — they hold inheritable state
		// (tags, entity subscriptions, metadata) but never wake and
		// never execute. Reject any field that would imply
		// execution; the validation here catches authoring mistakes
		// before the runtime has to refuse the spec at start time.
		return validateContainerShape(s)
	}
	if s.Operation == OperationEventDriven {
		if err := validateEventDrivenShape(s); err != nil {
			return err
		}
	}
	cfg := s.ToConfig()
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	return nil
}

// validateEventDrivenShape rejects timer-shaped fields on
// event-driven specs. These loops have no periodic wake — the only
// reasons they iterate are notification arrivals and explicit
// runtime [Config.WaitFunc] channel reads. A sleep envelope would
// just be dead config; catching it at spec-validation time keeps
// authoring mistakes loud rather than silently ignored at start.
//
// What IS legal on an event-driven spec: Task / TaskBuilder /
// TurnBuilder / Handler, all of which still describe what to do on
// each wake. Profile, Tags, Subscriptions, ParentName, Conditions,
// Outputs, Supervisor + SupervisorProfile — all participate
// normally. The only constraint is "no periodic timer."
func validateEventDrivenShape(s *Spec) error {
	if s.SleepMin != 0 || s.SleepMax != 0 || s.SleepDefault != 0 {
		return fmt.Errorf("loop: event-driven %q cannot set sleep envelope (event-driven loops have no periodic timer; remove sleep_min / sleep_max / sleep_default)", s.Name)
	}
	if s.Jitter != nil {
		return fmt.Errorf("loop: event-driven %q cannot set jitter (no periodic timer to randomize)", s.Name)
	}
	return nil
}

// validateContainerShape rejects execution-shaped fields on a
// container spec. Containers are structural nodes; setting Task,
// sleep envelope, or any execution hook is a category error rather
// than an unused field, so the failure mode here is loud rather
// than silently ignored.
func validateContainerShape(s *Spec) error {
	// Containers can declare a SupervisorProfile for cascade to
	// descendants, but the supervisor-turn Bernoulli trial itself
	// never fires on a container (containers don't execute), so
	// Supervisor and SupervisorProb must remain off.
	// SupervisorProfile-only is OK — that's the inheritance vector
	// the cascade walker uses.
	return containerShape(
		s.Name, s.Task,
		s.TaskBuilder != nil, s.TurnBuilder != nil, s.Handler != nil, s.WaitFunc != nil, s.PostIterate != nil,
		s.SleepMin, s.SleepMax, s.SleepDefault, s.MaxDuration,
		s.Jitter, s.MaxIter,
		s.Supervisor, s.SupervisorProb,
		len(s.Outputs), s.Completion,
	)
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
		Name:                 ns.Name,
		Task:                 ns.Task,
		Intent:               resolveIntent(ns.Intent, ns.Metadata),
		Operation:            ns.Operation,
		Completion:           ns.Completion,
		Outputs:              cloneOutputs(ns.Outputs),
		Subscriptions:        cloneEntitySubscriptions(ns.Subscriptions),
		Tags:                 append([]string(nil), ns.Tags...),
		ExcludeTools:         append([]string(nil), ns.ExcludeTools...),
		SleepMin:             ns.SleepMin,
		SleepMax:             ns.SleepMax,
		SleepDefault:         ns.SleepDefault,
		Jitter:               cloneFloat64Ptr(ns.Jitter),
		MaxDuration:          ns.MaxDuration,
		MaxIter:              ns.MaxIter,
		Supervisor:           ns.Supervisor,
		SupervisorProb:       ns.SupervisorProb,
		SupervisorProfile:    cloneLoopProfilePtr(ns.SupervisorProfile),
		OnRetrigger:          ns.OnRetrigger,
		TaskBuilder:          ns.TaskBuilder,
		TurnBuilder:          ns.TurnBuilder,
		PostIterate:          ns.PostIterate,
		WaitFunc:             ns.WaitFunc,
		Handler:              ns.Handler,
		RoutingFactors:       cloneStringMap(ns.RoutingFactors),
		DelegationGating:     ns.DelegationGating,
		FallbackContent:      ns.FallbackContent,
		Setup:                ns.Setup,
		RuntimeTools:         cloneRuntimeTools(ns.RuntimeTools),
		OutputContextBuilder: ns.OutputContextBuilder,
		Metadata:             cloneStringMap(ns.Metadata),
		ParentID:             ns.ParentID,
		ParentName:           ns.ParentName,
	}
}

// resolveIntent returns the loop's purpose string, preferring the first-class
// [Spec.Intent] field and falling back to the legacy metadata["intent"] for one
// release (#1106). When authoring no longer writes metadata["intent"] and stored
// specs are migrated, the fallback collapses to the field alone.
func resolveIntent(field string, metadata map[string]string) string {
	if strings.TrimSpace(field) != "" {
		return field
	}
	return metadata["intent"]
}

// containerShape is the shared shape contract for container loops. It
// rejects every execution-related field, returning a category-error for
// authoring mistakes (Spec layer) or programmer mistakes (Config layer)
// rather than silently ignoring the value at runtime.
func containerShape(name, task string, hasTaskBuilder, hasTurnBuilder, hasHandler, hasWaitFunc, hasPostIterate bool, sleepMin, sleepMax, sleepDefault, maxDuration time.Duration, jitter *float64, maxIter int, supervisor bool, supervisorProb float64, outputCount int, completion Completion) error {
	if strings.TrimSpace(task) != "" {
		return fmt.Errorf("loop: container %q cannot set task", name)
	}
	if hasTaskBuilder {
		return fmt.Errorf("loop: container %q cannot set TaskBuilder", name)
	}
	if hasTurnBuilder {
		return fmt.Errorf("loop: container %q cannot set TurnBuilder", name)
	}
	if hasHandler {
		return fmt.Errorf("loop: container %q cannot set Handler", name)
	}
	if hasWaitFunc {
		return fmt.Errorf("loop: container %q cannot set WaitFunc", name)
	}
	if hasPostIterate {
		return fmt.Errorf("loop: container %q cannot set PostIterate", name)
	}
	if sleepMin != 0 || sleepMax != 0 || sleepDefault != 0 {
		return fmt.Errorf("loop: container %q cannot set sleep envelope (containers never wake)", name)
	}
	if jitter != nil {
		return fmt.Errorf("loop: container %q cannot set jitter", name)
	}
	if maxDuration != 0 {
		return fmt.Errorf("loop: container %q cannot set max_duration", name)
	}
	if maxIter != 0 {
		return fmt.Errorf("loop: container %q cannot set max_iter", name)
	}
	if supervisor || supervisorProb != 0 {
		return fmt.Errorf("loop: container %q cannot set supervisor fields", name)
	}
	if outputCount > 0 {
		return fmt.Errorf("loop: container %q cannot declare outputs", name)
	}
	if completion != "" && completion != CompletionNone {
		return fmt.Errorf("loop: container %q cannot set completion (containers never produce a result)", name)
	}
	return nil
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
		Model:            opts.Model,
		RoutingFactors:   opts.RoutingFactors,
		DelegationGating: opts.DelegationGating,
		ExcludeTools:     opts.ExcludeTools,
		InitialTags:      append([]string(nil), s.Tags...),
		RuntimeTools:     cloneRuntimeTools(s.RuntimeTools),
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

// cloneLoopProfilePtr deep-copies an optional [router.LoopProfile]
// pointer so callers can mutate the result without affecting the
// underlying spec. Used to thread SupervisorProfile through
// [Spec.ToConfig] without aliasing the spec's overlay struct.
func cloneLoopProfilePtr(src *router.LoopProfile) *router.LoopProfile {
	if src == nil {
		return nil
	}
	c := *src
	if len(src.ExcludeTools) > 0 {
		c.ExcludeTools = append([]string(nil), src.ExcludeTools...)
	}
	if len(src.ExtraHints) > 0 {
		c.ExtraHints = cloneStringMap(src.ExtraHints)
	}
	return &c
}
