// Package outputtargets defines the slotted rendering targets a loop may
// declare as a structured output.
//
// A structured output is the non-document tier of the loop output
// contract in [github.com/nugget/thane-ai-agent/internal/runtime/loop]. A
// maintained document asks the model for prose; a structured output asks
// it for a small set of named, budgeted values that a specific external
// surface renders. The surface — an Apple Watch complication, a compact
// display, a status tile — has fixed geometry, so the contract is fixed
// too: named slots, one primary slot, per-slot type and size limits.
//
// # Why a registry
//
// Targets live in code rather than operator config so the budgets and
// validators that make a target real are compile-checked and testable.
// Adding a tier is adding a [Target] value to the registry in this
// package; nothing downstream changes. The loop layer validates a
// declared target ID against [Lookup], the app layer turns [Target.Schema]
// into a request-scoped tool and [Target.Normalize] into that tool's
// handler, and the sink publishes the resulting [Payload].
//
// # Slots and the primary slot
//
// Every target has exactly one slot marked primary. The primary slot's
// value becomes the [Payload] state — the single scalar a consuming
// surface reads first (for the MQTT sink, the sensor's state). Remaining
// slots become payload attributes. This split is what lets a downstream
// binding stay trivial: the headline value needs no attribute lookup.
//
// # Validation teaches
//
// [Target.Normalize] rejects rather than repairs. A slot over its rune
// budget, a fraction outside [0,1], or an unparseable color returns an
// error naming the slot, the limit, and what arrived, so a delegate can
// fix it in one more call instead of guessing. The exception is
// whitespace trimming and color-hex casing, which are canonicalization
// rather than a change of meaning.
package outputtargets
