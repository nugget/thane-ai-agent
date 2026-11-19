package mcp

import (
	"encoding/json"
	"maps"
	"net/url"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Bounds for inlining local $ref pointers. Inlining copies a target
// into every place that references it, so a small schema whose
// definitions are referenced many times can grow far beyond its own
// size. When the copies would add more than maxInlinedBytes, inlining
// is abandoned and the schema keeps its pointers and definitions
// unchanged, so inlining never grows a schema by more than that.
// A reference nested deeper than maxRefDepth becomes a permissive {}
// schema, as a recursive one does.
const (
	maxRefDepth     = 8        // nested reference expansions on one path
	maxInlinedBytes = 32 << 10 // bytes the inlined copies may add to one schema
)

// refOverBudgetChange is the change reported when inlining is abandoned.
var refOverBudgetChange = "left $ref pointers in place: inlining would exceed " + strconv.Itoa(maxInlinedBytes) + " bytes"

// normalizeInputSchema returns a deep copy of an MCP tool's inputSchema
// made safe for every model provider, plus a short description of each
// change it made (for logs). The input is never mutated.
//
// In order: local $ref pointers ("#", "#/$defs/…", "#/definitions/…",
// any "#/…" JSON pointer) are inlined with bounded depth (a cyclic or
// too-deep reference becomes {}), and $defs/definitions are dropped
// once inlined, unless the copies would add more than maxInlinedBytes,
// in which case the pointers and definitions all stay as they were;
// top-level oneOf/allOf/anyOf are merged away by
// llm.StripTopLevelCompositionKeywords; the root is forced to
// type "object" with a properties map, as MCP requires.
func normalizeInputSchema(in map[string]any) (map[string]any, []string) {
	var changes []string
	if in == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, []string{"added a missing input schema"}
	}
	schema, _ := deepCopyJSON(in).(map[string]any)

	if refChanges := inlineLocalRefs(schema); len(refChanges) > 0 {
		changes = append(changes, refChanges...)
	}

	stripped, removed := llm.StripTopLevelCompositionKeywords(schema)
	if len(removed) > 0 {
		schema = stripped
		changes = append(changes, "merged top-level "+strings.Join(removed, "/"))
	}

	if t, ok := schema["type"]; !ok {
		schema["type"] = "object"
		changes = append(changes, `added root type "object"`)
	} else if t != "object" {
		schema["type"] = "object"
		changes = append(changes, `forced root type "object"`)
	}
	if _, ok := schema["properties"].(map[string]any); !ok {
		if _, present := schema["properties"]; present {
			changes = append(changes, "replaced a malformed root properties value")
		} else {
			changes = append(changes, "added empty root properties")
		}
		schema["properties"] = map[string]any{}
	}
	return schema, changes
}

// deepCopyJSON copies a decoded JSON value (maps, slices, scalars).
func deepCopyJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopyJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopyJSON(val)
		}
		return out
	default:
		return v
	}
}

// refInliner expands local references against the original root.
type refInliner struct {
	root       map[string]any // pristine copy the pointers resolve against
	sizes      map[string]int // marshalled size of each resolved target, by ref
	added      int            // bytes the copies made so far add
	overBudget bool           // a copy would pass maxInlinedBytes; stop
	inlined    bool
	cyclic     bool
	tooDeep    bool
	unresolved bool
	remote     bool
}

// inlineLocalRefs rewrites schema in place (it is already a private
// copy) and reports what changed. When inlining runs over budget,
// schema is restored to its original content.
func inlineLocalRefs(schema map[string]any) []string {
	if !containsRef(schema, 0) {
		return nil
	}
	root, _ := deepCopyJSON(schema).(map[string]any)
	r := &refInliner{root: root, sizes: map[string]int{}}
	// The root counts as being expanded: "#" inside it is recursion.
	stack := []string{"#"}
	if ref, ok := schema["$ref"].(string); ok {
		expanded, _ := r.expandRef(schema, ref, stack).(map[string]any)
		if !r.overBudget {
			clear(schema)
			maps.Copy(schema, expanded)
		}
	} else {
		r.walkSchema(schema, stack)
	}
	if r.overBudget {
		// Discard the partial work. root is never written to (targets
		// are copied out of it), so it still holds the original.
		clear(schema)
		maps.Copy(schema, root)
		return []string{refOverBudgetChange}
	}

	var changes []string
	if r.inlined {
		changes = append(changes, "inlined local $ref pointers")
		for _, key := range []string{"$defs", "definitions"} {
			if _, ok := schema[key]; ok {
				delete(schema, key)
				changes = append(changes, "removed "+key+" after inlining")
			}
		}
	}
	if r.cyclic {
		changes = append(changes, "replaced recursive $ref pointers with {}")
	}
	if r.tooDeep {
		changes = append(changes, "replaced $ref pointers beyond the nesting limit with {}")
	}
	if r.unresolved {
		changes = append(changes, "replaced unresolvable local $ref pointers with {}")
	}
	if r.remote {
		changes = append(changes, "left non-local $ref pointers in place")
	}
	return changes
}

// schemaDataKeys hold instance data, not subschemas: a "$ref" key under
// them is data and stays untouched.
var schemaDataKeys = map[string]bool{"const": true, "default": true, "enum": true, "examples": true}

// schemaMapKeys map names to subschemas.
var schemaMapKeys = map[string]bool{"properties": true, "patternProperties": true, "dependentSchemas": true}

// walkSchema expands every reference inside node (a schema object).
// stack holds the references being expanded on this path.
func (r *refInliner) walkSchema(node map[string]any, stack []string) {
	for key, child := range node {
		if r.overBudget {
			return
		}
		switch {
		case schemaDataKeys[key], key == "$defs", key == "definitions":
			// Data, or definitions that are dropped after inlining.
		case schemaMapKeys[key]:
			if m, ok := child.(map[string]any); ok {
				for name, sub := range m {
					m[name] = r.expandValue(sub, stack)
				}
			}
		default:
			node[key] = r.expandValue(child, stack)
		}
	}
}

// expandValue returns v with references expanded: a schema object is
// walked (or replaced when it is a reference), an array's elements are
// handled one by one, scalars pass through.
func (r *refInliner) expandValue(v any, stack []string) any {
	if r.overBudget {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			return r.expandRef(x, ref, stack)
		}
		r.walkSchema(x, stack)
		return x
	case []any:
		for i, el := range x {
			x[i] = r.expandValue(el, stack)
		}
		return x
	default:
		return v
	}
}

// expandRef replaces a reference node with a copy of its target merged
// under the node's sibling keywords (siblings win, as in JSON Schema
// 2020-12 where $ref applies alongside them).
func (r *refInliner) expandRef(node map[string]any, ref string, stack []string) any {
	if r.overBudget {
		return node
	}
	if !strings.HasPrefix(ref, "#") {
		r.remote = true
		r.walkSchema(node, stack)
		return node
	}
	siblings := make(map[string]any, len(node))
	for k, v := range node {
		if k != "$ref" {
			siblings[k] = v
		}
	}
	var replacement map[string]any
	switch {
	case containsString(stack, ref):
		r.cyclic = true
	case len(stack) > maxRefDepth:
		r.tooDeep = true
	default:
		target, ok := resolvePointer(r.root, ref)
		if !ok {
			r.unresolved = true
			break
		}
		if !r.charge(ref, target) {
			return node
		}
		r.inlined = true
		replacement, _ = deepCopyJSON(target).(map[string]any)
		if replacement == nil {
			// A boolean schema (true/false) or other non-object target.
			replacement = map[string]any{}
		}
		inner := append(append(make([]string, 0, len(stack)+1), stack...), ref)
		if innerRef, ok := replacement["$ref"].(string); ok {
			expanded, _ := r.expandRef(replacement, innerRef, inner).(map[string]any)
			replacement = expanded
		} else {
			r.walkSchema(replacement, inner)
		}
	}
	if replacement == nil {
		replacement = map[string]any{}
	}
	r.walkSchema(siblings, stack)
	for k, v := range siblings {
		replacement[k] = v
	}
	return replacement
}

// charge counts one copy of target (resolved from ref) against
// maxInlinedBytes and reports whether it fits. A copy that does not fit
// sets overBudget, which ends the inlining. Sizes are cached per ref,
// so each distinct target is marshalled once; every marshal either
// fits the budget or ends the walk, which bounds the total work.
func (r *refInliner) charge(ref string, target any) bool {
	size, ok := r.sizes[ref]
	if !ok {
		size = maxInlinedBytes + 1
		if b, err := json.Marshal(target); err == nil {
			size = len(b)
		}
		r.sizes[ref] = size
	}
	if r.added+size > maxInlinedBytes {
		r.overBudget = true
		return false
	}
	r.added += size
	return true
}

// resolvePointer follows a "#/a/b" JSON pointer (RFC 6901, inside a URI
// fragment) from root.
func resolvePointer(root map[string]any, ref string) (any, bool) {
	frag, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	if err != nil {
		return nil, false
	}
	if frag == "" {
		return root, true
	}
	if !strings.HasPrefix(frag, "/") {
		return nil, false // a plain-name anchor; not supported
	}
	var cur any = root
	for _, tok := range strings.Split(frag[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// containsRef reports whether any schema object under v has a string
// "$ref" (instance data excluded). Depth-bounded.
func containsRef(v any, depth int) bool {
	if depth > maxSchemaWalkDepth {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["$ref"].(string); ok {
			return true
		}
		for k, child := range x {
			if !schemaDataKeys[k] && containsRef(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, el := range x {
			if containsRef(el, depth+1) {
				return true
			}
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
