package mcp

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// wireTSchema decodes a JSON schema literal.
func wireTSchema(t *testing.T, s string) map[string]any {
	t.Helper()
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad schema literal %s: %v", s, err)
	}
	return m
}

func TestBridge_NormalizeInputSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		changes []string // substrings; nil = no changes
	}{
		{
			name: "already fine",
			in:   `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`,
			want: `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`,
		},
		{
			name:    "nil schema",
			in:      ``,
			want:    `{"type":"object","properties":{}}`,
			changes: []string{"missing input schema"},
		},
		{
			name:    "missing type and properties",
			in:      `{}`,
			want:    `{"type":"object","properties":{}}`,
			changes: []string{`added root type`, "added empty root properties"},
		},
		{
			name:    "non-object root type",
			in:      `{"type":"string","properties":{}}`,
			want:    `{"type":"object","properties":{}}`,
			changes: []string{`forced root type`},
		},
		{
			name:    "malformed properties",
			in:      `{"type":"object","properties":[]}`,
			want:    `{"type":"object","properties":{}}`,
			changes: []string{"malformed root properties"},
		},
		{
			name:    "top-level anyOf merged",
			in:      `{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"b":{"type":"integer"}}}]}`,
			want:    `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`,
			changes: []string{"merged top-level anyOf"},
		},
		{
			name:    "$defs inlined with sibling keywords",
			in:      `{"type":"object","properties":{"loc":{"$ref":"#/$defs/Loc","description":"where"}},"$defs":{"Loc":{"type":"object","properties":{"lat":{"type":"number"}},"description":"a place"}}}`,
			want:    `{"type":"object","properties":{"loc":{"type":"object","properties":{"lat":{"type":"number"}},"description":"where"}}}`,
			changes: []string{"inlined local $ref", "removed $defs"},
		},
		{
			name:    "definitions and nested refs",
			in:      `{"type":"object","properties":{"list":{"type":"array","items":{"$ref":"#/definitions/Item"}}},"definitions":{"Item":{"type":"object","properties":{"tag":{"$ref":"#/definitions/Tag"}}},"Tag":{"type":"string","enum":["a","b"]}}}`,
			want:    `{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","properties":{"tag":{"type":"string","enum":["a","b"]}}}}}}`,
			changes: []string{"inlined local $ref", "removed definitions"},
		},
		{
			name:    "root $ref",
			in:      `{"$ref":"#/$defs/Args","$defs":{"Args":{"type":"object","properties":{"x":{"type":"boolean"}}}}}`,
			want:    `{"type":"object","properties":{"x":{"type":"boolean"}}}`,
			changes: []string{"inlined local $ref", "removed $defs"},
		},
		{
			name:    "recursive ref cut",
			in:      `{"type":"object","properties":{"node":{"$ref":"#/$defs/Node"}},"$defs":{"Node":{"type":"object","properties":{"child":{"$ref":"#/$defs/Node"}}}}}`,
			want:    `{"type":"object","properties":{"node":{"type":"object","properties":{"child":{}}}}}`,
			changes: []string{"inlined local $ref", "recursive $ref"},
		},
		{
			name:    "self reference to root",
			in:      `{"type":"object","properties":{"sub":{"$ref":"#","description":"same shape"}}}`,
			want:    `{"type":"object","properties":{"sub":{"description":"same shape"}}}`,
			changes: []string{"recursive $ref"},
		},
		{
			name:    "unresolvable local ref",
			in:      `{"type":"object","properties":{"x":{"$ref":"#/$defs/Missing"}}}`,
			want:    `{"type":"object","properties":{"x":{}}}`,
			changes: []string{"unresolvable local $ref"},
		},
		{
			name:    "remote ref left alone",
			in:      `{"type":"object","properties":{"x":{"$ref":"https://example.com/s.json"}}}`,
			want:    `{"type":"object","properties":{"x":{"$ref":"https://example.com/s.json"}}}`,
			changes: []string{"non-local $ref"},
		},
		{
			name: "ref inside instance data untouched",
			in:   `{"type":"object","properties":{"x":{"type":"object","default":{"$ref":"#/nowhere"}}}}`,
			want: `{"type":"object","properties":{"x":{"type":"object","default":{"$ref":"#/nowhere"}}}}`,
		},
		{
			name:    "property named $ref is a property",
			in:      `{"type":"object","properties":{"$ref":{"$ref":"#/$defs/S"}},"$defs":{"S":{"type":"string"}}}`,
			want:    `{"type":"object","properties":{"$ref":{"type":"string"}}}`,
			changes: []string{"inlined local $ref"},
		},
		{
			name:    "escaped pointer tokens",
			in:      `{"type":"object","properties":{"x":{"$ref":"#/$defs/a~1b~0c"}},"$defs":{"a/b~c":{"type":"integer"}}}`,
			want:    `{"type":"object","properties":{"x":{"type":"integer"}}}`,
			changes: []string{"inlined local $ref"},
		},
		{
			name:    "boolean schema target",
			in:      `{"type":"object","properties":{"x":{"$ref":"#/$defs/Any"}},"$defs":{"Any":true}}`,
			want:    `{"type":"object","properties":{"x":{}}}`,
			changes: []string{"inlined local $ref"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := wireTSchema(t, tt.in)
			before, _ := json.Marshal(in)
			got, changes := normalizeInputSchema(in)
			if after, _ := json.Marshal(in); string(after) != string(before) {
				t.Fatalf("input mutated:\nbefore %s\nafter  %s", before, after)
			}
			if want := wireTSchema(t, tt.want); !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				t.Fatalf("schema =\n%s\nwant\n%s", gotJSON, tt.want)
			}
			if tt.changes == nil && len(changes) > 0 {
				t.Fatalf("changes = %q, want none", changes)
			}
			joined := strings.Join(changes, " | ")
			for _, c := range tt.changes {
				if !strings.Contains(joined, c) {
					t.Errorf("changes %q missing %q", changes, c)
				}
			}
		})
	}
}

// wireTMarshalLen returns the size of v as JSON.
func wireTMarshalLen(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// wireTFanOutSchema returns a schema whose $defs.def is referenced by
// nprops properties.
func wireTFanOutSchema(def map[string]any, nprops int) map[string]any {
	props := map[string]any{}
	for i := range nprops {
		props["p"+strconv.Itoa(i)] = map[string]any{"$ref": "#/$defs/def"}
	}
	return map[string]any{"type": "object", "properties": props, "$defs": map[string]any{"def": def}}
}

// wireTCheckLeftInPlace asserts that normalizeInputSchema abandoned
// inlining: the output equals the input and reports only that.
func wireTCheckLeftInPlace(t *testing.T, in, got map[string]any, changes []string) {
	t.Helper()
	if n, limit := wireTMarshalLen(t, got), wireTMarshalLen(t, in)+maxInlinedBytes; n > limit {
		t.Fatalf("normalized schema is %d bytes, more than input + maxInlinedBytes = %d", n, limit)
	}
	if !reflect.DeepEqual(got, in) {
		t.Error("an over-budget schema should keep its $ref pointers and $defs unchanged")
	}
	if !slices.Equal(changes, []string{refOverBudgetChange}) {
		t.Errorf("changes = %q, want only %q", changes, refOverBudgetChange)
	}
}

func TestBridge_NormalizeInputSchemaBoundsExpansion(t *testing.T) {
	t.Parallel()
	// Each level references the next one eight times: unbounded inlining
	// would grow as 8^depth.
	defs := map[string]any{}
	const levels, fanOut = 20, 8
	for i := range levels {
		next := map[string]any{"type": "string"}
		if i+1 < levels {
			props := map[string]any{}
			for j := range fanOut {
				props["f"+strconv.Itoa(j)] = map[string]any{"$ref": "#/$defs/L" + strconv.Itoa(i+1)}
			}
			next = map[string]any{"type": "object", "description": strings.Repeat("d", 100), "properties": props}
		}
		defs["L"+strconv.Itoa(i)] = next
	}
	in := map[string]any{"type": "object", "properties": map[string]any{"root": map[string]any{"$ref": "#/$defs/L0"}}, "$defs": defs}
	start := time.Now()
	got, changes := normalizeInputSchema(in)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("normalizing took %v; expansion work is not bounded", elapsed)
	}
	wireTCheckLeftInPlace(t, in, got, changes)
}

// TestBridge_NormalizeInputSchemaFanOutStaysWithinBudget covers a
// benign schema whose one large definition is referenced by many
// properties: inlining it everywhere would grow it about 300 times.
func TestBridge_NormalizeInputSchemaFanOutStaysWithinBudget(t *testing.T) {
	t.Parallel()
	in := wireTFanOutSchema(map[string]any{"type": "string", "description": strings.Repeat("x", 20_000)}, 300)
	got, changes := normalizeInputSchema(in)
	wireTCheckLeftInPlace(t, in, got, changes)
	if _, ok := got["$defs"]; !ok {
		t.Error("$defs removed although the $ref pointers were kept")
	}
	if p, _ := got["properties"].(map[string]any)["p7"].(map[string]any); p["$ref"] != "#/$defs/def" {
		t.Errorf("properties.p7 = %v, want its $ref kept", p)
	}
}

// TestBridge_NormalizeInputSchemaRootRefOverBudget restores a schema
// whose root is itself a reference to an oversized definition.
func TestBridge_NormalizeInputSchemaRootRefOverBudget(t *testing.T) {
	t.Parallel()
	in := map[string]any{
		"$ref": "#/$defs/Args",
		"$defs": map[string]any{"Args": map[string]any{
			"type": "object", "description": strings.Repeat("x", maxInlinedBytes),
			"properties": map[string]any{"q": map[string]any{"type": "string"}},
		}},
	}
	got, changes := normalizeInputSchema(in)
	if got["$ref"] != "#/$defs/Args" || got["$defs"] == nil {
		t.Fatalf("root reference not kept: %v", got)
	}
	if got["type"] != "object" {
		t.Errorf("root type = %v, want object", got["type"])
	}
	if !slices.Contains(changes, refOverBudgetChange) {
		t.Errorf("changes = %q, want %q", changes, refOverBudgetChange)
	}
}

// TestBridge_NormalizeInputSchemaInlinesManySmallRefs inlines a small
// definition referenced by hundreds of properties, which fits the byte
// budget however many references there are.
func TestBridge_NormalizeInputSchemaInlinesManySmallRefs(t *testing.T) {
	t.Parallel()
	const nprops = 400
	in := wireTFanOutSchema(map[string]any{"type": "string", "format": "uuid"}, nprops)
	got, changes := normalizeInputSchema(in)
	if _, ok := got["$defs"]; ok {
		t.Error("$defs kept after inlining")
	}
	props, _ := got["properties"].(map[string]any)
	want := map[string]any{"type": "string", "format": "uuid"}
	for i := range nprops {
		name := "p" + strconv.Itoa(i)
		if !reflect.DeepEqual(props[name], want) {
			t.Fatalf("properties.%s = %v, want %v", name, props[name], want)
		}
	}
	if !slices.Equal(changes, []string{"inlined local $ref pointers", "removed $defs after inlining"}) {
		t.Errorf("changes = %q", changes)
	}
}
