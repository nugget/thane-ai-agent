package forge

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	toolpkg "github.com/nugget/thane-ai-agent/internal/tools"
)

func TestNewServiceOwnsForgeRuntime(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	state, err := opstate.NewStore(db, discardLogger())
	if err != nil {
		t.Fatalf("create operational state: %v", err)
	}

	service, err := NewService(Config{
		SubscriptionCheckInterval: 60,
		Accounts: []AccountConfig{{
			Name:     "primary",
			Provider: "github",
			Token:    "test-token",
			Owner:    "thane",
		}},
	}, ServiceDependencies{
		State:      state,
		MessageBus: messages.NewBus(discardLogger()),
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	resolved, err := service.ResolveAccount(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveAccount: %v", err)
	}
	if resolved.Name != "primary" || resolved.Config.Owner != "thane" {
		t.Errorf("resolved account = %#v, want primary account with thane owner", resolved)
	}

	provider := service.ToolProvider()
	if provider == nil || provider.Name() != "forge" {
		t.Fatalf("ToolProvider() = %#v, want forge provider", provider)
	}
	if provider.service != service {
		t.Fatal("ToolProvider() does not share its owning forge service")
	}
	if got := len(provider.Tools()); got != 21 {
		t.Fatalf("ToolProvider declared %d tools, want 21", got)
	}
	contextProvider := service.ContextProvider()
	if contextProvider == nil {
		t.Fatal("ContextProvider() returned nil")
	}
	if contextProvider.service != service {
		t.Fatal("ContextProvider() does not share its owning forge service")
	}
	if !service.SubscriptionPollingEnabled() {
		t.Fatal("SubscriptionPollingEnabled() = false, want true for positive interval")
	}

	registry := toolpkg.NewEmptyRegistry()
	registry.RegisterProvider(provider)
	for _, name := range []string{
		"forge_issue_get",
		"forge_pr_diff",
		"forge_repo_follow",
		"forge_search",
	} {
		if registry.Get(name) == nil {
			t.Errorf("forge provider did not register %q", name)
		}
	}

	accountParameters := 0
	if !strings.Contains(forgeAccountDescription, "bound account") {
		t.Fatalf("forge account description does not teach binding behavior: %q", forgeAccountDescription)
	}
	if strings.Contains(forgeAccountDescription, "default: primary") {
		t.Fatalf("forge account description promises the primary account unconditionally: %q", forgeAccountDescription)
	}
	seen := make(map[string]bool, len(provider.Tools()))
	for _, tool := range provider.Tools() {
		if tool == nil {
			t.Fatal("forge provider declared a nil tool")
		}
		if tool.Handler == nil {
			t.Errorf("%s has a nil handler", tool.Name)
		}
		if seen[tool.Name] {
			t.Errorf("forge provider declared duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true

		properties, _ := tool.Parameters["properties"].(map[string]any)
		account, ok := properties["account"].(map[string]any)
		if !ok {
			continue
		}
		accountParameters++
		if got, _ := account["description"].(string); got != forgeAccountDescription {
			t.Errorf("%s account description = %q, want %q", tool.Name, got, forgeAccountDescription)
		}
	}
	if accountParameters == 0 {
		t.Fatal("forge provider declared no account-bearing tools")
	}
}

func TestNewServiceDisablesSubscriptionPollingAtZero(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	state, err := opstate.NewStore(db, discardLogger())
	if err != nil {
		t.Fatalf("create operational state: %v", err)
	}

	service, err := NewService(Config{
		Accounts: []AccountConfig{{Name: "primary", Provider: "github", Token: "test-token"}},
	}, ServiceDependencies{
		State:      state,
		MessageBus: messages.NewBus(discardLogger()),
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if service.SubscriptionPollingEnabled() {
		t.Fatal("SubscriptionPollingEnabled() = true, want false for zero interval")
	}
	if _, err := service.CheckSubscriptions(t.Context()); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("CheckSubscriptions error = %v, want disabled error", err)
	}
}

func TestNewServiceRequiresRuntimeDependencies(t *testing.T) {
	t.Parallel()

	cfg := Config{Accounts: []AccountConfig{{
		Name:     "primary",
		Provider: "github",
		Token:    "test-token",
	}}}
	if _, err := NewService(cfg, ServiceDependencies{}); err == nil || !strings.Contains(err.Error(), "operational state") {
		t.Fatalf("NewService without state error = %v, want operational state error", err)
	}

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	state, err := opstate.NewStore(db, discardLogger())
	if err != nil {
		t.Fatalf("create operational state: %v", err)
	}
	if _, err := NewService(cfg, ServiceDependencies{State: state}); err == nil || !strings.Contains(err.Error(), "message bus") {
		t.Fatalf("NewService without message bus error = %v, want message bus error", err)
	}
}
