package llm

import "context"

// WireFacts describes one chat request exactly as a provider encoded it
// for the wire. Each field is read from the encoded request, not from
// the configuration or context that fed it, so a fact here is what the
// server was sent rather than what Thane meant to send.
type WireFacts struct {
	// Provider is the server flavor label of the client that encoded
	// the request, such as "openai_compat" or "lmstudio".
	Provider string
	// Resource names the configured endpoint the request was bound
	// for. Empty for an ad-hoc client that no configured resource backs.
	Resource string
	// Model is the model identifier written into the request body. For
	// LM Studio this is the loaded instance id when one was resolved,
	// which is why it can differ from the deployment name a caller
	// selected.
	Model string
	// TTLSeconds is the idle-unload ttl written into the body. Zero
	// means the field was omitted.
	TTLSeconds int
	// MaxTokens is the output ceiling written into the body. Zero means
	// the field was omitted.
	MaxTokens int
	// Stream reports whether the request asked for a streamed response.
	Stream bool
	// ChatTemplateKwargs is the chat_template_kwargs object written into
	// the body, nil when the field was omitted. It is a copy: changing
	// it changes nothing that was or will be sent.
	ChatTemplateKwargs map[string]any
	// BodySHA256 is the lowercase hex SHA-256 digest of the exact bytes
	// posted as the request body.
	BodySHA256 string
	// BodyBytes is the length of that body in bytes.
	BodyBytes int
}

// WireObserver receives the [WireFacts] of each request a provider is
// about to post. It is called synchronously on the request path, once
// per call, before the request leaves, so it must return promptly. The
// facts are the observer's own to keep.
type WireObserver func(WireFacts)

// wireObserverKey carries the [WireObserver] for one model call.
type wireObserverKey struct{}

// WithWireObserver attaches an observer that providers which report
// wire facts call for each request made under ctx. A nil observer
// attaches nothing.
//
// It rides the context for the same reason as [WithMaxOutputTokens]:
// the observer is optional, only some providers report wire facts, and
// widening [Client] would make every implementation acknowledge it. A
// provider that does not report leaves the observer uncalled, which a
// caller must read as "wire facts unknown", never as zero values.
func WithWireObserver(ctx context.Context, observer WireObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, wireObserverKey{}, observer)
}

// ObserveWire hands facts to the observer attached to ctx, and does
// nothing when none is attached.
func ObserveWire(ctx context.Context, facts WireFacts) {
	if observer, _ := ctx.Value(wireObserverKey{}).(WireObserver); observer != nil {
		observer(facts)
	}
}
