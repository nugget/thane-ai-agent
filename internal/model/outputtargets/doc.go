// Package outputtargets defines the slotted rendering surfaces a loop
// may publish to.
//
// A maintained document asks the model for prose. A slotted surface —
// an Apple Watch complication, a compact display, a status tile — asks
// instead for a small set of named, budgeted values, because it has
// fixed geometry: four lines of about twenty characters, a gauge wanting
// a number in [0,1]. So the contract is fixed too: named slots, one
// primary slot, per-slot type and size limits.
//
// # Landed ahead of its consumer
//
// This package has no caller yet. It was written for #1253, which
// modelled a slotted surface as a third loop output type with its own
// tool verb. #1250 replaced that framing: a slotted surface is not a
// third kind of output but another facet of one — a face cut for a
// surface that renders no prose — and the machinery here becomes the
// shared validation core the facet contract already needed.
//
// The registry survived that change untouched because it never knew
// about loops: it describes surfaces and the values they hold, and the
// question of who publishes to them lives entirely elsewhere. Rebuilding
// it to say the same things in a different caller's vocabulary would
// have been waste, so it is preserved here while the facet binding that
// consumes it is designed.
//
// # Why a registry
//
// Targets live in code rather than operator config so the budgets and
// validators that make a target real are compile-checked and testable.
// Adding a surface is adding a [Target] value to the registry in this
// package; nothing downstream changes. A consumer validates a declared
// target ID against [Lookup], turns [Target.Schema] into the arguments a
// model is offered and [Target.Normalize] into what checks them, and
// publishes the resulting [Payload].
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
