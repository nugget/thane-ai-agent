package forge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestContextProvider_AccountConfig(t *testing.T) {
	t.Parallel()

	mgr := &Manager{
		configs: map[string]AccountConfig{
			"github-primary": {Name: "github-primary", Provider: "github", URL: "https://api.github.com", Owner: "nugget"},
		},
		order: []string{"github-primary"},
	}

	p := NewContextProvider(mgr, nil)
	got, err := p.TagContext(context.Background())
	if err != nil {
		t.Fatalf("TagContext() error: %v", err)
	}

	if !strings.Contains(got, `"github-primary"`) {
		t.Error("should contain account name")
	}
	if !strings.Contains(got, `"default_owner":"nugget"`) {
		t.Error("should contain default owner")
	}

	// Should be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output should be valid JSON: %v\nGot: %s", err, got)
	}
}

func TestContextProvider_WithRecentOps(t *testing.T) {
	t.Parallel()

	mgr := &Manager{
		configs: map[string]AccountConfig{
			"github-primary": {Name: "github-primary", Provider: "github", Owner: "nugget"},
		},
		order: []string{"github-primary"},
	}

	opLog := NewOperationLog()
	opLog.Record(Operation{Tool: "forge_pr_get", Account: "github-primary", Repo: "nugget/thane", Ref: "#42"})
	opLog.Record(Operation{Tool: "forge_issue_update", Account: "github-primary", Repo: "nugget/thane", Ref: "#100"})

	p := NewContextProvider(mgr, opLog)
	got, err := p.TagContext(context.Background())
	if err != nil {
		t.Fatalf("TagContext() error: %v", err)
	}

	var parsed struct {
		Forges    []any `json:"forges"`
		RecentOps []struct {
			Tool    string `json:"tool"`
			Account string `json:"account"`
			Repo    string `json:"repo"`
			Ref     string `json:"ref"`
			Ago     string `json:"ago"`
		} `json:"recent_operations"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("output should be valid JSON: %v\nGot: %s", err, got)
	}

	if len(parsed.RecentOps) != 2 {
		t.Fatalf("expected 2 recent ops, got %d", len(parsed.RecentOps))
	}
	// Newest first.
	if parsed.RecentOps[0].Tool != "forge_issue_update" {
		t.Errorf("newest op should be forge_issue_update, got %q", parsed.RecentOps[0].Tool)
	}
	if parsed.RecentOps[0].Ref != "#100" {
		t.Errorf("ref should be #100, got %q", parsed.RecentOps[0].Ref)
	}
	// Ago should be a delta string.
	if !strings.HasPrefix(parsed.RecentOps[0].Ago, "-") {
		t.Errorf("ago should be negative delta, got %q", parsed.RecentOps[0].Ago)
	}
}

func TestContextProvider_NoOps(t *testing.T) {
	t.Parallel()

	mgr := &Manager{
		configs: map[string]AccountConfig{
			"github": {Name: "github", Provider: "github"},
		},
		order: []string{"github"},
	}

	p := NewContextProvider(mgr, NewOperationLog())
	got, err := p.TagContext(context.Background())
	if err != nil {
		t.Fatalf("TagContext() error: %v", err)
	}

	// Should NOT contain recent_operations key when empty.
	if strings.Contains(got, "recent_operations") {
		t.Error("empty oplog should not emit recent_operations field")
	}
}

func TestContextProvider_NilManager(t *testing.T) {
	t.Parallel()

	p := NewContextProvider(nil, nil)
	got, err := p.TagContext(context.Background())
	if err != nil {
		t.Fatalf("TagContext() error: %v", err)
	}
	if got != "" {
		t.Errorf("nil manager should return empty, got %q", got)
	}
}
