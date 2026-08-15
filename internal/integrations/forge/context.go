package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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

	RepositoryRoots []repositoryRootView `json:"repository_roots,omitempty"`

	// BindingError explains an empty account list that is a
	// misconfiguration rather than an absence of forges. It rides
	// inside the JSON rather than as prose beside it because the block
	// is header-then-JSON everywhere else, and a reader that has
	// learned to parse what follows the header should not meet a
	// different shape on the one path that reports a broken boundary.
	BindingError string `json:"binding_error,omitempty"`

	RepositoryRootBindingError string `json:"repository_root_binding_error,omitempty"`

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

type repositoryRootView struct {
	Root          string `json:"root"`
	Account       string `json:"account"`
	Repo          string `json:"repo"`
	Remote        string `json:"remote"`
	Branch        string `json:"branch"`
	Commit        string `json:"commit,omitempty"`
	LastSyncedAge string `json:"last_synced_age,omitempty"`
	Bound         bool   `json:"bound,omitempty"`
}

// TagContext returns the forge context block for tag-gated injection.
// Implements [agent.TagContextProvider].
func (p *ContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	return p.buildContext(boundAccount(ctx), boundRepositoryRoot(ctx))
}

// buildContext renders the account block. When bound is non-empty the
// block narrows to that account: the tools will refuse every other one,
// and advertising an account the caller cannot use teaches a door that
// is painted on — the reader spends a turn discovering by refusal what
// the prompt could have told it for free.
func (p *ContextProvider) buildContext(bound, boundRoot string) (string, error) {
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
	allowedRootAccount := ""
	allowedRootRepo := ""

	// A binding naming an account that is not configured would
	// otherwise render an empty list, which reads as "no forge here"
	// rather than "this loop is misconfigured". Hydration refuses this
	// at boot; saying it plainly covers a live account removal.
	if len(views) == 0 && bound != "" {
		output.BindingError = "This loop is bound to forge account " + bound +
			", which is not configured at this site. No forge operation can succeed until the operator restores the account or changes the binding."
	}

	if p.service.subscriptions != nil {
		subs, err := p.service.subscriptions.List()
		if err != nil {
			return "", fmt.Errorf("list repository roots for forge context: %w", err)
		}
		for _, sub := range subs {
			if sub.RepositoryRoot == "" {
				continue
			}
			if bound != "" && sub.Account != bound {
				continue
			}
			if boundRoot != "" && sub.RepositoryRoot != boundRoot {
				continue
			}
			view := repositoryRootView{
				Root:    sub.RepositoryRoot,
				Account: sub.Account,
				Repo:    sub.Repo,
				Remote:  modelFacingRepositoryRemote(sub.CheckoutRemoteURL),
				Branch:  sub.Branch,
				Commit:  sub.LastSyncedSHA,
				Bound:   boundRoot != "",
			}
			if !sub.LastSyncedAt.IsZero() {
				view.LastSyncedAge = promptfmt.FormatDeltaOnly(sub.LastSyncedAt, now)
			}
			output.RepositoryRoots = append(output.RepositoryRoots, view)
			if boundRoot != "" {
				allowedRootAccount = sub.Account
				allowedRootRepo = sub.Repo
			}
		}
	}
	if boundRoot != "" && len(output.RepositoryRoots) == 0 {
		output.RepositoryRootBindingError = "This loop is bound to repository root " + boundRoot +
			", which is not available under its current forge-account binding. No file or repository-history operation can succeed until the operator restores the subscription or changes the binding."
	}

	// Recent operations (if log is available and non-empty).
	//
	// The operation log is instance-wide, so a bound caller must not read it
	// whole. Account bindings exclude other credentials; root bindings also
	// exclude operations for sibling repositories on the same account.
	if p.opLog != nil {
		ops := p.opLog.Recent(10)
		if bound != "" || boundRoot != "" {
			filtered := make([]Operation, 0, len(ops))
			for _, op := range ops {
				if bound != "" && op.Account != bound {
					continue
				}
				if boundRoot != "" && (op.Account != allowedRootAccount || op.Repo != allowedRootRepo) {
					continue
				}
				filtered = append(filtered, op)
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

func modelFacingRepositoryRemote(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}
