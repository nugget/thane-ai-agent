package forge

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

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
	return &Tools{manager: mgr, logger: logger}
}

func boundCtx(account string) context.Context {
	return looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: account,
	})
}

func TestResolveAccountArgHonorsBinding(t *testing.T) {
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
			wantAccount: "",
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
			got, err := tools.resolveAccountArg(tt.ctx, tt.account)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("resolveAccountArg() = %q, want an error", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q\nmissing substring %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAccountArg() unexpected error: %v", err)
			}
			if got != tt.wantAccount {
				t.Errorf("resolveAccountArg() = %q, want %q", got, tt.wantAccount)
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
	provider := NewContextProvider(tools.manager, nil)

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

	t.Run("a binding to a vanished account says so", func(t *testing.T) {
		t.Parallel()
		out, err := provider.TagContext(boundCtx("github-retired"), agentctx.ContextRequest{})
		if err != nil {
			t.Fatalf("TagContext() unexpected error: %v", err)
		}
		if !strings.Contains(out, "not configured") {
			t.Errorf("context = %q\nwant an explicit statement that the bound account is missing", out)
		}
	})
}
