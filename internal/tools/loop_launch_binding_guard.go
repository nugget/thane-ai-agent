package tools

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// guardLaunchParent refuses a launch whose requested parent would move
// a binding the caller is operating inside.
//
// Bindings resolve ancestors-first, which is right when parentage is
// declared by an operator: a container's restriction should outrank
// what its descendants say about themselves. But parent_id is a
// caller-supplied field on both launch surfaces, and under
// ancestors-win, choosing a parent is choosing a binding. A loop bound
// to a read-only forge account could name a container bound to the
// write account and have the cascade hand the spawned turn that
// account instead — merging the caller's binding onto the child spec
// does not help, because the cascade outranks the spec by design.
//
// Refusal rather than reconciliation is deliberate. Account names have
// no ordering, so there is no "tighter of the two" to pick: a conflict
// means the caller asked for something the boundary cannot express,
// and silently choosing either side would be a guess about intent.
// A caller may still parent under a container that binds keys it does
// not, or that binds the same key to the same value.
// A launch can name its parent two ways — the launch-level parent_id
// and the spec's parent_name — and which one the runtime resolves
// differs by path. Both are checked rather than reasoning about which
// applies here, because the cost of checking the inapplicable one is
// nothing and the cost of missing the applicable one is the escape.
func (r *Registry) guardLaunchParent(ctx context.Context, parentID, parentName string) error {
	caller := looppkg.BindingsFromContext(ctx)
	if len(caller) == 0 || r.liveLoopRegistry == nil {
		return nil
	}

	parentID = strings.TrimSpace(parentID)
	if name := strings.TrimSpace(parentName); parentID == "" && name != "" {
		// An unresolvable name is not this guard's error to raise; the
		// launch itself refuses or default-parents.
		if parent := r.liveLoopRegistry.GetByName(name); parent != nil {
			parentID = parent.ID()
		}
	}
	if parentID == "" {
		return nil
	}

	for _, inherited := range r.liveLoopRegistry.InheritableBindings(parentID) {
		want, bound := caller[inherited.Key]
		if !bound || inherited.Value == want {
			continue
		}
		return fmt.Errorf("cannot launch under parent %q: it would impose %s=%q, but this loop is bound to %s=%q. A parent's binding outranks the launch, so this would move the boundary you are operating inside — choose a parent that does not bind %s, or one that binds it to %q",
			parentID, inherited.Key, inherited.Value,
			inherited.Key, want, inherited.Key, want)
	}
	return nil
}
