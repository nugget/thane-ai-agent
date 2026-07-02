package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func TestContextProvider_Bucket(t *testing.T) {
	p := NewContextProvider(NewRegistry(nil))
	if got := p.TagContextBucket(); got != agentctx.ContextBucketLiveState {
		t.Errorf("bucket: got %v, want live state", got)
	}
}

func TestContextProvider_Empty(t *testing.T) {
	p := NewContextProvider(NewRegistry(nil))
	out, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	want := "### Connected Companions\n\n{\"companions\":[]}\n"
	if out != want {
		t.Errorf("empty registry: got %q, want headed explicit empty array", out)
	}
}

func TestContextProvider_ListsConnected(t *testing.T) {
	r := NewRegistry(nil)
	p := &Provider{
		ID:          "p1",
		Account:     "aimee",
		ClientName:  "pocket",
		ClientID:    "uuid-a",
		ConnectedAt: time.Now().Add(-5 * time.Minute),
		done:        make(chan struct{}),
	}
	r.Add(p)
	if err := r.RegisterCapabilities("p1", []Capability{{
		Name: "macos.contacts",
		Tools: []ToolDefinition{
			{Name: "macos_search_contacts", Method: "search_contacts"},
		},
	}}); err != nil {
		t.Fatalf("RegisterCapabilities: %v", err)
	}

	out, err := NewContextProvider(r).TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	payload := strings.TrimSpace(strings.TrimPrefix(out, "### Connected Companions\n\n"))
	var ctx companionContextJSON
	if err := json.Unmarshal([]byte(payload), &ctx); err != nil {
		t.Fatalf("decode context: %v (%s)", err, out)
	}
	if len(ctx.Companions) != 1 {
		t.Fatalf("companions: got %d, want 1", len(ctx.Companions))
	}
	c := ctx.Companions[0]
	if c.Account != "aimee" || c.ClientName != "pocket" {
		t.Errorf("identity: got account=%q client=%q", c.Account, c.ClientName)
	}
	if len(c.Tools) != 1 || c.Tools[0] != "macos_search_contacts" {
		t.Errorf("tools: got %v", c.Tools)
	}
	if c.ConnectedAgo == "" {
		t.Error("connected_ago should be populated with a delta")
	}
}
