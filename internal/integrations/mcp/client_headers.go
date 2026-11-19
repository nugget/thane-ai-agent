package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// headerAnnotationKey is the inputSchema extension that mirrors a tool
// argument into an Mcp-Param-{name} header (modern Streamable HTTP).
const headerAnnotationKey = "x-mcp-header"

// maxSchemaWalkDepth bounds the schema walk; deeper nodes are not
// inspected.
const maxSchemaWalkDepth = 64

// headerParam is one validated x-mcp-header annotation.
type headerParam struct {
	name string   // annotation value, e.g. "Region" (header Mcp-Param-Region)
	path []string // chain of properties keys from the schema root
}

// toolHeaderParams validates every x-mcp-header annotation in schema
// (streamable-http "Schema Extension", six constraints) and returns the
// valid ones sorted by name. Any invalid annotation invalidates the
// whole tool: the value must be a non-empty RFC 9110 token (which also
// excludes control characters), unique case-insensitively, on a
// property of type integer, string or boolean, reachable from the root
// through properties keys only (never items, composition, conditional
// keywords or $ref).
func toolHeaderParams(schema map[string]any) ([]headerParam, error) {
	w := headerWalker{names: map[string]struct{}{}}
	if err := w.walk(schema, nil, true, 0); err != nil {
		return nil, err
	}
	sort.Slice(w.params, func(i, j int) bool { return w.params[i].name < w.params[j].name })
	return w.params, nil
}

type headerWalker struct {
	params []headerParam
	names  map[string]struct{} // lower-cased annotation values seen
}

// walk inspects one schema node. reachable says whether node is the
// root or reached from it through properties keys only; path is the
// property chain (nil for the root and unreachable nodes).
func (w *headerWalker) walk(node map[string]any, path []string, reachable bool, depth int) error {
	if depth > maxSchemaWalkDepth {
		return nil
	}
	if v, ok := node[headerAnnotationKey]; ok {
		if err := w.annotation(node, v, path, reachable && path != nil); err != nil {
			return err
		}
	}
	for key, child := range node {
		switch key {
		case headerAnnotationKey, "const", "default", "enum", "examples":
			// Annotation handled above; the rest are instance data.
		case "properties":
			props, ok := child.(map[string]any)
			if !ok {
				continue
			}
			for name, sub := range props {
				m, ok := sub.(map[string]any)
				if !ok {
					continue
				}
				var subPath []string
				if reachable {
					subPath = append(append(make([]string, 0, len(path)+1), path...), name)
				}
				if err := w.walk(m, subPath, reachable, depth+1); err != nil {
					return err
				}
			}
		case "$defs", "definitions", "patternProperties", "dependentSchemas":
			// name → schema maps; never statically reachable.
			if defs, ok := child.(map[string]any); ok {
				for _, sub := range defs {
					if err := w.walkAny(sub, depth+1); err != nil {
						return err
					}
				}
			}
		default:
			if err := w.walkAny(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkAny inspects nested schemas that are not statically reachable.
func (w *headerWalker) walkAny(v any, depth int) error {
	switch x := v.(type) {
	case map[string]any:
		return w.walk(x, nil, false, depth)
	case []any:
		for _, item := range x {
			if err := w.walkAny(item, depth); err != nil {
				return err
			}
		}
	}
	return nil
}

// annotation validates one x-mcp-header value found on node.
func (w *headerWalker) annotation(node map[string]any, v any, path []string, reachable bool) error {
	name, ok := v.(string)
	switch {
	case !ok:
		return errors.New("x-mcp-header value is not a string")
	case name == "":
		return errors.New("x-mcp-header value is empty")
	case !isHeaderToken(name):
		return fmt.Errorf("x-mcp-header %q is not a valid header name token", logText(name))
	case !reachable:
		return fmt.Errorf("x-mcp-header %q is not on a property reachable through properties only", logText(name))
	}
	switch typ, _ := node["type"].(string); typ {
	case "string", "integer", "boolean":
	default:
		return fmt.Errorf("x-mcp-header %q is on a property of type %q; only string, integer and boolean are allowed",
			logText(name), logText(typ))
	}
	lower := strings.ToLower(name)
	if _, dup := w.names[lower]; dup {
		return fmt.Errorf("x-mcp-header %q is used more than once", logText(name))
	}
	w.names[lower] = struct{}{}
	w.params = append(w.params, headerParam{name: name, path: path})
	return nil
}

// headerValues extracts the Mcp-Param-* values for a tools/call: the
// argument at each parameter's exact property path, rendered per the
// value-encoding rules (strings as-is, integers in decimal, booleans
// lowercase). Absent and null arguments are omitted. The values are raw;
// the transport applies encodeHeaderValue.
func headerValues(params []headerParam, args map[string]any) map[string]string {
	var out map[string]string
	for _, p := range params {
		v, ok := argumentAt(args, p.path)
		if !ok || v == nil {
			continue
		}
		s, ok := headerArgString(v)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(params))
		}
		out[p.name] = s
	}
	return out
}

// argumentAt walks args along path.
func argumentAt(args map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	cur := args
	for i, key := range path {
		v, ok := cur[key]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return v, true
		}
		if cur, ok = v.(map[string]any); !ok {
			return nil, false
		}
	}
	return nil, false
}

// headerArgString renders one argument value as a header value.
func headerArgString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case json.Number:
		return x.String(), true
	default:
		// A mismatched value (object, array); the server decides. Send
		// its JSON so the header still mirrors the body.
		b, err := json.Marshal(x)
		return string(b), err == nil
	}
}
