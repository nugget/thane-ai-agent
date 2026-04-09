// Package messages defines the shared envelope and delivery substrate for
// inter-component communication inside Thane. It is intentionally narrower
// than a full workflow layer: callers construct envelopes, the bus routes
// them, and concrete delivery backends implement the destination-specific
// behavior.
package messages
