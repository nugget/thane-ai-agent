package mcp

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

func clientTSchema(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("schema %s: %v", raw, err)
	}
	return m
}

// TestClient_HeaderParamsValidation covers the six x-mcp-header
// constraints (streamable-http "Schema Extension").
func TestClient_HeaderParamsValidation(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		want    []string // "Name=path.to.prop"
		wantErr string
	}{
		{name: "no annotations", schema: `{"type":"object","properties":{"a":{"type":"string"}}}`},
		{name: "nil schema", schema: `null`},
		{name: "top-level property",
			schema: `{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}}`,
			want:   []string{"Region=region"}},
		{name: "nested properties chain",
			schema: `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"integer","x-mcp-header":"B"}}},"c":{"type":"boolean","x-mcp-header":"C-Flag"}}}`,
			want:   []string{"B=a.b", "C-Flag=c"}},
		{name: "annotation-like data is ignored",
			schema: `{"type":"object","properties":{"a":{"type":"object","default":{"x-mcp-header":"Nope"},"examples":[{"x-mcp-header":"Nope"}]}}}`},
		{name: "a property named x-mcp-header is not an annotation",
			schema: `{"type":"object","properties":{"x-mcp-header":{"type":"string"}}}`},
		{name: "empty value", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":""}}}`, wantErr: "empty"},
		{name: "non-string value", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":7}}}`, wantErr: "not a string"},
		{name: "not a token", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"Re gion"}}}`, wantErr: "token"},
		{name: "control character", schema: `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"A\r\nB"}}}`, wantErr: "token"},
		{name: "case-insensitive duplicate",
			schema:  `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"Region"},"b":{"type":"string","x-mcp-header":"REGION"}}}`,
			wantErr: "more than once"},
		{name: "number type", schema: `{"type":"object","properties":{"a":{"type":"number","x-mcp-header":"A"}}}`, wantErr: "type"},
		{name: "object type", schema: `{"type":"object","properties":{"a":{"type":"object","x-mcp-header":"A"}}}`, wantErr: "type"},
		{name: "missing type", schema: `{"type":"object","properties":{"a":{"x-mcp-header":"A"}}}`, wantErr: "type"},
		{name: "on the root", schema: `{"type":"object","x-mcp-header":"Root"}`, wantErr: "reachable"},
		{name: "through items",
			schema: `{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{"b":{"type":"string","x-mcp-header":"B"}}}}}}`, wantErr: "reachable"},
		{name: "through anyOf",
			schema: `{"type":"object","anyOf":[{"properties":{"b":{"type":"string","x-mcp-header":"B"}}}]}`, wantErr: "reachable"},
		{name: "through oneOf below a property",
			schema: `{"type":"object","properties":{"a":{"oneOf":[{"type":"string","x-mcp-header":"A"}]}}}`, wantErr: "reachable"},
		{name: "through if/then",
			schema: `{"type":"object","if":{"properties":{"a":{"type":"string"}}},"then":{"properties":{"a":{"type":"string","x-mcp-header":"A"}}}}`, wantErr: "reachable"},
		{name: "inside $defs",
			schema: `{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"string","x-mcp-header":"A"}}}`, wantErr: "reachable"},
		{name: "inside additionalProperties",
			schema: `{"type":"object","additionalProperties":{"type":"string","x-mcp-header":"A"}}`, wantErr: "reachable"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			params, err := toolHeaderParams(clientTSchema(t, tt.schema))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range params {
				got = append(got, p.name+"="+strings.Join(p.path, "."))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("params = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClient_HeaderValues(t *testing.T) {
	params := []headerParam{
		{name: "S", path: []string{"s"}},
		{name: "I", path: []string{"i"}},
		{name: "Neg", path: []string{"neg"}},
		{name: "Big", path: []string{"big"}},
		{name: "B", path: []string{"b"}},
		{name: "N", path: []string{"nested", "n"}},
		{name: "Null", path: []string{"null"}},
		{name: "Absent", path: []string{"absent"}},
		{name: "NotObject", path: []string{"s", "deeper"}},
		{name: "Num", path: []string{"num"}},
		{name: "Int", path: []string{"int"}},
		{name: "Obj", path: []string{"obj"}},
	}
	args := map[string]any{
		"s":      "Hello, 世界",
		"i":      float64(42),
		"neg":    float64(-7),
		"big":    float64(9007199254740991),
		"b":      false,
		"nested": map[string]any{"n": "x"},
		"null":   nil,
		"num":    json.Number("12"),
		"int":    5,
		"obj":    map[string]any{"k": 1},
	}
	want := map[string]string{
		"S": "Hello, 世界", "I": "42", "Neg": "-7", "Big": "9007199254740991", "B": "false", "N": "x",
		"Num": "12", "Int": "5", "Obj": `{"k":1}`,
	}
	got := headerValues(params, args)
	if !maps.Equal(got, want) {
		t.Errorf("headerValues = %v, want %v", got, want)
	}
	if headerValues(nil, args) != nil {
		t.Error("no params should yield no headers")
	}
}
