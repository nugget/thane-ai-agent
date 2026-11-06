package providers

import (
	"bytes"
	"encoding/json"
)

// deepCopyJSONValue returns v as the plain JSON data it encodes to:
// objects become map[string]any, arrays []any, and numbers
// json.Number, which keeps each number's literal text. Going through
// the encoder is what makes the copy share nothing with v. A typed
// slice or map, state a custom MarshalJSON reads from unexported
// fields, and anything reachable only through fields JSON ignores all
// stay behind, and the last is never visited at all, so a
// self-reference behind json:"-" cannot recurse.
//
// The copy encodes to the same JSON value as v. Only key order can
// differ, and only for a struct: its fields encode in declaration
// order, while the copy is a map and encodes with sorted keys.
//
// A value that cannot be encoded is returned unchanged. Every request
// built from it fails to encode anyway, so no request body, digest or
// observer ever sees the shared value.
func deepCopyJSONValue(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return v
	}
	return out
}
