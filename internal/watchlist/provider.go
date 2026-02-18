package watchlist

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

// StateGetter abstracts the Home Assistant REST client for fetching
// entity state. Using an interface keeps the provider testable without
// a real HA instance.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
}

// Provider implements agent.ContextProvider by fetching live state for
// all watched entities and formatting them as a markdown block for
// system prompt injection.
type Provider struct {
	store  *Store
	ha     StateGetter
	logger *slog.Logger
}

// NewProvider creates a watchlist context provider.
func NewProvider(store *Store, ha StateGetter, logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		store:  store,
		ha:     ha,
		logger: logger,
	}
}

// GetContext returns a formatted markdown block of watched entity states
// for injection into the agent's system prompt. Returns an empty string
// when the watchlist is empty. Implements agent.ContextProvider.
func (p *Provider) GetContext(ctx context.Context, _ string) (string, error) {
	ids, err := p.store.List()
	if err != nil {
		return "", fmt.Errorf("list watched entities: %w", err)
	}
	if len(ids) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("### Watched Entities\n\n")

	for _, id := range ids {
		state, err := p.ha.GetState(ctx, id)
		if err != nil {
			p.logger.Warn("failed to fetch watched entity state",
				"entity_id", id,
				"error", err,
			)
			fmt.Fprintf(&sb, "- **%s**: unavailable\n", id)
			continue
		}

		displayName := id
		if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
			displayName = name
		}

		stateValue := state.State
		if unit, ok := state.Attributes["unit_of_measurement"].(string); ok && unit != "" {
			stateValue += " " + unit
		}

		since := state.LastChanged.Format(time.RFC3339)
		fmt.Fprintf(&sb, "- **%s** (%s): %s (since %s)\n", displayName, id, stateValue, since)
	}

	return sb.String(), nil
}
