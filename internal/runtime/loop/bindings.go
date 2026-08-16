package loop

import (
	"fmt"
	"sort"
	"strings"
)

// Binding keys. A binding names the specific instance of a shared
// resource a loop may reach, where the subsystem would otherwise pick a
// default on the model's behalf or let the model pick for itself.
//
// The distinction from [Spec.Tags] is worth holding onto: a tag decides
// WHETHER a surface is available, and a binding decides WHICH instance
// of it the caller gets. Granting the forge tag hands a loop every
// configured account, because the account argument on every forge tool
// is the model's to fill in; binding forge_account decides that
// question before the model can answer it.
//
// Keys are a closed set on purpose. An unrecognized key is a typo or a
// stale spec, and both should refuse the boot rather than be silently
// ignored — a binding that quietly does nothing is worse than no
// binding at all, because it reads like a boundary while being none.
//
// Naming: a key is <subsystem>_<what the value names>, not the
// subsystem alone. "forge_account" rather than "forge", because the
// value is an account and "forge: github-readonly" asserts something
// that is not true. The rule is not cosmetic — subsystems have more
// than one bindable dimension. Companion tools already select on both
// an account and a client_id, so subsystem-named keys would produce
// "companion" meaning the account beside "companion_client_id" meaning
// the device: one key named for the subsystem and its sibling named
// for the value, in the same map. Naming for the value also matches
// the config path the value is read from (forge.accounts[].name) and
// the tool argument it fills in (every forge tool's "account"), so a
// bound loop can connect the three without an inference step.
//
// Keys deliberately do not match capability-tag names, even where a
// subsystem has both. Tags and bindings answer different questions,
// and spelling them identically invites the reading that a binding is
// a kind of tag.
const (
	// BindingForgeAccount names the forge account (from forge.accounts
	// in config) this loop's forge tools resolve to. Its value is an
	// account name, and the account must exist at hydration.
	BindingForgeAccount = "forge_account"

	// BindingRepositoryRoot names the forge-maintained repository root this
	// loop's file and repository-history tools may resolve. Its value is the
	// root handle without a trailing colon, and the root must exist at
	// hydration.
	BindingRepositoryRoot = "repo_root"
)

// registeredBindings is the closed set of binding keys, each with the
// operator-facing description of what declaring it does. New entries
// belong here the moment a subsystem learns to honor one — the
// registry is what makes an unknown key an error rather than a guess.
var registeredBindings = map[string]string{
	BindingForgeAccount:   "Forge account name this loop's forge tools resolve to. Empty account arguments default to it, and other accounts are refused.",
	BindingRepositoryRoot: "Repository root name this loop's file and repository-history tools resolve to. Omitted roots default to it, and other roots are refused.",
}

// BindingKeys returns the registered binding keys in sorted order, for
// error messages and documentation.
func BindingKeys() []string {
	keys := make([]string, 0, len(registeredBindings))
	for k := range registeredBindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// BindingDescription returns the operator-facing description of a
// registered binding key, or "" when the key is not registered.
func BindingDescription(key string) string {
	return registeredBindings[key]
}

// ValidateBindings checks that every key is registered and every value
// is non-empty. It deliberately does not check that the value names a
// resource that exists: this package knows nothing about forge
// accounts, and resolving a value against live configuration is the
// hydrating layer's job (see app.validateLoopBindings). Splitting the
// two keeps the spec grammar independent of which subsystems happen to
// be configured at this site.
func ValidateBindings(bindings map[string]string) error {
	for key, value := range bindings {
		if _, ok := registeredBindings[key]; !ok {
			return fmt.Errorf("unknown binding %q; registered bindings are %s",
				key, strings.Join(BindingKeys(), ", "))
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("binding %q has an empty value", key)
		}
	}
	return nil
}

// MergeBindings resolves the effective bindings for an iteration.
//
// Ancestors win. This is the opposite of the routing-factor cascade,
// and deliberately so: a routing factor is a preference a child may
// know better than its container, while a binding is a restriction the
// container imposed. If a child could rebind a key its ancestor
// declared, the ancestor's binding would be advice rather than a
// boundary, and any loop could escape a restriction by declaring its
// way out of it. A child may still bind a key no ancestor mentions.
//
// Later arguments are closer to the leaf, so earlier arguments win on
// collision. Returns nil when nothing declares anything, so an unbound
// loop carries no binding rather than an empty map.
//
// Exported because the same precedence governs every mechanism that
// starts a new turn, not just the ancestor walk: guided creation,
// full-spec definition authoring, and ad-hoc launch all merge a
// caller's bindings over a model-authored spec, and the caller has to
// win there for the same reason an ancestor wins here.
func MergeBindings(sets ...map[string]string) map[string]string {
	var out map[string]string
	for _, set := range sets {
		for key, value := range set {
			if value == "" {
				continue
			}
			if _, taken := out[key]; taken {
				continue
			}
			if out == nil {
				out = make(map[string]string, len(set))
			}
			out[key] = value
		}
	}
	return out
}

// CloneBindings copies a binding map, returning nil for an empty input
// so callers can hand the result to a request without materializing an
// empty map on every iteration. Exported for callers that carry a
// binding across a package boundary — the delegate executor hands the
// caller's bindings to the loop it launches.
func CloneBindings(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// EffectiveBindingsFromSpecChain resolves the bindings a loop would
// carry given a persisted ancestry chain, where index 0 is the loop
// itself and later entries are its ancestors outward.
//
// It mirrors the live registry's cascade rather than reimplementing it:
// ancestors contribute only when they are containers, and the outermost
// declaration wins. Callers that authorize against stored definitions
// need this, and having it live beside [MergeBindings] and the walker
// is what keeps the two from drifting into disagreement about what a
// chain means.
func EffectiveBindingsFromSpecChain(chain []Spec) map[string]string {
	sets := make([]map[string]string, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		if i > 0 && chain[i].Operation != OperationContainer {
			continue
		}
		sets = append(sets, chain[i].Bindings)
	}
	return MergeBindings(sets...)
}
