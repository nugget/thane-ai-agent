package promptfmt

import (
	"fmt"
	"strings"
)

// FormatNumber renders an integer with thousands separators
// (e.g., 200000 → "200,000"). Used in prompt lines where a raw digit
// run is hard to scan at a glance (token counts, byte totals).
func FormatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var sb strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		sb.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}
