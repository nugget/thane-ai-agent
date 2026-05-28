package toolargs_test

import (
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// A tool handler receives its decoded JSON arguments as a
// map[string]any. toolargs extracts typed values from that map,
// coercing the shapes a model/JSON call tends to produce.

func ExampleString() {
	args := map[string]any{"entity_id": "light.office"}
	fmt.Println(toolargs.String(args, "entity_id"))
	fmt.Printf("%q\n", toolargs.String(args, "missing"))
	// Output:
	// light.office
	// ""
}

func ExampleInt() {
	// JSON numbers decode to float64; a model may also send a numeric
	// string. Both coerce; an absent key yields 0.
	fmt.Println(toolargs.Int(map[string]any{"limit": float64(25)}, "limit"))
	fmt.Println(toolargs.Int(map[string]any{"limit": "25"}, "limit"))
	fmt.Println(toolargs.Int(map[string]any{}, "limit"))
	// Output:
	// 25
	// 25
	// 0
}

func ExampleBoolOr() {
	// BoolOr returns the documented fallback when the key is absent —
	// the fix for models that omit a schema's `default: true` field
	// rather than sending it explicitly.
	fmt.Println(toolargs.BoolOr(map[string]any{}, "include_read", true))
	fmt.Println(toolargs.BoolOr(map[string]any{"include_read": false}, "include_read", true))
	// Output:
	// true
	// false
}

func ExampleStringSlice() {
	// A JSON array or a bare string both yield a []string.
	fmt.Println(toolargs.StringSlice(map[string]any{"tags": []any{"a", "b"}}, "tags"))
	fmt.Println(toolargs.StringSlice(map[string]any{"tags": "solo"}, "tags"))
	// Output:
	// [a b]
	// [solo]
}
