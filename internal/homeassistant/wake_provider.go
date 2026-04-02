package homeassistant

import (
	"context"
	"fmt"
	"strings"
)

// WakeProvider implements agent.ContextProvider for wake subscription
// context injection. It lists active wake subscriptions in the system
// prompt so the model knows what it's watching for.
type WakeProvider struct {
	store *WakeStore
}

// NewWakeProvider creates a wake subscription context provider.
func NewWakeProvider(store *WakeStore) *WakeProvider {
	return &WakeProvider{store: store}
}

// GetContext implements agent.ContextProvider. Returns a formatted
// summary of active wake subscriptions for system prompt injection.
func (p *WakeProvider) GetContext(_ context.Context, _ string) (string, error) {
	active, err := p.store.Active()
	if err != nil {
		return "", err
	}

	if len(active) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("## Active Wake Subscriptions\n\n")
	sb.WriteString("You have configured these MQTT wake subscriptions. When a message arrives on the subscribed topic, you will wake with the associated context.\n\n")

	for _, w := range active {
		if !w.Enabled {
			continue
		}
		fmt.Fprintf(&sb, "- **%s** — topic: `%s`", w.Name, w.Topic)
		if w.KBRef != "" {
			fmt.Fprintf(&sb, " — KB: %s", w.KBRef)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nUse `cancel_anticipation` to remove subscriptions that are no longer needed.\n")

	return sb.String(), nil
}
