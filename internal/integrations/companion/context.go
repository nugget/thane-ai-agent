package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// ContextProvider injects the set of currently-connected companion apps and
// the tools they offer into the system prompt. It implements
// [agent.TagContextProvider] structurally and is registered on the
// companion capability tag, so it fires only when that tag is active.
//
// Companions are laptops that pop on and off line without warning. This
// block is what lets the model tell, on a companion-tagged turn, whether a
// device is actually connected and which tools it currently exposes —
// rather than guessing from the tool list alone.
type ContextProvider struct {
	registry *Registry
}

// NewContextProvider creates a companion live-state context provider.
func NewContextProvider(registry *Registry) *ContextProvider {
	return &ContextProvider{registry: registry}
}

// TagContextBucket places the connected-companion view in live state: it
// reflects current runtime connectivity and must not thrash the cached
// prompt prefix.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

type companionContextJSON struct {
	Companions []connectedCompanionJSON `json:"companions"`
}

type connectedCompanionJSON struct {
	Account      string   `json:"account"`
	ClientName   string   `json:"client_name,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	ConnectedAgo string   `json:"connected_ago"`
	Tools        []string `json:"tools,omitempty"`
}

// TagContext returns the connected-companion block for tag-gated injection.
// Implements [agent.TagContextProvider].
func (p *ContextProvider) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	if p.registry == nil {
		return "", nil
	}

	now := time.Now()
	infos := p.registry.List()

	companions := make([]connectedCompanionJSON, 0, len(infos))
	for _, info := range infos {
		var toolNames []string
		for _, cap := range info.Capabilities {
			for _, def := range cap.Tools {
				toolNames = append(toolNames, def.Name)
			}
		}
		sort.Strings(toolNames)

		companions = append(companions, connectedCompanionJSON{
			Account:      info.Account,
			ClientName:   info.ClientName,
			ClientID:     info.ClientID,
			ConnectedAgo: promptfmt.FormatDeltaOnly(info.ConnectedAt, now),
			Tools:        toolNames,
		})
	}

	// Deterministic order across turns (by account, then client) so the
	// model can compare turns without relearning the shape.
	sort.Slice(companions, func(i, j int) bool {
		if companions[i].Account != companions[j].Account {
			return companions[i].Account < companions[j].Account
		}
		return companions[i].ClientID < companions[j].ClientID
	})

	data, err := json.Marshal(companionContextJSON{Companions: companions})
	if err != nil {
		return "", fmt.Errorf("marshal companion context: %w", err)
	}
	return string(data), nil
}
