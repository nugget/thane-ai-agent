package homeassistant

import (
	"path"
	"strings"
)

// IsEntityGlob reports whether s is a glob pattern rather than a concrete
// entity_id. Home Assistant entity IDs are domain.object_id over
// [a-z0-9_] and never contain glob metacharacters, so the presence of
// '*', '?', or '[' unambiguously marks a pattern. Callers use this to
// decide whether an entity-subscription target should be expanded against
// live entities or treated as a single concrete entity.
func IsEntityGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

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

// ValidateEntityTarget validates a subscription or tool target that may
// be either a concrete entity_id or a glob. Concrete IDs always pass; a
// glob must be well-formed. An empty string passes (callers enforce
// required-ness separately). This is the one call subscription-creation
// surfaces make so a malformed glob is rejected up front instead of
// silently never matching at render time.
func ValidateEntityTarget(s string) error {
	if IsEntityGlob(s) {
		return ValidateEntityGlob(s)
	}
	return nil
}
