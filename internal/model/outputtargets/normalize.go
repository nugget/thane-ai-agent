package outputtargets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Payload is a validated slot set ready for a sink to publish.
type Payload struct {
	// State is the primary slot's value: the single scalar the
	// consuming surface reads without an attribute lookup.
	State string `json:"state"`
	// Attributes holds every other supplied slot, keyed by slot name.
	// Text and color slots carry strings; fraction slots carry float64.
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Normalize validates raw tool arguments against the target's slots and
// returns the payload to publish.
//
// It rejects rather than repairs: an unknown key, a missing required
// slot, an over-budget string, an out-of-range fraction, or a malformed
// color each return an error naming the slot and the constraint. The only
// values it changes are surrounding whitespace (trimmed) and color hex
// casing (upper, with a leading "#"), neither of which changes meaning.
//
// Omitting an optional slot is how a caller clears it — Normalize does
// not carry values forward from a previous payload, because the tool
// contract is a full replacement.
func (t Target) Normalize(args map[string]any) (Payload, error) {
	if err := t.rejectUnknownSlots(args); err != nil {
		return Payload{}, err
	}

	payload := Payload{Attributes: make(map[string]any, len(t.Slots))}
	for _, slot := range t.Slots {
		raw, present := args[slot.Name]
		// An explicit JSON null is how some model families spell "leave
		// this one out"; treat it as absence rather than a type error.
		if raw == nil {
			present = false
		}
		if !present {
			if slot.Required {
				return Payload{}, fmt.Errorf("slot %q is required for target %q but was not supplied; %s", slot.Name, t.ID, slot.Description)
			}
			continue
		}

		// Each kind is handled in its own branch rather than through a
		// common any-returning helper: only text slots can be primary
		// or blank-and-therefore-cleared, and routing every kind through
		// one signature would mean type-asserting that back out.
		switch slot.Kind {
		case SlotKindText:
			text, err := slot.normalizeText(raw)
			if err != nil {
				return Payload{}, fmt.Errorf("slot %q: %w", slot.Name, err)
			}
			if text == "" {
				if slot.Required {
					return Payload{}, fmt.Errorf("slot %q is required for target %q but was empty after trimming whitespace", slot.Name, t.ID)
				}
				// An optional slot explicitly set to blank means "clear
				// it", which is the same as omitting it.
				continue
			}
			if slot.Primary {
				payload.State = text
				continue
			}
			payload.Attributes[slot.Name] = text
		case SlotKindFraction:
			fraction, err := slot.normalizeFraction(raw)
			if err != nil {
				return Payload{}, fmt.Errorf("slot %q: %w", slot.Name, err)
			}
			payload.Attributes[slot.Name] = fraction
		case SlotKindColor:
			color, err := slot.normalizeColor(raw)
			if err != nil {
				return Payload{}, fmt.Errorf("slot %q: %w", slot.Name, err)
			}
			payload.Attributes[slot.Name] = color
		default:
			return Payload{}, fmt.Errorf("slot %q: unsupported slot kind %q", slot.Name, slot.Kind)
		}
	}

	if payload.State == "" {
		// Unreachable for a registered target (validate guarantees a
		// required primary slot, and the required check above fires
		// first), but a sink publishing an empty state would produce an
		// entity HA renders as "unknown" — fail here instead.
		return Payload{}, fmt.Errorf("target %q produced no state value", t.ID)
	}
	if len(payload.Attributes) == 0 {
		payload.Attributes = nil
	}
	return payload, nil
}

// rejectUnknownSlots fails on any argument key the target does not
// define, listing the valid slots. A silently dropped key looks to the
// model like a slot that renders nothing.
func (t Target) rejectUnknownSlots(args map[string]any) error {
	var unknown []string
	for key := range args {
		if _, ok := t.Slot(key); !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf(
		"target %q has no slot named %s; valid slots are %s",
		t.ID, strings.Join(quoteAll(unknown), ", "), strings.Join(quoteAll(t.SlotNames()), ", "),
	)
}

// normalizeText trims and length-checks a text slot value.
func (s Slot) normalizeText(raw any) (string, error) {
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("expected a string, got %T (%s)", raw, preview(raw))
	}
	text = strings.TrimSpace(text)
	if i := strings.IndexFunc(text, isDisplayControl); i >= 0 {
		return "", fmt.Errorf("contains a control character at offset %d; this slot renders as a single line of text", i)
	}
	// Rune count, not len: a budget measured in bytes would reject
	// legitimate accented or emoji values that fit the display fine.
	if count := utf8.RuneCountInString(text); count > s.MaxRunes {
		return "", fmt.Errorf("is %d characters but this slot renders at most %d; shorten it rather than relying on truncation (got %s)", count, s.MaxRunes, preview(text))
	}
	return text, nil
}

// normalizeFraction range-checks a gauge fill value.
func (s Slot) normalizeFraction(raw any) (float64, error) {
	value, ok := coerceFloat(raw)
	if !ok {
		return 0, fmt.Errorf("expected a number between 0.0 and 1.0, got %T (%s)", raw, preview(raw))
	}
	if value < 0 || value > 1 {
		return 0, fmt.Errorf("is %v but must be between 0.0 and 1.0; normalize it first, e.g. (value - minimum) / (maximum - minimum) clamped to the range", value)
	}
	return value, nil
}

// normalizeColor canonicalizes a hex color to "#RRGGBB".
func (s Slot) normalizeColor(raw any) (string, error) {
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("expected an \"#RRGGBB\" hex color string, got %T (%s)", raw, preview(raw))
	}
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "#")
	if len(trimmed) != 6 || !isHex(trimmed) {
		return "", fmt.Errorf("must be a six-digit hex color like \"#3FB950\"; got %s", preview(text))
	}
	return "#" + strings.ToUpper(trimmed), nil
}

// coerceFloat accepts the numeric shapes a tool call can arrive in: JSON
// decodes to float64 by default and to json.Number under a UseNumber
// decoder, and some model families emit numbers as quoted strings.
func coerceFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// isDisplayControl reports whether r would break a single-line display.
// Newlines and tabs are the realistic offenders, but the whole control
// range is rejected: none of it renders on a complication.
func isDisplayControl(r rune) bool {
	return unicode.IsControl(r)
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.Quote(value)
	}
	return out
}

// preview bounds a model-supplied value before it enters an error.
//
// Slot values arrive from tool arguments, so their size is decided by
// the model rather than by this package. An error that echoes the whole
// value lands in the log and in the model's next turn, where a runaway
// string costs context at the exact moment something is already going
// wrong. Enough to recognise what was sent is enough to fix it.
func preview(raw any) string {
	const max = 80
	text := fmt.Sprintf("%v", raw)
	if utf8.RuneCountInString(text) <= max {
		return fmt.Sprintf("%q", text)
	}
	runes := []rune(text)
	return fmt.Sprintf("%q… (%d characters)", string(runes[:max]), len(runes))
}
