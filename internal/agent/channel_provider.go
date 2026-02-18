package agent

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

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
// carries a "source" hint that matches a known channel. For Signal,
// the sender name is resolved dynamically from the sender_name hint
// (populated by the bridge's ContactResolver). Returns an empty
// string for unrecognized sources.
func (p *ChannelProvider) GetContext(ctx context.Context, _ string) (string, error) {
	hints := tools.HintsFromContext(ctx)
	if hints == nil {
		return "", nil
	}

	switch hints["source"] {
	case "signal":
		name := hints["sender_name"]
		if name == "" {
			name = hints["sender"]
		}
		if name == "" {
			name = "unknown sender"
		}
		return fmt.Sprintf("[Source: Signal — mobile messaging from %s. "+
			"Terse input is normal; typing on mobile devices is slow "+
			"and brevity is not an indicator of emotional state.]", name), nil
	default:
		return "", nil
	}
}
