package email

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// maxContextFolders caps the folder names rendered per account. Roles
// sort first so the folders a model needs to file into survive the cap
// on a label-heavy mailbox; the truncation flag says when names were
// withheld.
const maxContextFolders = 16

// maxContextRecentOps caps the recent operations rendered.
const maxContextRecentOps = 5

// ContextProvider renders the Email Accounts block: every account the
// caller may reach, what it can do, its folder vocabulary, and the
// most recent operations. It implements agent.TagContextProvider by
// structural typing and is registered on the email capability tag.
type ContextProvider struct {
	service *Service
}

func newContextProvider(service *Service) *ContextProvider {
	return &ContextProvider{service: service}
}

// TagContextBucket places the block in live state: it reflects current
// configuration and the current folder cache, not guidance.
func (p *ContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// emailContextJSON is the block's payload.
type emailContextJSON struct {
	Accounts []accountView `json:"accounts"`

	// BindingError explains an empty account list that is a
	// misconfiguration rather than an absence of email. It rides
	// inside the JSON so the header-then-JSON shape never changes.
	BindingError string `json:"binding_error,omitempty"`

	RecentOps []recentOpJSON `json:"recent_operations,omitempty"`
}

// accountView is one account as the model sees it. Passwords are never
// included.
type accountView struct {
	Account     string `json:"account"`
	Address     string `json:"address,omitempty"`
	Description string `json:"description,omitempty"`

	// CanSend is whether the account has SMTP configured.
	CanSend bool `json:"can_send"`

	SentFolder string `json:"sent_folder,omitempty"`

	// Bound marks the account as the one this caller is restricted to,
	// so the narrowed list reads as a boundary rather than as the
	// whole of what the site has configured.
	Bound bool `json:"bound,omitempty"`

	// Folders is the cached folder vocabulary, or null when no listing
	// has succeeded yet (never an empty array, which would read as "no
	// folders").
	Folders          []folderView `json:"folders"`
	FoldersAsOf      string       `json:"folders_as_of,omitempty"`
	FoldersTruncated bool         `json:"folders_truncated,omitempty"`
}

type folderView struct {
	Name string     `json:"name"`
	Role FolderRole `json:"role,omitempty"`
}

type recentOpJSON struct {
	Tool    string `json:"tool"`
	Account string `json:"account"`
	Folder  string `json:"folder,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Ago     string `json:"ago"`
}

// TagContext renders the block, narrowed to the bound account when the
// caller carries an email_account binding.
func (p *ContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	return p.buildContext(boundAccount(ctx))
}

func (p *ContextProvider) buildContext(bound string) (string, error) {
	if p == nil || p.service == nil {
		return "", nil
	}
	accounts := p.service.AccountsInConfigOrder()
	if len(accounts) == 0 {
		return "", nil
	}
	now := time.Now()

	views := make([]accountView, 0, len(accounts))
	for _, cfg := range accounts {
		if bound != "" && cfg.Name != bound {
			continue
		}
		view := accountView{
			Account:     cfg.Name,
			Description: cfg.Description,
			CanSend:     cfg.SMTPConfigured(),
			SentFolder:  cfg.SentFolder,
			Bound:       bound != "",
		}
		if cfg.DefaultFrom != "" {
			if addr, err := parseAddress(cfg.DefaultFrom); err == nil {
				view.Address = addr.Address
			}
		}
		if snap, ok := p.service.cachedFolders(cfg.Name); ok {
			view.Folders, view.FoldersTruncated = folderViews(snap.Folders)
			view.FoldersAsOf = promptfmt.FormatDeltaOnly(snap.At, now)
		}
		views = append(views, view)
	}

	output := emailContextJSON{Accounts: views}
	if len(views) == 0 && bound != "" {
		output.BindingError = "This loop is bound to email account " + bound +
			", which is not configured at this site. No email operation can succeed until the operator restores the account or changes the binding."
	}

	if ops := p.service.opLog.Recent(defaultOpLogSize); len(ops) > 0 {
		for _, op := range ops {
			if bound != "" && op.Account != bound {
				continue
			}
			output.RecentOps = append(output.RecentOps, recentOpJSON{
				Tool:    op.Tool,
				Account: op.Account,
				Folder:  op.Folder,
				Ref:     op.Ref,
				Ago:     promptfmt.FormatDeltaOnly(op.Timestamp, now),
			})
			if len(output.RecentOps) == maxContextRecentOps {
				break
			}
		}
	}

	data, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("marshal email context: %w", err)
	}
	return "### Email Accounts\n\n" + string(data) + "\n", nil
}

// folderViews projects a listing for the block: selectable folders
// only, role-bearing folders first, then alphabetical, capped at
// maxContextFolders.
func folderViews(folders []Folder) ([]folderView, bool) {
	selectable := make([]Folder, 0, len(folders))
	for _, f := range folders {
		if f.Selectable {
			selectable = append(selectable, f)
		}
	}
	slices.SortStableFunc(selectable, func(a, b Folder) int {
		aRole, bRole := a.Role != "", b.Role != ""
		if aRole != bRole {
			if aRole {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	truncated := len(selectable) > maxContextFolders
	if truncated {
		selectable = selectable[:maxContextFolders]
	}
	views := make([]folderView, len(selectable))
	for i, f := range selectable {
		views[i] = folderView{Name: f.Name, Role: f.Role}
	}
	return views, truncated
}
