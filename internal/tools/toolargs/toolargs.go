// Package toolargs provides the canonical translators for extracting
// typed values from a tool handler's decoded JSON argument map
// (map[string]any). Tool calls arrive as JSON, so numbers land as
// float64, booleans as bool, and the model occasionally encodes a
// number or boolean as a string. These helpers centralize that
// coercion in one place.
//
// Before this package, three tool packages each carried their own
// prefixed copies of the same logic — email's stringArg/intArg/...,
// the loop tools' ldStringArg/ldIntArg, and the model-registry tools'
// mrStringArg/mrIntArg/... — prefixed only because they would
// otherwise collide. The prefixes were a workaround for the missing
// shared home; subtle coercion differences between the copies were a
// standing drift risk. This package is that home.
//
// The package is deliberately named toolargs rather than args: tool
// handlers conventionally name their argument map `args map[string]any`,
// and a package named `args` would be shadowed at almost every call
// site. `toolargs.String(args, key)` reads cleanly with no alias.
//
// All getters are nil-safe: a nil map, an absent key, or a wrong-typed
// value yields the zero value (or the supplied fallback). The *OK
// variants distinguish "absent / uncoercible" from "present and zero."
package toolargs

import (
	"encoding/json"
	"strconv"
	"strings"
)

// maxUint32 is the largest valid uint32 value, used for range checks
// in Uint32Slice.
const maxUint32 = 1<<32 - 1

// String returns the string value at key, or "" when the key is absent
// or not a string. The value is returned verbatim — use TrimmedString
// when surrounding whitespace should be stripped.
func String(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// TrimmedString returns String(args, key) with leading and trailing
// whitespace removed. Matches the loop tools' historical ldStringArg
// semantics, where trimming guarded against models padding identifiers.
func TrimmedString(args map[string]any, key string) string {
	return strings.TrimSpace(String(args, key))
}

// Int returns the integer value at key, or 0 when the key is absent or
// not coercible. Equivalent to IntOr(args, key, 0); see IntOK for the
// coercion rules.
func Int(args map[string]any, key string) int {
	return IntOr(args, key, 0)
}

// IntOr returns the integer value at key, or fallback when the key is
// absent or not coercible.
func IntOr(args map[string]any, key string, fallback int) int {
	if v, ok := IntOK(args, key); ok {
		return v
	}
	return fallback
}

// IntOK returns the integer value at key and whether it was present and
// coercible. Coercion accepts Go int/int32/int64, JSON float64 and
// json.Number (rejecting non-integral values), and decimal strings
// (trimmed). A nil map, absent key, nil value, fractional number, or
// unparseable string returns (0, false).
func IntOK(args map[string]any, key string) (int, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), true
		}
		// Decimal-form integral values (e.g. "9.0") fail Int64 but still
		// satisfy the integer contract; parse as float and apply the
		// same integrality check as the float64 case.
		if f, err := v.Float64(); err == nil && f == float64(int(f)) {
			return int(f), true
		}
		return 0, false
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// Bool returns the boolean value at key, or false when the key is
// absent or not coercible. Equivalent to BoolOr(args, key, false).
//
// Use BoolOr for any argument whose schema documents a non-false
// default: models routinely omit a documented `default: true` field
// rather than sending it explicitly, and the zero value for the missing
// key (false) is the opposite of what the schema promised (the #930
// email_mark regression). BoolOr returns the documented fallback when
// the key is absent, so runtime behavior matches the schema regardless
// of how the model encodes the call.
func Bool(args map[string]any, key string) bool {
	return BoolOr(args, key, false)
}

// BoolOr returns the boolean value at key, or fallback when the key is
// absent or not coercible. See Bool for when to prefer this.
func BoolOr(args map[string]any, key string, fallback bool) bool {
	if v, ok := BoolOK(args, key); ok {
		return v
	}
	return fallback
}

// BoolOK returns the boolean value at key and whether it was present
// and coercible. Coercion accepts a real bool, or the strings "true"/
// "false" (case-insensitive, trimmed). Anything else returns
// (false, false).
func BoolOK(args map[string]any, key string) (bool, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// HasBool reports whether key holds a present, coercible boolean. Use
// it to distinguish "the caller set this flag" from "the flag defaulted
// to false."
func HasBool(args map[string]any, key string) bool {
	_, ok := BoolOK(args, key)
	return ok
}

// StringSlice returns the value at key as a []string. A JSON array
// ([]any) yields its string elements (non-string elements are skipped);
// a bare non-empty string yields a single-element slice. Anything else
// yields nil.
func StringSlice(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, el := range v {
			if s, ok := el.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}

// Uint32Slice returns the value at key as a []uint32. A JSON array
// ([]any) yields its coercible elements; a bare value yields a
// single-element slice when coercible. Elements may be float64, int, or
// decimal strings; values that are nil, fractional, below 1, or above
// the uint32 range are skipped. Anything uncoercible yields nil.
func Uint32Slice(args map[string]any, key string) []uint32 {
	switch v := args[key].(type) {
	case []any:
		var out []uint32
		for _, el := range v {
			if n, ok := coerceUint32(el); ok {
				out = append(out, n)
			}
		}
		return out
	default:
		if n, ok := coerceUint32(v); ok {
			return []uint32{n}
		}
	}
	return nil
}

// Uint32 returns the value at key as a uint32, or 0 when the key is
// absent or not coercible to a valid identifier. Equivalent to the
// value of Uint32OK; see Uint32OK for the coercion rules.
func Uint32(args map[string]any, key string) uint32 {
	n, _ := Uint32OK(args, key)
	return n
}

// Uint32OK returns the value at key as a uint32 and whether it was
// present and coercible. It is the single-value form of Uint32Slice's
// element coercion: accepts float64, Go int, and decimal strings
// (trimmed), and rejects values that are nil, negative, fractional,
// below 1, or above the uint32 range (returning (0, false)).
//
// Prefer this over uint32(Int(args, key)) for identifier arguments
// (e.g. an IMAP UID): the signed-int cast silently wraps a negative or
// out-of-range value into a valid-looking large ID, whereas Uint32OK
// rejects it so an absent/garbage value reads as "not provided."
func Uint32OK(args map[string]any, key string) (uint32, bool) {
	return coerceUint32(args[key])
}

// coerceUint32 converts a single value to a valid uint32 in [1,
// maxUint32]. Returns (0, false) for nil, negative, fractional, or
// out-of-range values.
func coerceUint32(v any) (uint32, bool) {
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) || n < 1 || n > float64(maxUint32) {
			return 0, false
		}
		return uint32(n), true
	case int:
		if n < 1 || int64(n) > int64(maxUint32) {
			return 0, false
		}
		return uint32(n), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil || parsed < 1 || int64(parsed) > int64(maxUint32) {
			return 0, false
		}
		return uint32(parsed), true
	}
	return 0, false
}
