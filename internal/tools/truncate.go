package tools

import "unicode/utf8"

// truncateUTF8 truncates s to at most maxBytes, ensuring the result is
// valid UTF-8. The cut respects rune boundaries by stepping back from
// maxBytes until it lands on a leading byte. Used by tool handlers
// that need to clip oversized output to a byte ceiling without
// emitting an invalid UTF-8 prefix.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
