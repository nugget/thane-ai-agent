package promptfmt

import (
	"strconv"
	"strings"
)

// FormatNumber renders an integer with thousands separators
// (e.g., 200000 → "200,000", -1234567 → "-1,234,567"). Used in prompt
// lines where a raw digit run is hard to scan at a glance (token counts,
// byte totals).
//
// Negative numbers keep the sign outside the grouping so the comma logic
// doesn't see '-' as a digit. Using strconv.Itoa + string-level sign
// stripping instead of arithmetic negation avoids the math.MinInt
// overflow edge case.
func FormatNumber(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	if len(s) <= 3 {
		return sign + s
	}
	var sb strings.Builder
	sb.WriteString(sign)
	remainder := len(s) % 3
	if remainder > 0 {
		sb.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if sb.Len() > len(sign) {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}
