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
	service *Service
	opLog   *OperationLog
}

// NewContextProvider creates a forge context provider. When opLog is
// non-nil, recent operations are included in the context each turn.
func NewContextProvider(mgr *Manager, opLog *OperationLog) *ContextProvider {
	return newContextProvider(&Service{manager: mgr}, opLog)
}

func newContextProvider(service *Service, opLog *OperationLog) *ContextProvider {
	return &ContextProvider{
		service: service,
		opLog:   opLog,
	}
}

// TagContextBucket places forge account config and recent operations in
// live state because the output reflects current runtime configuration.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// forgeContextJSON is the JSON structure emitted by the provider.
type forgeContextJSON struct {
	Forges []accountView `json:"forges"`

	// BindingError explains an empty account list that is a
	// misconfiguration rather than an absence of forges. It rides
	// inside the JSON rather than as prose beside it because the block
	// is header-then-JSON everywhere else, and a reader that has
	// learned to parse what follows the header should not meet a
	// different shape on the one path that reports a broken boundary.
	BindingError string `json:"binding_error,omitempty"`

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
func (p *ContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	return p.buildContext(boundAccount(ctx))
}

// buildContext renders the account block. When bound is non-empty the
// block narrows to that account: the tools will refuse every other one,
// and advertising an account the caller cannot use teaches a door that
// is painted on — the reader spends a turn discovering by refusal what
// the prompt could have told it for free.
func (p *ContextProvider) buildContext(bound string) (string, error) {
	accounts := p.service.AccountsInConfigOrder()
	if len(accounts) == 0 {
		return "", nil
	}

	now := time.Now()

	// Account config.
	views := make([]accountView, 0, len(accounts))
	for _, cfg := range accounts {
		if bound != "" && cfg.Name != bound {
			continue
		}
		views = append(views, accountView{
			Account:      cfg.Name,
			Type:         cfg.Provider,
			URL:          cfg.URL,
			DefaultOwner: cfg.Owner,
			Description:  cfg.Description,
			Bound:        bound != "",
		})
	}

	output := forgeContextJSON{Forges: views}

	// A binding naming an account that is not configured would
	// otherwise render an empty list, which reads as "no forge here"
	// rather than "this loop is misconfigured". Hydration refuses this
	// at boot; saying it plainly covers a live account removal.
	if len(views) == 0 && bound != "" {
		output.BindingError = "This loop is bound to forge account " + bound +
			", which is not configured at this site. No forge operation can succeed until the operator restores the account or changes the binding."
	}

	// Recent operations (if log is available and non-empty).
	//
	// The operation log is instance-wide, so a bound caller must not
	// read it whole: repository and ref names from another account are
	// exactly the activity the binding exists to keep out of this
	// loop's context, and narrowing the account list while leaking the
	// operations underneath it would be a boundary in name only.
	if p.opLog != nil {
		ops := p.opLog.Recent(10)
		if bound != "" {
			filtered := make([]Operation, 0, len(ops))
			for _, op := range ops {
				if op.Account == bound {
					filtered = append(filtered, op)
				}
			}
			ops = filtered
		}
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
	return "### Forge Accounts\n\n" + string(data) + "\n", nil
}
