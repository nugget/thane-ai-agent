package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
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

func TestBridge_NormalizeInputSchemaBoundsExpansion(t *testing.T) {
	t.Parallel()
	// Each level references the next one twice: unbounded inlining would
	// grow as 2^depth.
	defs := map[string]any{}
	const levels = 40
	for i := range levels {
		next := map[string]any{"type": "string"}
		if i+1 < levels {
			ref := map[string]any{"$ref": "#/$defs/L" + wireTItoa(i+1)}
			next = map[string]any{"type": "object", "properties": map[string]any{"a": ref, "b": ref}}
		}
		defs["L"+wireTItoa(i)] = next
	}
	in := map[string]any{"type": "object", "properties": map[string]any{"root": map[string]any{"$ref": "#/$defs/L0"}}, "$defs": defs}
	got, changes := normalizeInputSchema(in)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 1<<20 {
		t.Fatalf("expanded schema is %d bytes; expansion is not bounded", len(b))
	}
	if !strings.Contains(strings.Join(changes, " "), "expansion limit") {
		t.Errorf("changes %q do not report the expansion limit", changes)
	}
}

func wireTItoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
