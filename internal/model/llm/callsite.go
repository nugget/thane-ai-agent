package llm

import "context"

// CallKind names the reason the agent's turn made one physical model
// call: the ordinary iteration, or which recovery path made it instead.
type CallKind string

const (
	// CallKindIteration is an ordinary iteration of the turn, including
	// one the engine re-ran to nudge an empty response.
	CallKindIteration CallKind = "iteration"
	// CallKindForceText is the tool-free call that asks for a closing
	// text answer once the iteration loop has stopped.
	CallKindForceText CallKind = "force_text"
	// CallKindRetry repeats a timed-out call against the same model.
	CallKindRetry CallKind = "retry"
	// CallKindFailover repeats a failed call against the default model.
	CallKindFailover CallKind = "failover"
	// CallKindTimeoutRecovery asks the recovery model to summarize a turn
	// whose retries all timed out. Its messages are a synthetic summary
	// prompt, not the turn's own history.
	CallKindTimeoutRecovery CallKind = "timeout_recovery"
	// CallKindContextRecovery repeats a call with the tools it offered
	// after the runner rejected it for its loaded context window and the
	// window was refreshed or grown.
	CallKindContextRecovery CallKind = "context_recovery"
	// CallKindContextRecoveryNoTools repeats that rejected call with the
	// tool definitions it offered removed, to make the prompt fit.
	CallKindContextRecoveryNoTools CallKind = "context_recovery_no_tools"
	// CallKindLite is the single tool-free call of a request that skips
	// the agent loop's context assembly.
	CallKindLite CallKind = "lite"
)

type callSiteKey struct{}

// CallSite places one physical model call within the agent turn that
// made it.
//
// It is not usage.Record.Purpose. Purpose says what kind of work a call
// was billed to, the agent or an auxiliary job such as compaction; every
// call stamped with a CallSite has the agent purpose. CallSite says where
// inside that agent turn the call happened.
type CallSite struct {
	// Iteration is the zero-based index of the engine iteration the call
	// belongs to, matching the Index of its iterate.IterationRecord. A
	// retry, failover or recovery keeps the iteration of the call it
	// replaces. A lite call, which runs no engine, is iteration 0.
	Iteration int `json:"iteration"`
	// Kind is why the call was made.
	Kind CallKind `json:"kind"`
}

// WithCallSite attaches site to ctx, replacing any call site already
// there.
func WithCallSite(ctx context.Context, site CallSite) context.Context {
	return context.WithValue(ctx, callSiteKey{}, site)
}

// CallSiteFromContext returns the call site carried by ctx, and false when
// the call was made without one.
func CallSiteFromContext(ctx context.Context) (CallSite, bool) {
	site, ok := ctx.Value(callSiteKey{}).(CallSite)
	return site, ok
}

// WithCallKind re-stamps the call site on ctx with kind and keeps its
// iteration, for a call that stands in for the one ctx was stamped for.
// When ctx carries no call site it is returned unchanged: the iteration
// is unknown, and claiming iteration 0 would be a false placement.
func WithCallKind(ctx context.Context, kind CallKind) context.Context {
	site, ok := CallSiteFromContext(ctx)
	if !ok {
		return ctx
	}
	site.Kind = kind
	return WithCallSite(ctx, site)
}

// CarryCallScope copies the values that describe one model call from src
// onto dst: the output-token budget and the call site. Nothing else
// crosses, and in particular not src's deadline or cancellation.
//
// It exists for a call that must run on a context detached from the one
// it belongs to, such as a recovery that must outlive the deadline that
// just expired. Detaching drops every context value; this puts back the
// ones that bound and place the call, from one list kept here.
func CarryCallScope(dst, src context.Context) context.Context {
	dst = WithMaxOutputTokens(dst, MaxOutputTokensFromContext(src))
	if site, ok := CallSiteFromContext(src); ok {
		dst = WithCallSite(dst, site)
	}
	return dst
}
