package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// channelNotes maps source hint values to system prompt context notes.
// Each note describes the channel's characteristics so the agent can
// adjust its communication style accordingly.
var channelNotes = map[string]string{
	"signal": "[Source: Signal \u2014 mobile messaging from Nugget. " +
		"Terse input is normal; typing on mobile devices is slow " +
		"and brevity is not an indicator of emotional state.]",
}

// ChannelProvider is a ContextProvider that injects channel-specific
// notes into the system prompt based on the "source" routing hint
// attached to the request context. When no recognized source is
// present, it returns an empty string.
type ChannelProvider struct{}

// NewChannelProvider creates a channel awareness context provider.
func NewChannelProvider() *ChannelProvider {
	return &ChannelProvider{}
}

// GetContext returns a channel-specific note if the request context
// carries a "source" hint that matches a known channel. Returns an
// empty string otherwise.
func (p *ChannelProvider) GetContext(ctx context.Context, _ string) (string, error) {
	hints := tools.HintsFromContext(ctx)
	if hints == nil {
		return "", nil
	}
	if note, ok := channelNotes[hints["source"]]; ok {
		return note, nil
	}
	return "", nil
}
