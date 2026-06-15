package loop

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

const detachedCompletionTimeout = 10 * time.Second

// Registry tracks all active loops and provides visibility into what is
// running. It enforces concurrency limits and coordinates graceful
// shutdown.
type Registry struct {
	mu       sync.RWMutex
	loops    map[string]*Loop
	maxLoops int
	logger   *slog.Logger
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithMaxLoops sets the maximum number of concurrent loops the registry
// will allow. Zero means unlimited.
func WithMaxLoops(n int) RegistryOption {
	return func(r *Registry) {
		r.maxLoops = n
	}
}

// WithRegistryLogger sets the logger for registry operations. Nil is
// ignored (keeps slog.Default()).
func WithRegistryLogger(l *slog.Logger) RegistryOption {
	return func(r *Registry) {
		if l != nil {
			r.logger = l
		}
	}
}

// NewRegistry creates a new loop registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		loops:  make(map[string]*Loop),
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// MultipleCoreError reports an attempt to register a second core
// loop — a container named [CoreLoopName]. The graph has exactly
// one structural root; the registry enforces this so callers
// (auto-create, manual spawn via tools) cannot accidentally
// produce a second.
type MultipleCoreError struct {
	// ExistingID is the loop ID of the core already in the registry.
	ExistingID string
}

func (e *MultipleCoreError) Error() string {
	return fmt.Sprintf("loop: cannot register a second core; existing core (id=%s) is already the singleton root", e.ExistingID)
}

// UnresolvedParentNameError reports an attempt to register a loop
// whose ParentName references a parent that is not currently
// registered. The registry refuses loud rather than silently
// default-parenting to core — silent fallback would lose the
// declared intent ("attach to outer when it comes up") permanently
// because there is no late-rebind path. Callers either need to
// ensure the named parent registers first (the bootstrap's
// topological sort handles this for definition-driven specs) or
// drop the ParentName and accept attachment to core.
type UnresolvedParentNameError struct {
	// LoopName is the loop being registered.
	LoopName string
	// ParentName is the unresolved parent reference.
	ParentName string
}

func (e *UnresolvedParentNameError) Error() string {
	return fmt.Sprintf("loop: cannot register %q — parent_name %q is not currently registered; spawn the parent first or drop parent_name to default to core", e.LoopName, e.ParentName)
}

// ContainerHasChildrenError reports a [Registry.StopLoop] attempt
// against a container that still has live descendants. The
// registry refuses rather than orphaning the children (their
// ParentID would point at a deregistered loop and ancestor walks
// would silently lose inherited tags/subscriptions). Callers can
// inspect [Children] for the names and either stop or re-parent
// them first; the definition reconciler uses this signal to log
// loudly and skip rather than fail-loop on a removed container
// that still has live workers underneath.
type ContainerHasChildrenError struct {
	// ContainerID is the loop being asked to stop.
	ContainerID string
	// ContainerName is the human-facing name from the loop's config.
	ContainerName string
	// ChildNames lists the live descendants by name, sorted for
	// stable diagnostics.
	ChildNames []string
}

func (e *ContainerHasChildrenError) Error() string {
	return fmt.Sprintf("loop: cannot stop container %q — it has %d live child loop(s): %v; stop or re-parent them first", e.ContainerName, len(e.ChildNames), e.ChildNames)
}

// Register adds a loop to the registry. Returns an error if the loop's
// ID is already registered or the concurrency limit would be exceeded.
// The loop is not started — call [Loop.Start] after registering.
func (r *Registry) Register(l *Loop) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.loops[l.id]; exists {
		return fmt.Errorf("loop ID %q already registered", l.id)
	}
	if r.maxLoops > 0 && len(r.loops) >= r.maxLoops {
		return fmt.Errorf("concurrency limit reached (%d loops)", r.maxLoops)
	}
	// Core is a singleton — exactly one structural root in the
	// graph. Scan for an existing core before accepting a second.
	// O(n) is fine: this only runs at register time and n is small.
	if l.IsCore() {
		for _, existing := range r.loops {
			if existing.IsCore() {
				return &MultipleCoreError{ExistingID: existing.id}
			}
		}
	}

	// Three parent-shape outcomes at Register:
	//
	//   - ParentID set       → caller wired the parent explicitly. Use it.
	//   - ParentName set     → caller declared a named parent but it
	//                          wasn't resolved by the time we reached
	//                          Register. Refuse loud — silently
	//                          default-parenting to core would lose
	//                          the declared intent permanently
	//                          because the registry has no late-
	//                          rebind path. Callers must spawn the
	//                          parent first (the bootstrap's
	//                          topological sort handles this for
	//                          definition-driven specs).
	//   - both empty         → orphan. Attach to core so the graph
	//                          stays single-rooted.
	//
	// Trim ParentName to match the rest of the codebase
	// ([DefinitionRegistry.AncestorSpecs] and the parent_name
	// resolution path both trim) so incidental whitespace doesn't
	// flip a resolvable reference into a loud refusal.
	//
	// Core is the exception — it sits above the tree by definition.
	if !l.IsCore() {
		parentName := strings.TrimSpace(l.config.ParentName)
		switch {
		case l.config.ParentID != "":
			// Explicit parent; leave it alone.
		case parentName != "":
			return &UnresolvedParentNameError{
				LoopName:   l.config.Name,
				ParentName: parentName,
			}
		default:
			for _, existing := range r.loops {
				if existing.IsCore() {
					l.setDefaultParentID(existing.id)
					break
				}
			}
		}
	}

	r.loops[l.id] = l
	// Containers inherit nothing themselves (they exist precisely to
	// provide tags to descendants). Wiring the function uniformly is
	// still fine — the resolver simply returns nil for a container with
	// no container ancestors, and any ancestor-of-container case is
	// handled by walk skipping non-containers.
	loopID := l.id
	l.setAncestorTagsFunc(func() []string {
		return r.ancestorContainerTags(loopID)
	})
	l.setEffectiveStateFunc(func() effectiveStateResult {
		// One walk, atomic snapshot: a second walk could observe a
		// SetSubscriptions between the two calls and yield tags +
		// subscriptions from different points in time. Status callers
		// would have no way to tell.
		return r.effectiveState(loopID)
	})
	r.logger.Debug("loop registered",
		"loop_id", l.id,
		"loop_name", l.config.Name,
		"parent_id", l.config.ParentID,
		"active_loops", len(r.loops),
	)
	return nil
}

// Deregister removes a loop from the registry. Safe to call for a loop
// that is not registered (no-op).
func (r *Registry) Deregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.loops[id]; !exists {
		return
	}
	delete(r.loops, id)
	r.logger.Debug("loop deregistered",
		"loop_id", id,
		"active_loops", len(r.loops),
	)
}

// Get returns the loop with the given ID, or nil if not found.
func (r *Registry) Get(id string) *Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loops[id]
}

// StatusByID returns the status snapshot of the loop with the given ID.
// The bool is false when no loop is registered under that ID. It lets
// read-only callers (e.g. the /v1/loops API) depend on the snapshot rather
// than the live *Loop.
func (r *Registry) StatusByID(id string) (Status, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l := r.loops[id]
	if l == nil {
		return Status{}, false
	}
	return l.Status(), true
}

// GetByName returns the first loop with the given name, or nil if not
// found. If multiple loops share a name, the result is undefined.
func (r *Registry) GetByName(name string) *Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.loops {
		if l.config.Name == name {
			return l
		}
	}
	return nil
}

// List returns a snapshot of all registered loops sorted by name.
func (r *Registry) List() []*Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Loop, 0, len(r.loops))
	for _, l := range r.loops {
		result = append(result, l)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].config.Name < result[j].config.Name
	})
	return result
}

// Statuses returns a snapshot of all registered loop statuses sorted by
// name.
func (r *Registry) Statuses() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Status, 0, len(r.loops))
	for _, l := range r.loops {
		result = append(result, l.Status())
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// ActiveCount returns the number of registered loops.
func (r *Registry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.loops)
}

// MaxLoops returns the configured concurrency limit. Zero means
// unlimited.
func (r *Registry) MaxLoops() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.maxLoops
}

// ancestorWalkLimit caps the depth of [Registry.Ancestors] walks to
// prevent unbounded recursion if a parent_id cycle ever slips past the
// definition-time guards. The graph is operator-curated and typically
// only a few levels deep, so any walk that exceeds this depth signals
// either a bug or a maliciously crafted spec.
const ancestorWalkLimit = 64

// Ancestors returns the chain of registered parent loops for loopID,
// beginning with the immediate parent and ending with the topmost
// reachable ancestor. The walk terminates when ParentID is empty or
// no longer registered; it never re-enters the starting loop and
// short-circuits at [ancestorWalkLimit] to bound work.
func (r *Registry) Ancestors(loopID string) []*Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()

	current := r.loops[loopID]
	if current == nil {
		return nil
	}

	var ancestors []*Loop
	seen := map[string]struct{}{loopID: {}}
	for i := 0; i < ancestorWalkLimit; i++ {
		parentID := current.config.ParentID
		if parentID == "" {
			break
		}
		if _, looped := seen[parentID]; looped {
			break
		}
		parent, ok := r.loops[parentID]
		if !ok {
			break
		}
		ancestors = append(ancestors, parent)
		seen[parentID] = struct{}{}
		current = parent
	}
	return ancestors
}

// Children returns loops whose ParentID equals loopID, sorted by name.
// Used to refuse deletion of containers that still have descendants and
// to surface child counts in container introspection.
func (r *Registry) Children(loopID string) []*Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var children []*Loop
	for _, l := range r.loops {
		if l.config.ParentID == loopID {
			children = append(children, l)
		}
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].config.Name < children[j].config.Name
	})
	return children
}

// effectiveStateResult is the bundle returned by the shared internal
// walker. Surfaced through dedicated Effective* methods rather than
// directly so each public field can carry its own GoDoc and so the
// internal shape can grow without changing every caller signature.
type effectiveStateResult struct {
	Subscriptions    []EffectiveSubscription
	Tags             []EffectiveTag
	ExcludeTools     []EffectiveExcludeTool
	RoutingFactors   []EffectiveRoutingFactor
	DelegationGating *EffectiveDelegationGating
	// SupervisorRoutingFactors is the parallel cascade for the
	// per-turn-mode overrides declared on [Spec.SupervisorProfile].
	// Same closest-wins semantics as RoutingFactors, walked over the
	// same container ancestor chain — but populated from each loop's
	// SupervisorProfile rather than Profile. Loop runtime consumes
	// this only during supervisor turns; normal turns ignore it.
	SupervisorRoutingFactors []EffectiveRoutingFactor
}

// EffectiveSubscriptions returns the deduplicated effective entity
// subscriptions for loopID with provenance: the union of the loop's
// own Subscriptions and every container ancestor's, walked parent-
// first. Dedup is first-wins by EntityID — the loop's own declaration
// takes precedence over an inherited one, so a child can override a
// container's default history/forecast settings by listing the same
// entity locally with different options.
//
// Each [EffectiveSubscription.From] is [EffectiveOriginSelf] for the
// loop's own declarations or the ancestor container's name for an
// inherited entry. Returns nil when the loop is not registered or
// nothing in the chain subscribes to anything.
//
// Only container ancestors contribute inheritance; service-loop
// ancestors are skipped to match the tag-inheritance contract from
// Phase 1A.
func (r *Registry) EffectiveSubscriptions(loopID string) []EffectiveSubscription {
	return r.effectiveState(loopID).Subscriptions
}

// EffectiveTags returns the deduplicated effective capability tags
// for loopID with provenance. Same walk and dedup semantics as
// [EffectiveSubscriptions]; own declarations carry
// [EffectiveOriginSelf] in From, inherited tags carry the ancestor
// container's name.
func (r *Registry) EffectiveTags(loopID string) []EffectiveTag {
	return r.effectiveState(loopID).Tags
}

// EffectiveExcludeTools returns the deduplicated union of the loop's
// own ExcludeTools and every container ancestor's, walked parent-
// first. Union semantics mean a descendant cannot un-exclude a tool
// the ancestor restricted; provenance on each entry tells the
// operator (or model) where the exclusion came from.
func (r *Registry) EffectiveExcludeTools(loopID string) []EffectiveExcludeTool {
	return r.effectiveState(loopID).ExcludeTools
}

// EffectiveRoutingFactors returns the resolved routing-factor map
// for loopID. The cascade walks leaf-first up through container
// ancestors with first-seen-wins dedup, so the closest declaration
// to the leaf wins on key collision (equivalent to "child wins").
// Each entry carries the origin loop name; collisions silently keep
// the closest declaration. Returns nil when nothing in the chain
// declares any factors.
func (r *Registry) EffectiveRoutingFactors(loopID string) []EffectiveRoutingFactor {
	return r.effectiveState(loopID).RoutingFactors
}

// EffectiveSupervisorRoutingFactors returns the per-turn-mode
// override cascade derived from [Spec.SupervisorProfile] across
// the loop and its container ancestors. Same leaf-first walk and
// closest-declaration-wins dedup semantics as
// [EffectiveRoutingFactors], but sourced from SupervisorProfile
// rather than Profile. The loop runtime overlays these on top of
// the normal-turn routing factors only when the iteration is a
// supervisor turn; normal turns ignore this surface entirely.
func (r *Registry) EffectiveSupervisorRoutingFactors(loopID string) []EffectiveRoutingFactor {
	return r.effectiveState(loopID).SupervisorRoutingFactors
}

// EffectiveDelegationGating returns the resolved delegation-gating
// value for loopID using closest-non-empty semantics: the loop's
// own value wins if set; otherwise the nearest ancestor that
// declares a non-empty value wins. Returns nil when no loop in the
// chain declared a non-empty value (i.e. the agent default
// applies).
func (r *Registry) EffectiveDelegationGating(loopID string) *EffectiveDelegationGating {
	return r.effectiveState(loopID).DelegationGating
}

// AncestorSubscriptions returns the same union [EffectiveSubscriptions]
// produces, but stripped of provenance. Kept as the convenient
// rendering-side projection that the awareness loop subscription
// provider consumes; the renderer doesn't need to attribute entries.
func (r *Registry) AncestorSubscriptions(loopID string) []EntitySubscription {
	subs := r.EffectiveSubscriptions(loopID)
	if len(subs) == 0 {
		return nil
	}
	out := make([]EntitySubscription, len(subs))
	for i, sub := range subs {
		out[i] = sub.EntitySubscription
	}
	return out
}

// ancestorContainerTags returns the deduplicated capability tags
// inherited from container ancestors only (excludes the loop's own
// tags) and drops provenance. Used by [Loop] tag preparation, which
// already pulls own tags from config separately.
func (r *Registry) ancestorContainerTags(loopID string) []string {
	tags := r.EffectiveTags(loopID)
	if len(tags) == 0 {
		return nil
	}
	var inherited []string
	for _, t := range tags {
		if t.From == EffectiveOriginSelf {
			continue
		}
		inherited = append(inherited, t.Tag)
	}
	if len(inherited) == 0 {
		return nil
	}
	return inherited
}

// effectiveState is the shared single-pass walker behind every
// Registry.Effective* method. It snapshots the parent chain under
// the registry lock (parent_id and operation are construction-set
// invariants), then pulls each loop's mutable state via the locked
// snapshot accessors on [Loop] — preventing the data races that
// would arise from reading l.config.* directly while holding only
// r.mu.
//
// This is the LIVE walker. Its persisted-side counterpart is
// [EvaluateEffectiveConditions] (and the per-field
// [DefinitionRegistry.AncestorSpecs] consumers). The two walkers
// agree only as long as mutators persist BEFORE patching live
// state — see the load-bearing ordering at
// [app.App.mutateLoopSubscriptions]. A mutator that updates live
// state without persisting (or persists after patching) would let
// the dual-walk model silently disagree: ConfigureLoop callers
// reading the live snapshot would see the change, while
// definition-snapshot consumers (the API surface, eligibility
// checks, view rendering) would not, until the next persist.
//
// Each cascading field uses its own merge semantics:
//
//   - Subscriptions / Tags / ExcludeTools — union, first-wins on
//     key (entity_id / tag / tool name). Closest declaration in
//     the chain takes priority on collision.
//   - RoutingFactors — map merge, child-wins on key collision.
//     Same closest-declaration-wins behavior, just expressed over
//     map[string]string instead of a slice.
//   - DelegationGating — single scalar, closest non-empty wins.
func (r *Registry) effectiveState(loopID string) effectiveStateResult {
	r.mu.RLock()
	walk := make([]*Loop, 0, 4)
	current := r.loops[loopID]
	if current != nil {
		walk = append(walk, current)
		for i := 0; i < ancestorWalkLimit; i++ {
			parentID := current.config.ParentID
			if parentID == "" {
				break
			}
			parent, ok := r.loops[parentID]
			if !ok {
				break
			}
			walk = append(walk, parent)
			current = parent
		}
	}
	r.mu.RUnlock()

	if len(walk) == 0 {
		return effectiveStateResult{}
	}

	result := effectiveStateResult{}
	seenSubs := make(map[string]struct{})
	seenTags := make(map[string]struct{})
	seenExcludes := make(map[string]struct{})
	seenFactors := make(map[string]struct{})
	seenSupervisorFactors := make(map[string]struct{})

	for i, l := range walk {
		// The starting loop contributes regardless of operation; only
		// ancestors are filtered to container nodes — matches the
		// tag-inheritance contract from Phase 1A. The core container
		// participates the same way every other container does.
		if i > 0 && l.Operation() != OperationContainer {
			continue
		}
		origin := EffectiveOriginSelf
		if i > 0 {
			origin = l.Name()
		}

		for _, sub := range l.Subscriptions() {
			if sub.EntityID == "" {
				continue
			}
			if _, dup := seenSubs[sub.EntityID]; dup {
				continue
			}
			seenSubs[sub.EntityID] = struct{}{}
			result.Subscriptions = append(result.Subscriptions, EffectiveSubscription{
				EntitySubscription: sub,
				From:               origin,
			})
		}
		for _, tag := range l.tagsSnapshot() {
			if tag == "" {
				continue
			}
			if _, dup := seenTags[tag]; dup {
				continue
			}
			seenTags[tag] = struct{}{}
			result.Tags = append(result.Tags, EffectiveTag{Tag: tag, From: origin})
		}
		for _, tool := range l.excludeToolsSnapshot() {
			if tool == "" {
				continue
			}
			if _, dup := seenExcludes[tool]; dup {
				continue
			}
			seenExcludes[tool] = struct{}{}
			result.ExcludeTools = append(result.ExcludeTools, EffectiveExcludeTool{
				Tool: tool,
				From: origin,
			})
		}
		for key, value := range l.routingFactorsSnapshot() {
			if key == "" {
				continue
			}
			if _, dup := seenFactors[key]; dup {
				continue
			}
			seenFactors[key] = struct{}{}
			result.RoutingFactors = append(result.RoutingFactors, EffectiveRoutingFactor{
				Key:   key,
				Value: value,
				From:  origin,
			})
		}
		// SupervisorRoutingFactors: parallel cascade fed by each
		// loop's SupervisorProfile. Same closest-wins semantics —
		// the leaf's SupervisorProfile beats ancestors', and once a
		// key is set it sticks for the rest of the walk.
		for key, value := range l.supervisorRoutingFactorsSnapshot() {
			if key == "" {
				continue
			}
			if _, dup := seenSupervisorFactors[key]; dup {
				continue
			}
			seenSupervisorFactors[key] = struct{}{}
			result.SupervisorRoutingFactors = append(result.SupervisorRoutingFactors, EffectiveRoutingFactor{
				Key:   key,
				Value: value,
				From:  origin,
			})
		}
		if result.DelegationGating == nil {
			if gating := l.delegationGatingSnapshot(); gating != "" {
				result.DelegationGating = &EffectiveDelegationGating{
					Value: gating,
					From:  origin,
				}
			}
		}
	}
	return result
}

// Core returns the singleton core loop — the container with the
// well-known name [CoreLoopName] — or nil if none is currently
// registered. The app's bootstrap auto-creates one at startup, so
// a non-nil Core is the steady-state expectation; nil only happens
// in tests or in the narrow window before bootstrap runs.
func (r *Registry) Core() *Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.loops {
		if l.IsCore() {
			return l
		}
	}
	return nil
}

// FindByName returns all live loops with the exact given name.
func (r *Registry) FindByName(name string) []*Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var matches []*Loop
	for _, l := range r.loops {
		if l.config.Name == name {
			matches = append(matches, l)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].id < matches[j].id
	})
	return matches
}

// StopLoopByName cancels one registered loop by exact name. It returns
// an error when no loop matches or when the name is ambiguous.
func (r *Registry) StopLoopByName(name string) error {
	matches := r.FindByName(name)
	switch len(matches) {
	case 0:
		return fmt.Errorf("loop named %q not found", name)
	case 1:
		return r.StopLoop(matches[0].id)
	default:
		ids := make([]string, 0, len(matches))
		for _, l := range matches {
			ids = append(ids, l.id)
		}
		return fmt.Errorf("loop name %q is ambiguous; retry with loop_id from %v", name, ids)
	}
}

// ShutdownAll cancels all registered loops and waits for them to drain.
// The provided context controls the maximum time to wait; if it expires,
// remaining loops are abandoned. Returns the number of loops that were
// stopped.
func (r *Registry) ShutdownAll(ctx context.Context) int {
	r.mu.RLock()
	loops := make([]*Loop, 0, len(r.loops))
	for _, l := range r.loops {
		loops = append(loops, l)
	}
	r.mu.RUnlock()

	r.logger.Info("shutting down all loops", "count", len(loops))

	// Fire all cancellations in parallel (non-blocking).
	for _, l := range loops {
		l.cancel0()
	}

	// Wait for each loop to finish, respecting the context deadline.
	// Loops that were never started (Done()==nil) are treated as
	// already drained.
	stopped := 0
	for _, l := range loops {
		done := l.Done()
		if done == nil {
			// Never started — just deregister.
			stopped++
			r.Deregister(l.id)
			continue
		}
		select {
		case <-done:
			stopped++
			r.Deregister(l.id)
		case <-ctx.Done():
			r.logger.Warn("shutdown context expired, abandoning remaining loops",
				"stopped", stopped,
				"remaining", len(loops)-stopped,
			)
			return stopped
		}
	}

	r.logger.Info("all loops shut down", "stopped", stopped)
	return stopped
}

func (r *Registry) configureLoop(l *Loop, setup func(*Loop)) {
	// Call Setup before Register/Start so the caller can register
	// tools or perform other initialization that needs *Loop.
	if setup != nil {
		setup(l)
	}
}

func (r *Registry) startLoop(ctx context.Context, name string, l *Loop, setup func(*Loop), autoDeregister bool) error {
	r.configureLoop(l, setup)

	if err := r.Register(l); err != nil {
		return err
	}

	if err := l.Start(ctx); err != nil {
		r.Deregister(l.id)
		return fmt.Errorf("start loop %q: %w", name, err)
	}

	// Containers (including the singleton core) close Done()
	// immediately inside Start because they don't run a goroutine.
	// The auto-deregister hook below interprets a closed Done as
	// "loop finished, clean up the registry entry" — which would
	// instantly delete every container we just registered. Skip
	// the hook for them; their lifetime in the registry is bounded
	// by explicit [Deregister]/[StopLoop]/[ShutdownAll] calls, not
	// by Done signaling.
	if autoDeregister && l.config.Operation != OperationContainer {
		go func(id string, done <-chan struct{}) {
			<-done
			r.Deregister(id)
		}(l.id, l.Done())
	}

	return nil
}

// SpawnLoop creates a new loop with the given config, registers it, and
// starts it. This is the primary entry point for creating loops. Returns
// the loop ID on success.
func (r *Registry) SpawnLoop(ctx context.Context, cfg Config, deps Deps) (string, error) {
	l, err := New(cfg, deps)
	if err != nil {
		return "", fmt.Errorf("create loop %q: %w", cfg.Name, err)
	}
	if err := r.startLoop(ctx, cfg.Name, l, cfg.Setup, true); err != nil {
		return "", err
	}

	return l.id, nil
}

// SpawnSpec creates, registers, and starts a loop from a [Spec]. It is
// the spec-based entrypoint; [Registry.SpawnLoop] with [Config] remains
// available for callers that build a Config directly.
func (r *Registry) SpawnSpec(ctx context.Context, spec Spec, deps Deps) (string, error) {
	l, err := NewFromSpec(spec, deps)
	if err != nil {
		return "", fmt.Errorf("create loop %q: %w", spec.Name, err)
	}
	if err := r.startLoop(ctx, spec.Name, l, spec.ToConfig().Setup, true); err != nil {
		return "", err
	}

	return l.id, nil
}

// Launch starts a loop from a [Launch]. Request/reply launches wait for
// completion and return a final status snapshot; background and service
// launches detach immediately and leave the loop running in the
// registry.
func (r *Registry) Launch(ctx context.Context, launch Launch, deps Deps) (LaunchResult, error) {
	if err := launch.Validate(); err != nil {
		return LaunchResult{}, err
	}

	spec := launch.Spec
	spec.Operation = effectiveOperation(spec.Operation)
	l, err := NewFromLaunch(launch, deps)
	if err != nil {
		return LaunchResult{}, fmt.Errorf("create loop %q: %w", spec.Name, err)
	}

	cfg := l.config
	detached := spec.Operation != OperationRequestReply
	if err := r.startLoop(ctx, spec.Name, l, cfg.Setup, detached); err != nil {
		return LaunchResult{}, err
	}

	result := LaunchResult{
		LoopID:    l.id,
		Operation: spec.Operation,
		Detached:  detached,
	}
	if detached {
		r.startDetachedCompletion(launch, l)
		return result, nil
	}

	finalStatus := make(chan Status, 1)
	go func() {
		<-l.Done()
		st := l.Status()
		r.Deregister(l.id)
		finalStatus <- st
	}()

	waitCtx := ctx
	waitCancel := func() {}
	if launch.RunTimeout > 0 {
		waitCtx, waitCancel = context.WithTimeout(ctx, launch.RunTimeout)
	}
	defer waitCancel()

	select {
	case st := <-finalStatus:
		result.Response = l.lastResponseSnapshot()
		result.FinalStatus = &st
		return result, nil
	case <-waitCtx.Done():
		if launch.RunTimeout > 0 && waitCtx.Err() == context.DeadlineExceeded {
			l.Stop()
		}
		return result, waitCtx.Err()
	}
}

func (r *Registry) startDetachedCompletion(launch Launch, l *Loop) {
	if l == nil {
		return
	}
	if launch.Spec.Completion != CompletionConversation && launch.Spec.Completion != CompletionChannel {
		return
	}
	if l.deps.CompletionSink == nil {
		return
	}
	conversationID := strings.TrimSpace(launch.CompletionConversationID)
	channelTarget := CloneCompletionChannelTarget(launch.CompletionChannel)
	if conversationID == "" {
		if launch.Spec.Completion == CompletionConversation {
			return
		}
	}
	if channelTarget == nil && launch.Spec.Completion == CompletionChannel {
		return
	}

	go func() {
		done := l.Done()
		if done != nil {
			<-done
		}

		resp := l.lastResponseSnapshot()
		status := l.Status()
		content := formatCompletionContent(launch, resp, status)
		if strings.TrimSpace(content) == "" {
			return
		}

		deliveryCtx, cancel := context.WithTimeout(context.Background(), detachedCompletionTimeout)
		defer cancel()

		if err := l.deps.CompletionSink(deliveryCtx, CompletionDelivery{
			Mode:           launch.Spec.Completion,
			ConversationID: conversationID,
			Channel:        channelTarget,
			Content:        content,
			LoopID:         l.id,
			LoopName:       l.config.Name,
			Response:       resp,
			Status:         &status,
		}); err != nil {
			r.logger.Warn("detached loop completion delivery failed",
				"loop_id", l.id,
				"loop_name", l.config.Name,
				"delivery_mode", launch.Spec.Completion,
				"conversation_id", conversationID,
				"channel_target", channelTarget,
				"delivery_timeout", detachedCompletionTimeout,
				"error", err,
			)
			return
		}
		r.logger.Info("detached loop completion delivered",
			"loop_id", l.id,
			"loop_name", l.config.Name,
			"delivery_mode", launch.Spec.Completion,
			"conversation_id", conversationID,
			"channel_target", channelTarget,
		)
	}()
}

func formatCompletionContent(launch Launch, resp *Response, status Status) string {
	label := strings.TrimSpace(launch.Task)
	if label == "" {
		label = strings.TrimSpace(launch.Spec.Name)
	}
	if label == "" {
		label = "background task"
	}

	prefix := fmt.Sprintf("Background task complete (%s).", label)
	switch {
	case resp != nil && strings.TrimSpace(resp.Content) != "":
		return prefix + "\n\n" + resp.Content
	case status.LastError != "":
		return prefix + "\n\nTask failed: " + status.LastError
	case resp != nil && resp.Exhausted && resp.FinishReason != "":
		return prefix + "\n\nTask exhausted: " + resp.FinishReason
	default:
		return prefix
	}
}

// StopLoop stops a loop by ID and deregisters it once the goroutine
// has exited. Returns an error if the loop is not found. If the
// goroutine does not exit within 10 seconds (the Stop timeout), the
// loop remains registered to avoid orphaning a running goroutine.
//
// Refuses to stop the singleton core. The graph's structural root
// has no operator-facing kill switch — the bootstrap manages its
// lifecycle. [ShutdownAll] still tears it down at process exit
// because that's the legitimate "everything off" path.
func (r *Registry) StopLoop(id string) error {
	l := r.Get(id)
	if l == nil {
		return fmt.Errorf("loop %q not found", id)
	}
	if l.IsCore() {
		return fmt.Errorf("loop: cannot stop core %q — the structural root has no operator-facing kill switch", l.config.Name)
	}
	// Stopping a container with live descendants would orphan them
	// — their ParentID would point at a deregistered loop, ancestor
	// walks would short-circuit, and inherited tags/subs/excludes
	// would silently disappear. Refuse with the children named, the
	// same pattern loop_definition_delete uses for the persisted
	// side; the model or operator can re-parent or stop the
	// children first.
	if l.config.Operation == OperationContainer {
		children := r.Children(id)
		if len(children) > 0 {
			names := make([]string, 0, len(children))
			for _, child := range children {
				names = append(names, child.config.Name)
			}
			sort.Strings(names)
			return &ContainerHasChildrenError{
				ContainerID:   id,
				ContainerName: l.config.Name,
				ChildNames:    names,
			}
		}
	}

	l.Stop()

	// Only deregister if the goroutine actually exited. Stop() waits
	// up to 10s internally; if Done is closed, it exited cleanly.
	done := l.Done()
	if done == nil {
		// Never started — safe to deregister.
		r.Deregister(id)
		return nil
	}

	select {
	case <-done:
		r.Deregister(id)
	default:
		r.logger.Warn("loop goroutine still running after Stop, keeping registered",
			"loop_id", id,
			"loop_name", l.config.Name,
		)
	}

	return nil
}
