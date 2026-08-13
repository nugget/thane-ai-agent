// Package introspection projects thane's own internal operation into
// model-facing views: the persistent loop-event journal behind
// loop_activity, and the subsystem annunciator panel behind
// system_health and the metacog context panel.
//
// It exists because most of thane's operational signals were computed
// but unrouted — visible to HTTP health endpoints or to nobody — while
// the loop runtime's own event stream was ephemeral: a restart erased
// exactly the evidence a post-incident investigation needs. This
// package is the single source of truth those surfaces render from, so
// a tool result and a context panel can never drift apart. (#1341)
package introspection
