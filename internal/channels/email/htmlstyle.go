package email

import "strconv"

// One inline style attribute can declare a property more than once. A
// browser applies the winning declaration and ignores the rest, so the
// renderer judges each property that bears on visibility by that
// declaration alone: display:none;display:block shows its text.
//
// A declaration takes part only when the renderer recognises its value,
// the way a browser drops an invalid declaration before choosing a
// winner. Recognised means a value every engine accepts. A value some
// engine rejects is treated as rejected everywhere, so a hiding
// declaration before it still stands, and the renderer withholds text a
// reader in some client might see rather than show text a reader in
// another cannot.

// styleEffect is what a property's winning declaration does to the
// visibility an element inherits.
type styleEffect uint8

const (
	// effectKeep leaves the inherited state alone: no declaration, a
	// value such as inherit, or a size relative to the parent's.
	effectKeep styleEffect = iota

	// effectHide hides the element's content.
	effectHide

	// effectShow sets a visible state of the element's own, which shows
	// what an ancestor's zero font-size or visibility:hidden hid.
	effectShow
)

// declaration is the winning declaration of one property so far.
type declaration struct {
	effect    styleEffect
	important bool
	set       bool
}

// offer applies CSS precedence to the next declaration of the property:
// a later declaration beats an earlier one, except that one marked
// !important yields only to a later !important one.
func (d *declaration) offer(effect styleEffect, important bool) {
	if d.set && d.important && !important {
		return
	}
	*d = declaration{effect: effect, important: important, set: true}
}

// apply returns the state hidden takes under the declaration, given the
// state it inherits.
func (d declaration) apply(hidden bool) bool {
	switch d.effect {
	case effectHide:
		return true
	case effectShow:
		return false
	}
	return hidden
}

// inlineStyle holds the winning declaration of each property that bears
// on visibility in one style attribute.
type inlineStyle struct {
	display, opacity, visibility, fontSize declaration
}

// parseInlineStyle resolves the winning declaration of each property in
// one style attribute, split into declarations the way a browser splits
// it (see [splitDeclarations]).
func parseInlineStyle(style string) inlineStyle {
	var s inlineStyle
	for _, d := range splitDeclarations(style) {
		var slot *declaration
		var effect styleEffect
		var ok bool
		switch d.name {
		case "display":
			slot = &s.display
			effect, ok = displayEffect(d.value)
		case "opacity":
			slot = &s.opacity
			effect, ok = opacityEffect(d.value)
		case "visibility":
			slot = &s.visibility
			effect, ok = visibilityEffect(d.value)
		case "font-size":
			slot = &s.fontSize
			effect, ok = fontSizeEffect(d.value)
		default:
			continue
		}
		if ok && !escapedNumberShows(d, effect) {
			slot.offer(effect, d.important)
		}
	}
	return s
}

// escapedNumberShows reports a numeric value written with an escape
// that would show text. The tokenizer reads an escaped digit as the
// start of an ident, not a number, so a browser drops opacity:\31 where
// decoding the escape reads 1. The renderer trusts a numeric value
// written with any escape only to hide, so it withholds text a browser
// might show rather than show text a browser hid.
func escapedNumberShows(d cssDeclaration, effect styleEffect) bool {
	if !d.escaped || effect == effectHide {
		return false
	}
	_, _, numeric := cssNumber(d.value)
	return numeric
}

// cssWideKeyword reports whether val is one of the keywords every
// property accepts.
func cssWideKeyword(val string) bool {
	switch val {
	case "inherit", "initial", "unset", "revert", "revert-layer":
		return true
	}
	return false
}

// displayKeywords are the display values other than none that every
// engine accepts. The multi-keyword forms, such as "block flow", are
// absent because older engines reject them.
var displayKeywords = map[string]bool{
	"block": true, "inline": true, "inline-block": true, "list-item": true,
	"contents": true, "flow-root": true, "flex": true, "inline-flex": true,
	"grid": true, "inline-grid": true, "table": true, "inline-table": true,
	"table-row-group": true, "table-header-group": true, "table-footer-group": true,
	"table-row": true, "table-cell": true, "table-column-group": true,
	"table-column": true, "table-caption": true,
	"-webkit-box": true, "-webkit-inline-box": true,
	"-webkit-flex": true, "-webkit-inline-flex": true,
}

// displayEffect reads a display value. none hides; every other
// recognised value, including inherit under a parent that is not
// display:none, leaves the content shown.
func displayEffect(val string) (styleEffect, bool) {
	switch {
	case val == "none":
		return effectHide, true
	case displayKeywords[val], cssWideKeyword(val):
		return effectKeep, true
	}
	return effectKeep, false
}

// visibilityEffect reads a visibility value. initial is visible; the
// other CSS-wide keywords inherit, because visibility is inherited.
func visibilityEffect(val string) (styleEffect, bool) {
	switch val {
	case "hidden", "collapse":
		return effectHide, true
	case "visible", "initial":
		return effectShow, true
	}
	if cssWideKeyword(val) {
		return effectKeep, true
	}
	return effectKeep, false
}

// opacityEffect reads an opacity value: a number or a percentage, where
// zero or less leaves nothing visible because a browser clamps a
// negative opacity to zero. A CSS-wide keyword gives a nonzero opacity,
// since an ancestor with a zero one has already hidden the element.
func opacityEffect(val string) (styleEffect, bool) {
	if cssWideKeyword(val) {
		return effectKeep, true
	}
	f, unit, ok := cssNumber(val)
	if !ok || (unit != "" && unit != "%") {
		return effectKeep, false
	}
	if f <= 0 {
		return effectHide, true
	}
	return effectKeep, true
}

// cssNumber splits a value into the number it starts with and the rest,
// which is the unit, the way the CSS tokenizer reads a number: an
// optional sign, digits with an optional fraction, and an exponent only
// where e is followed by a digit, so 1em is one em and 1e1px is ten
// pixels. ok is false when val does not start with a number.
func cssNumber(val string) (f float64, unit string, ok bool) {
	i := 0
	if i < len(val) && (val[i] == '+' || val[i] == '-') {
		i++
	}
	start := i
	i = skipDigits(val, i)
	if i+1 < len(val) && val[i] == '.' && isDigit(val[i+1]) {
		i = skipDigits(val, i+1)
	}
	if i == start {
		return 0, "", false
	}
	if i < len(val) && val[i] == 'e' {
		j := i + 1
		if j < len(val) && (val[j] == '+' || val[j] == '-') {
			j++
		}
		if j < len(val) && isDigit(val[j]) {
			i = skipDigits(val, j)
		}
	}
	f, err := strconv.ParseFloat(val[:i], 64)
	if err != nil {
		// Only an exponent too large for a float64 gets here.
		return 0, "", false
	}
	return f, val[i:], true
}

// skipDigits returns the index of the first byte at or after i in s
// that is not an ASCII digit.
func skipDigits(s string, i int) int {
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

func isDigit(b byte) bool { return '0' <= b && b <= '9' }
