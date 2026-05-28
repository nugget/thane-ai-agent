package homeassistant

import "path"

// MatchEntityGlob reports whether entityID matches the glob pattern.
// Patterns use [path.Match] syntax — '*' matches any run of characters.
// Entity IDs contain no '/' separators, so '*' spans the domain dot
// freely: "binary_sensor.*door*" matches "binary_sensor.front_door" and
// "*_temperature" spans domains. Returns an error only for a malformed
// pattern (see [ValidateEntityGlob]).
//
// This is the single entity-glob primitive shared across the codebase:
// [EntityFilter] (the state-watch stream), the ha_list_entities tool,
// entity-subscription expansion, and config validation all route through
// it so the glob contract can't drift between surfaces.
func MatchEntityGlob(pattern, entityID string) (bool, error) {
	return path.Match(pattern, entityID)
}

// ValidateEntityGlob reports whether pattern is a well-formed entity
// glob. Use it at tool and config boundaries to reject malformed
// patterns up front, rather than letting them silently fail to match.
func ValidateEntityGlob(pattern string) error {
	// path.Match only inspects the pattern for ErrBadPattern; the
	// subject string is irrelevant, so a representative entity_id shape
	// is enough to surface a syntax error.
	_, err := path.Match(pattern, "domain.entity")
	return err
}
