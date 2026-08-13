package outputtargets

// Apple Watch complication targets.
//
// Slot names mirror the companion app's own vocabulary (title, subtitle,
// value, bottom_text) so an operator wiring the complication editor sees
// the same words in both places. The rune budgets are deliberately
// tighter than what the widget will technically accept: watchOS shrinks
// and then truncates overflowing text, and the accessoryRectangular slot
// on the Modular Ultra face clips earlier than on other faces, so a
// budget that fits everywhere beats one that fits the roomiest face.
//
// Colors are published as ordinary attributes because an entity-sourced
// complication cannot template its colors — only a template-sourced one
// can. Both bindings read the same entity, so the operator picks the
// binding they want without Thane publishing anything differently.

const appleWatchColorNote = "Ignored unless the complication is template-sourced and reads this attribute in its color template. watchOS also renders on-face complications in the face's accent palette, so expect full fidelity in the Smart Stack and a tinted approximation on the watch face itself."

var appleWatchRectangular = Target{
	ID:      "apple_watch.rectangular",
	Title:   "Apple Watch rectangular complication",
	Summary: "The four-line accessoryRectangular complication: the large middle slot of the Modular Ultra face, and the wide slot on Modular and Infograph Modular. Renders an icon, a title line, a subtitle line, a value (as a progress-bar thumb when a fraction is set), and a bottom line. Nothing wraps — every line is one line, shrunk to fit and then clipped.",
	Binding: "Published as an MQTT sensor. In the companion app's complication builder choose the Rectangular size and an Entity source pointing at this sensor, then bind slots with {value} for the state and {attr:title} / {attr:subtitle} / {attr:bottom_text} for the rest. Set the progress bar's minimum to 0 and maximum to 1 to consume {attr:fraction}.",
	Icon:    "mdi:watch-variant",
	Slots: []Slot{
		{
			Name:        "value",
			Kind:        SlotKindText,
			Description: "The headline value and the sensor's state. Renders as the label inside the progress-bar thumb when a fraction is set, otherwise as its own line. Keep it to a number and a unit — this is the shortest slot on the target.",
			Required:    true,
			MaxRunes:    12,
			Primary:     true,
		},
		{
			Name:        "title",
			Kind:        SlotKindText,
			Description: "First line, semibold. What this complication is about — a name or label, not a sentence.",
			MaxRunes:    18,
		},
		{
			Name:        "subtitle",
			Kind:        SlotKindText,
			Description: "Second line, dimmed. Secondary context for the title: a qualifier, a comparison, or a second reading.",
			MaxRunes:    22,
		},
		{
			Name:        "bottom_text",
			Kind:        SlotKindText,
			Description: "Last line, dimmed. Trailing detail such as a freshness note or a next-event hint. Hidden by default in the complication editor; the operator opts in.",
			MaxRunes:    22,
		},
		{
			Name:        "fraction",
			Kind:        SlotKindFraction,
			Description: "Progress-bar fill from 0.0 (empty) to 1.0 (full). Normalize the underlying reading yourself — the bar has no notion of your units. Omit when there is nothing meaningful to fill.",
		},
		{
			Name:        "gauge_color",
			Kind:        SlotKindColor,
			Description: "Progress-bar tint as #RRGGBB. " + appleWatchColorNote,
		},
		{
			Name:        "icon_color",
			Kind:        SlotKindColor,
			Description: "Icon tint as #RRGGBB. " + appleWatchColorNote,
		},
		{
			Name:        "text_color",
			Kind:        SlotKindColor,
			Description: "Text tint as #RRGGBB. " + appleWatchColorNote,
		},
	},
}

var appleWatchCircular = Target{
	ID:      "apple_watch.circular",
	Title:   "Apple Watch circular complication",
	Summary: "The small round accessoryCircular complication: a gauge ring around a center that holds an icon, a value, and optionally a name. It is the smallest target here — assume roughly a half-dozen characters are legible, and that the ring carries more information than the text does.",
	Binding: "Published as an MQTT sensor. In the companion app's complication builder choose the Circular size and an Entity source pointing at this sensor, then bind {value} for the state and {attr:title} for the name slot. Set the gauge's minimum to 0 and maximum to 1 to consume {attr:fraction}, and pick the open-arc or capacity-ring style there.",
	Icon:    "mdi:watch-variant",
	Slots: []Slot{
		{
			Name:        "value",
			Kind:        SlotKindText,
			Description: "The center value and the sensor's state. This is a glanceable scalar: a number, a percentage, a two-letter status. Long strings shrink until they are unreadable rather than wrapping.",
			Required:    true,
			MaxRunes:    6,
			Primary:     true,
		},
		{
			Name:        "title",
			Kind:        SlotKindText,
			Description: "Optional name under the value. Hidden by default on circular because the center is small — supply it only when the value alone is ambiguous, and keep it to one short word.",
			MaxRunes:    8,
		},
		{
			Name:        "fraction",
			Kind:        SlotKindFraction,
			Description: "Gauge ring fill from 0.0 (empty) to 1.0 (full). On this target the ring is the primary signal; prefer setting it whenever the value is numeric.",
		},
		{
			Name:        "gauge_color",
			Kind:        SlotKindColor,
			Description: "Gauge ring tint as #RRGGBB. " + appleWatchColorNote,
		},
		{
			Name:        "icon_color",
			Kind:        SlotKindColor,
			Description: "Icon tint as #RRGGBB. " + appleWatchColorNote,
		},
		{
			Name:        "text_color",
			Kind:        SlotKindColor,
			Description: "Value text tint as #RRGGBB. " + appleWatchColorNote,
		},
	},
}
