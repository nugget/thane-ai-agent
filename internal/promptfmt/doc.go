// Package promptfmt provides presentation-layer helpers used to render
// values into prompt and context strings: relative timestamps, ID
// truncation for display, thousands-grouped numbers, and compact JSON.
//
// These helpers are domain-agnostic pure functions. Any package that
// builds text for injection into an LLM prompt (context providers,
// entity formatters, state-window renderers) can import them without
// back-edging into a sibling domain.
package promptfmt
