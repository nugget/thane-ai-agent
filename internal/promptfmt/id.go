package promptfmt

const shortIDLen = 8

// ShortIDPrefix returns the first 8 runes of id for compact display. It
// is rune-aware so multi-byte characters are not cut in half. Use this
// when the *prefix* of an ID is the useful slice (e.g., a UUIDv7 loop ID
// where the time-ordered prefix is what operators read first).
//
// Returns id unchanged when the rune count is already within the limit.
func ShortIDPrefix(id string) string {
	runes := []rune(id)
	if len(runes) > shortIDLen {
		return string(runes[:shortIDLen])
	}
	return id
}

// ShortIDSuffix returns the last 8 bytes of id for compact display. Use
// this when the *suffix* of an ID is the most discriminating part (e.g.,
// long hashes where the tail is what disambiguates entries in a log).
//
// Returns id unchanged when it is already 8 bytes or shorter. Byte-based
// rather than rune-based because callers only invoke this on ASCII IDs.
func ShortIDSuffix(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[len(id)-shortIDLen:]
}
