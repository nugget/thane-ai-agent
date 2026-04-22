package promptfmt

import (
	"encoding/json"
	"fmt"
)

// MarshalCompact returns v as a single-line JSON string with no extra
// whitespace. On marshal failure it falls back to a fmt.Sprintf %v
// rendering so the prompt path always produces *some* text; forensic
// records and tests can still detect the failure mode by matching on
// the absence of JSON delimiters.
func MarshalCompact(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}
