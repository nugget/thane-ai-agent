package promptfmt

import (
	"encoding/json"
	"fmt"
	"strings"
)

// marshalErrorPrefix is the stable sentinel stamped on the output when
// json.Marshal fails. Callers (tests, forensic log scanners) should
// detect marshal failures via this prefix — earlier versions relied on
// "output doesn't look like JSON," which broke for struct values
// because fmt's %v verb prints structs with braces.
const marshalErrorPrefix = "[json-marshal-error"

// MarshalCompact returns v as a single-line JSON string with no extra
// whitespace. On marshal failure it falls back to a sentinel-prefixed
// fmt.Sprintf rendering so the prompt path always produces text, and
// downstream code can detect the failure mode unambiguously via
// [MarshalErrorPrefix].
func MarshalCompact(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%s: %v] %v", marshalErrorPrefix, err, v)
	}
	return string(data)
}

// HasMarshalError reports whether s was produced by [MarshalCompact]'s
// error-fallback path. Use this instead of matching on output shape.
func HasMarshalError(s string) bool {
	return strings.HasPrefix(s, marshalErrorPrefix)
}
