package forge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// newMultiAccountTools builds a manager with a primary write account and
// a read-only second account — the shape a deployment takes when it
// wants one loop watching through a narrower credential.
func newMultiAccountTools() *Tools {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := &Manager{
		providers: map[string]ForgeProvider{
			"github-primary":  &mockProvider{name: "github-primary"},
			"github-readonly": &mockProvider{name: "github-readonly"},
		},
		configs: map[string]AccountConfig{
			"github-primary":  {Name: "github-primary", Owner: "nugget", Description: "Full access."},
			"github-readonly": {Name: "github-readonly", Owner: "nugget", Description: "Observation only."},
		},
		order:  []string{"github-primary", "github-readonly"},
		logger: logger,
	}
	return NewTools(mgr, nil, logger, nil)
}

func boundCtx(account string) context.Context {
	return looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: account,
	})
}

func TestServiceResolveAccountHonorsBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ctx         context.Context
		account     string
		wantAccount string
		wantErr     []string
	}{
		{
			// The default path. Without a binding this resolves to the
			// primary account exactly as it always has, which is what
			// keeps interactive turns and API callers unchanged.
			name:        "unbound caller keeps its own choice",
			ctx:         context.Background(),
			account:     "",
			wantAccount: "github-primary",
		},
		{
			name:        "unbound caller may still name an account",
			ctx:         context.Background(),
			account:     "github-readonly",
			wantAccount: "github-readonly",
		},
		{
			// The whole point: an omitted argument must not fall through
			// to the primary account when the caller is bound.
			name:        "bound caller defaults to its binding, not primary",
			ctx:         boundCtx("github-readonly"),
			account:     "",
			wantAccount: "github-readonly",
		},
		{
			name:        "bound caller may name its own account",
			ctx:         boundCtx("github-readonly"),
			account:     "github-readonly",
			wantAccount: "github-readonly",
		},
		{
			// Without this refusal the binding would be a suggestion:
			// every forge tool takes an account argument, so the model
			// could simply ask for the write credential by name.
			name:    "bound caller cannot name another account",
			ctx:     boundCtx("github-readonly"),
			account: "github-primary",
			wantErr: []string{"github-primary", "github-readonly", "bound"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tools := newMultiAccountTools()
			got, err := tools.service.ResolveAccount(tt.ctx, tt.account)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("ResolveAccount() = %#v, want an error", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q\nmissing substring %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAccount() unexpected error: %v", err)
			}
			if got.Name != tt.wantAccount {
				t.Errorf("ResolveAccount().Name = %q, want %q", got.Name, tt.wantAccount)
			}
		})
	}
}

func TestResolveAccountAndRepoAppliesBinding(t *testing.T) {
	t.Parallel()

	t.Run("empty account resolves to the binding", func(t *testing.T) {
		t.Parallel()
		tools := newMultiAccountTools()
		provider, repo, acct, err := tools.resolveAccountAndRepo(boundCtx("github-readonly"), baseArgs("thane"))
		if err != nil {
			t.Fatalf("resolveAccountAndRepo() unexpected error: %v", err)
		}
		if acct != "github-readonly" {
			t.Errorf("account = %q, want %q", acct, "github-readonly")
		}
		if got := provider.Name(); got != "github-readonly" {
			t.Errorf("provider = %q, want the bound account's provider", got)
		}
		if repo != "nugget/thane" {
			t.Errorf("repo = %q, want %q", repo, "nugget/thane")
		}
	})

	t.Run("a different account is refused before any call", func(t *testing.T) {
		t.Parallel()
		tools := newMultiAccountTools()
		_, _, _, err := tools.resolveAccountAndRepo(
			boundCtx("github-readonly"),
			map[string]any{"repo": "thane", "account": "github-primary"},
		)
		if err == nil {
			t.Fatal("resolveAccountAndRepo() succeeded, want a refusal")
		}
		if !strings.Contains(err.Error(), "github-readonly") {
			t.Errorf("error = %q, want it to name the bound account", err.Error())
		}
	})
}

func TestContextProviderNarrowsToBoundAccount(t *testing.T) {
	t.Parallel()

	tools := newMultiAccountTools()
	provider := newContextProvider(tools.service, nil)

	t.Run("unbound sees every account", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(context.Background(), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext() unexpected error: %v", err)
		}
		for _, want := range []string{"github-primary", "github-readonly"} {
			if !strings.Contains(out, want) {
				t.Errorf("context = %q\nmissing account %q", out, want)
			}
		}
	})

	t.Run("bound sees only its own", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(boundCtx("github-readonly"), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext() unexpected error: %v", err)
		}
		if !strings.Contains(out, "github-readonly") {
			t.Errorf("context = %q\nmissing the bound account", out)
		}
		// Advertising an account the tools will refuse costs the reader
		// a turn to discover by denial what the prompt could have said.
		if strings.Contains(out, "github-primary") {
			t.Errorf("context = %q\nadvertises an account this caller cannot use", out)
		}
		if !strings.Contains(out, `"bound":true`) {
			t.Errorf("context = %q\nwant the narrowed list marked as a boundary", out)
		}
	})

	t.Run("a binding to a vanished account says so, without changing the block's shape", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(boundCtx("github-retired"), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext() unexpected error: %v", err)
		}
		if !strings.Contains(out, "not configured") {
			t.Errorf("context = %q\nwant an explicit statement that the bound account is missing", out)
		}
		// The block is header-then-JSON on every other path, and a
		// reader that has learned to parse what follows the header
		// should not meet prose on the one path reporting a broken
		// boundary. This asserts the shape, not just the message.
		if !strings.HasPrefix(out, "### Forge Accounts\n\n") {
			t.Fatalf("missing the standard header: %q", out)
		}
		payload := strings.TrimSpace(strings.TrimPrefix(out, "### Forge Accounts\n\n"))
		var parsed struct {
			Forges       []map[string]any `json:"forges"`
			BindingError string           `json:"binding_error"`
		}
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			t.Fatalf("payload after the header is not JSON: %v\ngot: %s", err, out)
		}
		if len(parsed.Forges) != 0 {
			t.Errorf("forges = %v, want empty", parsed.Forges)
		}
		if !strings.Contains(parsed.BindingError, "github-retired") {
			t.Errorf("binding_error = %q, want it to name the unavailable account", parsed.BindingError)
		}
	})
}

// TestContextProviderFiltersRecentOpsByBinding covers the half of the
// context block that is not the account list. The operation log is
// instance-wide, so narrowing the roster while rendering every
// account's operations underneath it would name repositories and refs
// the binding exists to keep out of this loop's context — a boundary
// in name only.
func TestContextProviderFiltersRecentOpsByBinding(t *testing.T) {
	t.Parallel()

	tools := newMultiAccountTools()
	opLog := NewOperationLog()
	opLog.Record(Operation{Tool: "forge_pr_merge", Account: "github-primary", Repo: "nugget/secret-thing", Ref: "42"})
	opLog.Record(Operation{Tool: "forge_issue_list", Account: "github-readonly", Repo: "nugget/thane", Ref: ""})
	provider := newContextProvider(tools.service, opLog)

	t.Run("unbound sees the whole log", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(context.Background(), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext() unexpected error: %v", err)
		}
		for _, want := range []string{"nugget/secret-thing", "nugget/thane"} {
			if !strings.Contains(out, want) {
				t.Errorf("context = %q\nmissing operation on %q", out, want)
			}
		}
	})

	t.Run("bound sees only its own account's operations", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(boundCtx("github-readonly"), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext() unexpected error: %v", err)
		}
		if !strings.Contains(out, "nugget/thane") {
			t.Errorf("context = %q\nmissing this account's own operation", out)
		}
		if strings.Contains(out, "nugget/secret-thing") {
			t.Errorf("context = %q\nleaks a repository touched by another account", out)
		}
		if strings.Contains(out, "github-primary") {
			t.Errorf("context = %q\nnames an account the binding hides", out)
		}
	})
}

// TestSubscriptionToolsHonorBinding covers the two subscription tools
// that address rows by opaque ID rather than by account. Enforcing the
// binding only where an account argument happens to exist leaves the
// rest of the surface open: the listing is an inventory of every
// account's repositories, and unfollow is destructive against any row
// whose ID the caller can name.
func TestSubscriptionToolsHonorBinding(t *testing.T) {
	t.Parallel()

	newTools := func(t *testing.T) *Tools {
		t.Helper()
		tools := newMultiAccountTools()
		store := newTestSubscriptionStore(t)
		for _, sub := range []ProjectSubscription{
			{ID: "sub-ro", Account: "github-readonly", Repo: "nugget/thane", TrackReleases: true,
				WakeTarget: messages.LoopWakeTarget{Name: "watcher"}, CreatedAt: time.Now()},
			{ID: "sub-rw", Account: "github-primary", Repo: "nugget/secret-thing", TrackReleases: true,
				WakeTarget: messages.LoopWakeTarget{Name: "watcher"}, CreatedAt: time.Now()},
		} {
			if err := store.Add(sub); err != nil {
				t.Fatalf("seed %s: %v", sub.ID, err)
			}
		}
		tools.subscriptions = store
		return tools
	}

	t.Run("listing hides other accounts", func(t *testing.T) {
		t.Parallel()
		tools := newTools(t)
		out, err := tools.HandleRepoSubscriptions(boundCtx("github-readonly"), nil)
		if err != nil {
			t.Fatalf("HandleRepoSubscriptions: %v", err)
		}
		if !strings.Contains(out, "nugget/thane") {
			t.Errorf("listing = %q\nmissing this account's own subscription", out)
		}
		if strings.Contains(out, "nugget/secret-thing") || strings.Contains(out, "github-primary") {
			t.Errorf("listing = %q\nnames another account's subscription", out)
		}
	})

	t.Run("unbound listing is unchanged", func(t *testing.T) {
		t.Parallel()
		tools := newTools(t)
		out, err := tools.HandleRepoSubscriptions(context.Background(), nil)
		if err != nil {
			t.Fatalf("HandleRepoSubscriptions: %v", err)
		}
		for _, want := range []string{"nugget/thane", "nugget/secret-thing"} {
			if !strings.Contains(out, want) {
				t.Errorf("unbound listing = %q\nmissing %q", out, want)
			}
		}
	})

	t.Run("unfollow refuses another account's subscription", func(t *testing.T) {
		t.Parallel()
		tools := newTools(t)
		_, err := tools.HandleRepoUnfollow(boundCtx("github-readonly"),
			map[string]any{"subscription_id": "sub-rw"})
		if err == nil {
			t.Fatal("HandleRepoUnfollow() removed another account's subscription")
		}
		if !strings.Contains(err.Error(), "github-primary") {
			t.Errorf("error = %q, want it to name the owning account", err.Error())
		}
		// And the row must still be there.
		if _, err := tools.subscriptions.Get("sub-rw"); err != nil {
			t.Errorf("subscription was removed despite the refusal: %v", err)
		}
	})

	t.Run("unfollow allows its own subscription", func(t *testing.T) {
		t.Parallel()
		tools := newTools(t)
		if _, err := tools.HandleRepoUnfollow(boundCtx("github-readonly"),
			map[string]any{"subscription_id": "sub-ro"}); err != nil {
			t.Fatalf("HandleRepoUnfollow on own subscription: %v", err)
		}
	})
}

// TestContextCarriesExistingSubscriptions covers the gap that made a
// production loop re-follow a repository it already had. Nothing in the
// injected forge block named its subscriptions, so the reasonable
// opening move on every wake was to subscribe again — and while doing
// so it guessed a home directory that does not exist on that host.
func TestContextCarriesExistingSubscriptions(t *testing.T) {
	t.Parallel()

	tools := newMultiAccountTools()
	store := newTestSubscriptionStore(t)
	for _, sub := range []ProjectSubscription{
		{ID: "sub-ro", Account: "github-readonly", Repo: "nugget/thane", Branch: "main",
			CheckoutPath: "/srv/checkouts/thane", LastSyncedSHA: "abc123",
			TrackCommits: true, WakeTarget: messages.LoopWakeTarget{Name: "watcher"}, CreatedAt: time.Now()},
		{ID: "sub-rw", Account: "github-primary", Repo: "nugget/secret-thing", Branch: "main",
			TrackCommits: true, WakeTarget: messages.LoopWakeTarget{Name: "watcher"}, CreatedAt: time.Now()},
	} {
		if err := store.Add(sub); err != nil {
			t.Fatalf("seed %s: %v", sub.ID, err)
		}
	}
	tools.service.subscriptions = store
	provider := newContextProvider(tools.service, nil)

	t.Run("a bound loop sees its own and can tell it is already following", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(boundCtx("github-readonly"), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		for _, want := range []string{"nugget/thane", "sub-ro", "/srv/checkouts/thane", "abc123"} {
			if !strings.Contains(out, want) {
				t.Errorf("context = %q\nmissing %q", out, want)
			}
		}
		// The binding narrows subscriptions the same way it narrows accounts.
		if strings.Contains(out, "nugget/secret-thing") {
			t.Errorf("context = %q\nleaks a subscription on another account", out)
		}
	})

	t.Run("an unbound reader sees all of them", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(context.Background(), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext: %v", err)
		}
		for _, want := range []string{"nugget/thane", "nugget/secret-thing"} {
			if !strings.Contains(out, want) {
				t.Errorf("context = %q\nmissing %q", out, want)
			}
		}
	})
}
