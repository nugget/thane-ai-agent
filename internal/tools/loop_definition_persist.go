package tools

import (
	"context"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// setSharedLoopDeps records the two dependencies both loop tool families
// receive — the definition registry and the durable commit hook — which
// persistLoopSpec reads from one place. App hands the same objects to
// each (a.loopDefinitionRegistry and a.commitLoopDefinition).
//
// The definition family is authoritative: it owns these tools, so a
// value it supplies wins. It no longer clears them by passing nil,
// because a nil registry reaching persistLoopSpec would skip the
// config-owned guard rather than enforce it.
func (r *Registry) setSharedLoopDeps(registry *looppkg.DefinitionRegistry, commit func(context.Context, looppkg.Spec, time.Time) error) {
	if registry != nil {
		r.loopDefinitionRegistry = registry
	}
	if commit != nil {
		r.commitLoopDefinitionSpec = commit
	}
}

// fallbackSharedLoopDeps supplies the same two dependencies only when
// nothing has yet. The Core front door is registered unconditionally, so
// a configuration that wires it without the gated definition family must
// still reach a real registry — otherwise the guard it depends on is
// silently absent in exactly the configuration that has the fewest other
// checks.
func (r *Registry) fallbackSharedLoopDeps(registry *looppkg.DefinitionRegistry, commit func(context.Context, looppkg.Spec, time.Time) error) {
	if r.loopDefinitionRegistry == nil {
		r.loopDefinitionRegistry = registry
	}
	if r.commitLoopDefinitionSpec == nil {
		r.commitLoopDefinitionSpec = commit
	}
}

// persistLoopSpec is the single path from a validated Spec to a stored
// loop definition: refuse config-owned names, commit through the durable
// chokepoint, and report whether a running instance survived the commit
// still carrying its launched-time config.
//
// That last return is the reason this is one function rather than two.
// A commit's reconcile can SPAWN an absent active definition, so a loop
// that is live only after the commit is already running the spec just
// written — only an instance that existed beforehand and kept its ID is
// stale. Both callers have to say so, in their own vocabulary:
// loop_definition_set as a notice on the result, the guided create path
// as the reused loop it returns. Neither is free to get it wrong,
// because a result that omits it reads as though the new spec were live
// when the loop is still running the old one.
//
// Callers keep their own result shaping and their own additional policy
// — the guided path's replace flag, its refusal to convert an executing
// loop into a container — because those are caller rules rather than
// registry invariants.
func (r *Registry) persistLoopSpec(ctx context.Context, spec looppkg.Spec) (*looppkg.Loop, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return nil, err
	}
	if _, _, err := ensureDefinitionMutable(snapshot, spec.Name); err != nil {
		return nil, err
	}

	// Captured before the commit, for the reason in the doc comment.
	prior := r.runningLoopByName(spec.Name)

	if err := commitSpecThroughChokepoint(ctx, r.commitLoopDefinitionSpec, r.loopDefinitionRegistry, spec, time.Now().UTC()); err != nil {
		return nil, err
	}
	if prior == nil {
		return nil, nil
	}
	if after := r.runningLoopByName(spec.Name); after != nil && after.ID() == prior.ID() {
		return after, nil
	}
	return nil, nil
}
