package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// applyCallerBindingsToAuthoredSpec carries a bound caller's resource
// boundary into a spec the caller authors. The caller wins over the spec's own
// declarations, while an outer container still wins at runtime; the latter is
// therefore checked explicitly so nesting under an incompatible container
// cannot turn the inherited boundary into an escape.
func (r *Registry) applyCallerBindingsToAuthoredSpec(ctx context.Context, spec looppkg.Spec) (looppkg.Spec, error) {
	caller := looppkg.BindingsFromContext(ctx)
	if len(caller) == 0 {
		return spec, nil
	}
	spec.Bindings = looppkg.MergeBindings(caller, spec.Bindings)
	if err := r.requireCallerBindingsForSpec(ctx, spec); err != nil {
		return looppkg.Spec{}, err
	}
	return spec, nil
}

// requireCallerBindingsForDefinition refuses to start or reconfigure a stored
// definition that would run outside a bound caller's resource boundary. Stored
// definitions remain independently authored: they are not silently rebound at
// launch, but a bound caller cannot use one as a privilege bridge.
func (r *Registry) requireCallerBindingsForDefinition(ctx context.Context, name, action string) error {
	caller := looppkg.BindingsFromContext(ctx)
	if len(caller) == 0 {
		return nil
	}
	if r.loopDefinitionRegistry == nil {
		return fmt.Errorf("%s %q: loop definition registry not configured", action, name)
	}
	chain := r.loopDefinitionRegistry.AncestorSpecs(name)
	if len(chain) == 0 {
		return &looppkg.UnknownDefinitionError{Name: name}
	}
	return requireCallerBindings(action, name, caller, effectiveBindingsFromDefinitionChain(chain))
}

func (r *Registry) requireCallerBindingsForSpec(ctx context.Context, spec looppkg.Spec) error {
	caller := looppkg.BindingsFromContext(ctx)
	if len(caller) == 0 {
		return nil
	}

	chain := []looppkg.Spec{spec}
	if parentName := strings.TrimSpace(spec.ParentName); parentName != "" && r.loopDefinitionRegistry != nil {
		chain = append(chain, r.loopDefinitionRegistry.AncestorSpecs(parentName)...)
	}
	return requireCallerBindings("author definition", spec.Name, caller, effectiveBindingsFromDefinitionChain(chain))
}

// effectiveBindingsFromDefinitionChain mirrors the live registry's binding
// cascade over DefinitionRegistry.AncestorSpecs output: index 0 is the leaf,
// ancestors contribute only when they are containers, and the outermost
// declaration wins.
func effectiveBindingsFromDefinitionChain(chain []looppkg.Spec) map[string]string {
	sets := make([]map[string]string, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		if i > 0 && chain[i].Operation != looppkg.OperationContainer {
			continue
		}
		sets = append(sets, chain[i].Bindings)
	}
	return looppkg.MergeBindings(sets...)
}

func requireCallerBindings(action, name string, caller, effective map[string]string) error {
	keys := make([]string, 0, len(caller))
	for key := range caller {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := caller[key]
		if effective[key] == value {
			continue
		}
		return fmt.Errorf("cannot %s %q from this bound context: it must resolve binding %s=%q, but its own binding or an outer container resolves differently or not at all; bind the definition and every declaring container to the caller's value, or perform the action from an unbound context", action, name, key, value)
	}
	return nil
}
