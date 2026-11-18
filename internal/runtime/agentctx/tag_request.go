package agentctx

import (
	"context"
	"errors"
)

// TagRequester activates a capability tag on the agent run that
// installed it.
type TagRequester func(tag string) error

type tagRequesterKey struct{}

// ErrNoTagRequester reports that a context carries no [TagRequester]: it
// belongs to no run, or to a call its run did not install one on.
var ErrNoTagRequester = errors.New("no tag requester in context")

// WithTagRequester returns ctx carrying request. The agent loop installs
// one only on the context it passes a turn's mid-turn input pull, so a
// channel rendering newly arrived input can activate a tag that input
// earns on the run it joins. A nil request leaves ctx unchanged.
func WithTagRequester(ctx context.Context, request TagRequester) context.Context {
	if request == nil {
		return ctx
	}
	return context.WithValue(ctx, tagRequesterKey{}, request)
}

// RequestTag activates tag on the run that installed ctx's requester,
// or returns [ErrNoTagRequester].
func RequestTag(ctx context.Context, tag string) error {
	request, ok := ctx.Value(tagRequesterKey{}).(TagRequester)
	if !ok {
		return ErrNoTagRequester
	}
	return request(tag)
}
