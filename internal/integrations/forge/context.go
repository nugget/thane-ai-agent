package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// ContextProvider injects forge account configuration and recent
// operations into the system prompt. Implements
// [agent.TagContextProvider] via structural typing; registered as a
// tag-gated provider on the forge capability tag.
//
// When an OperationLog is provided, the context is dynamic — each
// call to TagContext includes the latest operation history with
// delta-annotated timestamps.
type ContextProvider struct {
	manager *Manager
	opLog   *OperationLog
}

// NewContextProvider creates a forge context provider. When opLog is
// non-nil, recent operations are included in the context each turn.
func NewContextProvider(mgr *Manager, opLog *OperationLog) *ContextProvider {
	return &ContextProvider{
		manager: mgr,
		opLog:   opLog,
	}
}

// forgeContextJSON is the JSON structure emitted by the provider.
type forgeContextJSON struct {
	Forges    []accountView  `json:"forges"`
	RecentOps []recentOpJSON `json:"recent_operations,omitempty"`
}

// recentOpJSON is a single recent operation with delta timestamp.
type recentOpJSON struct {
	Tool    string `json:"tool"`
	Account string `json:"account"`
	Repo    string `json:"repo"`
	Ref     string `json:"ref,omitempty"`
	Ago     string `json:"ago"`
}

// TagContext returns the forge context block for tag-gated injection.
// Implements [agent.TagContextProvider].
func (p *ContextProvider) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	return p.buildContext()
}

func (p *ContextProvider) buildContext() (string, error) {
	if p.manager == nil || len(p.manager.order) == 0 {
		return "", nil
	}

	now := time.Now()

	// Account config.
	views := make([]accountView, 0, len(p.manager.order))
	for _, name := range p.manager.order {
		cfg := p.manager.configs[name]
		views = append(views, accountView{
			Account:      cfg.Name,
			Type:         cfg.Provider,
			URL:          cfg.URL,
			DefaultOwner: cfg.Owner,
		})
	}

	output := forgeContextJSON{Forges: views}

	// Recent operations (if log is available and non-empty).
	if p.opLog != nil {
		ops := p.opLog.Recent(10)
		if len(ops) > 0 {
			output.RecentOps = make([]recentOpJSON, len(ops))
			for i, op := range ops {
				output.RecentOps[i] = recentOpJSON{
					Tool:    op.Tool,
					Account: op.Account,
					Repo:    op.Repo,
					Ref:     op.Ref,
					Ago:     promptfmt.FormatDeltaOnly(op.Timestamp, now),
				}
			}
		}
	}

	data, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("marshal forge context: %w", err)
	}
	return string(data), nil
}
