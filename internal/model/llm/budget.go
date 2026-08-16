package llm

import "context"

// maxOutputTokensKey carries the remaining output-token budget for one
// model call.
type maxOutputTokensKey struct{}

// WithMaxOutputTokens attaches the output-token budget remaining for the
// next model call, so a provider can ask the server to stop there.
//
// This rides the context rather than the [Client] signature, which is a
// deliberate exception to the usual rule against parameters in context.
// The value is an optional hint that every provider clamps against its
// own ceiling and none requires; ChatStream has fifteen implementations
// across providers, the routed client, and test doubles, and widening
// that signature would make every one of them acknowledge a budget most
// have no opinion about. A provider that ignores this is not broken —
// it is exactly where the runtime was before, with the post-response
// check as the backstop.
//
// A non-positive budget attaches nothing: "no ceiling" and "no budget
// left" must not arrive as the same value, and the caller stops the turn
// before the latter can reach a provider.
func WithMaxOutputTokens(ctx context.Context, tokens int) context.Context {
	if tokens <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxOutputTokensKey{}, tokens)
}

// MaxOutputTokensFromContext returns the remaining output-token budget
// for this call, or 0 when the caller set no budget. Providers should
// treat 0 as "no ceiling of mine to lower" and keep whatever limit they
// would otherwise send.
func MaxOutputTokensFromContext(ctx context.Context) int {
	tokens, _ := ctx.Value(maxOutputTokensKey{}).(int)
	if tokens < 0 {
		return 0
	}
	return tokens
}

// ClampMaxOutputTokens lowers a provider's own output ceiling to the
// budget remaining on ctx, and returns the ceiling unchanged when no
// budget is set or the budget is the looser of the two.
//
// Providers call this instead of reading the context directly so the
// precedence is stated once: a budget may only ever tighten a ceiling.
// A budget that exceeds what the provider will accept is not permission
// to send a larger value — that is how an over-limit request gets
// rejected outright and a turn dies for asking.
func ClampMaxOutputTokens(ctx context.Context, ceiling int) int {
	budget := MaxOutputTokensFromContext(ctx)
	if budget <= 0 {
		return ceiling
	}
	if ceiling <= 0 || budget < ceiling {
		return budget
	}
	return ceiling
}
