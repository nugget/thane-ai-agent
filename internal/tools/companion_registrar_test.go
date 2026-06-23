package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

type fakeCompanionSource struct {
	infos  []companion.ProviderInfo
	calls  []companion.CallRequest
	result json.RawMessage
	err    error
}

func (f *fakeCompanionSource) List() []companion.ProviderInfo { return f.infos }

func (f *fakeCompanionSource) Call(_ context.Context, req companion.CallRequest) (json.RawMessage, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}

func contactsProvider() companion.ProviderInfo {
	return companion.ProviderInfo{
		Account:    "aimee",
		ClientName: "pocket",
		ClientID:   "uuid-a",
		Capabilities: []companion.Capability{{
			Name:    "macos.contacts",
			Methods: []string{"search_contacts"},
			Tools: []companion.ToolDefinition{{
				Name:        "macos_search_contacts",
				Description: "Search the user's macOS Contacts.",
				Method:      "search_contacts",
				Tags:        []string{"people"},
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			}},
		}},
	}
}

func findTool(tools []*Tool, name string) *Tool {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

func TestCompanionRegistrar_Synthesize(t *testing.T) {
	src := &fakeCompanionSource{infos: []companion.ProviderInfo{contactsProvider()}}
	cr := newCompanionRegistrar(src.List, src.Call, nil)

	synth, tagAdds := cr.Snapshot()
	if len(synth) != 1 {
		t.Fatalf("synthesized tools: got %d, want 1", len(synth))
	}
	tool := synth[0]
	if tool.Name != "macos_search_contacts" {
		t.Errorf("name: got %q", tool.Name)
	}
	if tool.Source != companionToolSource {
		t.Errorf("source: got %q, want %q", tool.Source, companionToolSource)
	}
	if tool.CanonicalID != "companion:macos_search_contacts" {
		t.Errorf("canonical id: got %q", tool.CanonicalID)
	}
	// Forced companion tag plus the Mac-authored people tag.
	if !hasString(tool.Tags, "companion") || !hasString(tool.Tags, "people") {
		t.Errorf("tags: got %v, want companion+people", tool.Tags)
	}
	// Routing hints injected into the schema.
	props, _ := tool.Parameters["properties"].(map[string]any)
	for _, key := range []string{"query", "account", "client_id"} {
		if _, ok := props[key]; !ok {
			t.Errorf("schema missing property %q: %v", key, props)
		}
	}
	// Tag additions index both tags to the tool.
	if !hasString(tagAdds["companion"], "macos_search_contacts") {
		t.Errorf("tagAdds[companion]: got %v", tagAdds["companion"])
	}
	if !hasString(tagAdds["people"], "macos_search_contacts") {
		t.Errorf("tagAdds[people]: got %v", tagAdds["people"])
	}
}

func TestCompanionRegistrar_DispatchRoutesAndStripsHints(t *testing.T) {
	src := &fakeCompanionSource{
		infos:  []companion.ProviderInfo{contactsProvider()},
		result: json.RawMessage(`{"contacts":[{"name":"Bob"}]}`),
	}
	cr := newCompanionRegistrar(src.List, src.Call, nil)
	synth, _ := cr.Snapshot()
	tool := findTool(synth, "macos_search_contacts")

	out, err := tool.Handler(context.Background(), map[string]any{
		"query":     "bob",
		"account":   "aimee",
		"client_id": "uuid-a",
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Default formatter is JSON passthrough.
	if out != `{"contacts":[{"name":"Bob"}]}` {
		t.Errorf("passthrough output: got %q", out)
	}

	if len(src.calls) != 1 {
		t.Fatalf("calls: got %d, want 1", len(src.calls))
	}
	call := src.calls[0]
	if call.Capability != "macos.contacts" || call.Method != "search_contacts" {
		t.Errorf("routing: got %s/%s", call.Capability, call.Method)
	}
	if call.Account != "aimee" || call.ClientID != "uuid-a" {
		t.Errorf("routing hints: got account=%q client_id=%q", call.Account, call.ClientID)
	}
	// account/client_id are routing hints, not forwarded params.
	var params map[string]any
	if err := json.Unmarshal(call.Params, &params); err != nil {
		t.Fatalf("decode forwarded params: %v", err)
	}
	if _, ok := params["account"]; ok {
		t.Error("account should not be forwarded as a param")
	}
	if _, ok := params["client_id"]; ok {
		t.Error("client_id should not be forwarded as a param")
	}
	if params["query"] != "bob" {
		t.Errorf("query not forwarded: %v", params)
	}
}

func TestCompanionRegistrar_DispatchSurfacesError(t *testing.T) {
	src := &fakeCompanionSource{
		infos: []companion.ProviderInfo{contactsProvider()},
		err:   errors.New("provider_disconnected: companion app disconnected"),
	}
	cr := newCompanionRegistrar(src.List, src.Call, nil)
	synth, _ := cr.Snapshot()
	tool := findTool(synth, "macos_search_contacts")

	if _, err := tool.Handler(context.Background(), map[string]any{"query": "x"}); err == nil {
		t.Fatal("expected dispatch to surface the disconnect error")
	}
}

func TestCompanionRegistrar_CalendarFormatter(t *testing.T) {
	info := companion.ProviderInfo{
		Account: "aimee",
		Capabilities: []companion.Capability{{
			Name:    "macos.calendar",
			Methods: []string{"list_events"},
			Tools: []companion.ToolDefinition{{
				Name:        "macos_calendar_events",
				Method:      "list_events",
				InputSchema: map[string]any{"type": "object"},
			}},
		}},
	}
	src := &fakeCompanionSource{
		infos:  []companion.ProviderInfo{info},
		result: json.RawMessage(`{"events":[{"title":"Standup","calendar":"Work","start":"2026-06-23T09:00:00Z","end":"2026-06-23T09:15:00Z"}]}`),
	}
	cr := newCompanionRegistrar(src.List, src.Call, nil)
	synth, _ := cr.Snapshot()
	tool := findTool(synth, "macos_calendar_events")

	out, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Named formatter renders prose, not raw JSON.
	if strings.HasPrefix(out, "{") || !strings.Contains(out, "Standup") {
		t.Errorf("calendar formatter should render prose with the event title, got %q", out)
	}
}

func TestCompanionRegistrar_DedupAcrossProviders(t *testing.T) {
	mk := func(account, desc string) companion.ProviderInfo {
		return companion.ProviderInfo{
			Account: account,
			Capabilities: []companion.Capability{{
				Name: "macos.contacts",
				Tools: []companion.ToolDefinition{{
					Name: "macos_search_contacts", Description: desc, Method: "search_contacts",
					InputSchema: map[string]any{"type": "object"},
				}},
			}},
		}
	}
	src := &fakeCompanionSource{infos: []companion.ProviderInfo{mk("aimee", "first"), mk("nugget", "second")}}
	cr := newCompanionRegistrar(src.List, src.Call, nil)
	synth, tagAdds := cr.Snapshot()
	if len(synth) != 1 {
		t.Fatalf("dedup: got %d tools, want 1", len(synth))
	}
	// tagAdds must not list the deduped name twice.
	if got := tagAdds["companion"]; len(got) != 1 {
		t.Errorf("tagAdds[companion]: got %v, want one entry", got)
	}
}

func TestCompanionRegistrar_RebuildOnConnectAndDisconnect(t *testing.T) {
	src := &fakeCompanionSource{}
	cr := newCompanionRegistrar(src.List, src.Call, nil)

	if synth, _ := cr.Snapshot(); len(synth) != 0 {
		t.Fatalf("initial snapshot should be empty, got %d", len(synth))
	}

	// Connect.
	src.infos = []companion.ProviderInfo{contactsProvider()}
	cr.Rebuild()
	if synth, _ := cr.Snapshot(); len(synth) != 1 {
		t.Fatalf("after connect: got %d tools, want 1", len(synth))
	}

	// Disconnect.
	src.infos = nil
	cr.Rebuild()
	if synth, tagAdds := cr.Snapshot(); len(synth) != 0 || len(tagAdds) != 0 {
		t.Fatalf("after disconnect: got %d tools / %d tags, want 0/0", len(synth), len(tagAdds))
	}
}

func TestAugmentSchemaWithRouting_DoesNotMutateSource(t *testing.T) {
	orig := map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
	}
	_ = augmentSchemaWithRouting(orig)

	props := orig["properties"].(map[string]any)
	if _, ok := props["account"]; ok {
		t.Error("augmentSchemaWithRouting mutated the source schema properties")
	}
	if len(props) != 1 {
		t.Errorf("source properties mutated: %v", props)
	}
}

func hasString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
