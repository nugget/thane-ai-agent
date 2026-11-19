package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestBridge_Sanitize(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"hello":         "hello",
		"Hello-World":   "hello_world",
		"a--b":          "a_b",
		"_leading_":     "leading",
		"special!chars": "special_chars",
		"My Server":     "my_server",
		"ünïcode":       "n_code",
		"":              "",
		"---":           "",
	}
	for in, want := range tests {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBridge_ThaneToolName(t *testing.T) {
	t.Parallel()
	tests := []struct{ server, tool, want string }{
		{"home-assistant", "get_entities", "mcp_home_assistant_get_entities"},
		{"github", "create_issue", "mcp_github_create_issue"},
		{"My Server", "Do Thing", "mcp_my_server_do_thing"},
		{"test", "UPPERCASE", "mcp_test_uppercase"},
		{"a--b", "c--d", "mcp_a_b_c_d"},
		{"special!@#", "chars$%^", "mcp_special_chars"},
	}
	for _, tt := range tests {
		if got := thaneToolName(tt.server, tt.tool); got != tt.want {
			t.Errorf("thaneToolName(%q, %q) = %q, want %q", tt.server, tt.tool, got, tt.want)
		}
	}

	long := strings.Repeat("very_long_tool_name_", 5)
	a := thaneToolName("server", long+"alpha")
	b := thaneToolName("server", long+"beta")
	if len(a) > maxToolNameBytes || len(b) > maxToolNameBytes {
		t.Fatalf("long names not capped: %d %d bytes", len(a), len(b))
	}
	if a == b {
		t.Fatalf("distinct long tools share the name %q", a)
	}
	if a != thaneToolName("server", long+"alpha") {
		t.Fatal("shortened name is not stable")
	}
	if !strings.HasPrefix(a, "mcp_server_very_long_tool_name") {
		t.Errorf("shortened name lost its readable prefix: %q", a)
	}
	exact := "mcp_s_" + strings.Repeat("x", maxToolNameBytes-len("mcp_s_"))
	if got := thaneToolName("s", strings.Repeat("x", maxToolNameBytes-len("mcp_s_"))); got != exact {
		t.Errorf("a 64-byte name was changed: %q", got)
	}
}

func TestBridge_CanonicalIDKeepsRawNames(t *testing.T) {
	t.Parallel()
	dash, under := canonicalToolID("github-tools", "foo-bar"), canonicalToolID("github-tools", "foo_bar")
	if dash != "mcp:github-tools/foo-bar" || under != "mcp:github-tools/foo_bar" {
		t.Fatalf("canonical IDs = %q, %q", dash, under)
	}
	if got := canonicalToolID("a/b", "c d"); got != "mcp:a%2Fb/c%20d" {
		t.Errorf("canonicalToolID escapes = %q", got)
	}
}

// wireTBridge builds a bridge with no client (handlers are not called).
func wireTBridge(t *testing.T, cfg ServerConfig) (*serverBridge, *coreTLogBuffer) {
	t.Helper()
	logs := &coreTLogBuffer{}
	if cfg.Name == "" {
		cfg.Name = "srv"
	}
	return newServerBridge(cfg, nil, logs.logger()), logs
}

// wireTDefs builds tool definitions with an object schema.
func wireTDefs(names ...string) []toolDefinition {
	out := make([]toolDefinition, 0, len(names))
	for _, n := range names {
		out = append(out, toolDefinition{
			Name:        n,
			Description: "does " + n,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	return out
}

func wireTRawNames(bts []bridgedTool) []string {
	var out []string
	for _, bt := range bts {
		out = append(out, bt.raw)
	}
	return out
}

func TestBridge_Filtering(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	tests := []struct {
		name string
		cfg  ServerConfig
		want []string
	}{
		{name: "all by default", want: []string{"a", "b", "c"}},
		{name: "include list", cfg: ServerConfig{Include: []string{"a", "c"}}, want: []string{"a", "c"}},
		{name: "exclude list", cfg: ServerConfig{Exclude: []string{"b"}}, want: []string{"a", "c"}},
		{name: "enabled false excludes", cfg: ServerConfig{ToolOverrides: map[string]ToolOverride{"a": {Enabled: &no}}}, want: []string{"b", "c"}},
		{name: "enabled true beats include", cfg: ServerConfig{
			Include:       []string{"a"},
			ToolOverrides: map[string]ToolOverride{"c": {Enabled: &yes}},
		}, want: []string{"a", "c"}},
		{name: "enabled true beats exclude", cfg: ServerConfig{
			Exclude:       []string{"b", "c"},
			ToolOverrides: map[string]ToolOverride{"b": {Enabled: &yes}},
		}, want: []string{"a", "b"}},
		{name: "enabled false beats include", cfg: ServerConfig{
			Include:       []string{"a", "b"},
			ToolOverrides: map[string]ToolOverride{"a": {Enabled: &no}},
		}, want: []string{"b"}},
		{name: "nil enabled keeps filters", cfg: ServerConfig{
			Include:       []string{"a"},
			ToolOverrides: map[string]ToolOverride{"b": {Description: "x"}},
		}, want: []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, _ := wireTBridge(t, tt.cfg)
			got := wireTRawNames(b.bridge(wireTDefs("c", "a", "b")))
			if !slices.Equal(got, tt.want) {
				t.Errorf("bridged = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBridge_ToolShape(t *testing.T) {
	t.Parallel()
	b, _ := wireTBridge(t, ServerConfig{
		Name: "Home-Assistant",
		Tags: []string{" ha ", "", "ha", "home"},
		ToolOverrides: map[string]ToolOverride{
			"special": {Tags: []string{"admin"}, Description: "  operator text  "},
		},
	})
	defs := wireTDefs("get_state", "special")
	defs = append(defs, toolDefinition{Name: "titled", Title: "Title only", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}})
	bts := b.bridge(defs)
	if len(bts) != 3 {
		t.Fatalf("bridged %d tools, want 3", len(bts))
	}
	get, special, titled := bts[0].tool, bts[1].tool, bts[2].tool
	if get.Name != "mcp_home_assistant_get_state" || get.CanonicalID != "mcp:Home-Assistant/get_state" ||
		get.Source != "mcp" || get.Origin != "Home-Assistant" || get.Core || get.SkipContentResolve {
		t.Errorf("tool fields = %+v", get)
	}
	if !slices.Equal(get.Tags, []string{"ha", "home"}) {
		t.Errorf("server tags = %v, want [ha home]", get.Tags)
	}
	if !slices.Equal(special.Tags, []string{"admin"}) || special.Description != "operator text" {
		t.Errorf("override = tags %v description %q", special.Tags, special.Description)
	}
	if titled.Description != "Title only" {
		t.Errorf("title fallback = %q", titled.Description)
	}
}

func TestBridge_DescriptionCap(t *testing.T) {
	t.Parallel()
	b, logs := wireTBridge(t, ServerConfig{})
	defs := wireTDefs("big")
	defs[0].Description = strings.Repeat("é", maxToolDescriptionBytes) // 2 bytes per rune
	for range 2 {
		desc := b.bridge(defs)[0].tool.Description
		if len(desc) > maxToolDescriptionBytes || !strings.HasPrefix(desc, "éé") || strings.ContainsRune(desc, '�') {
			t.Fatalf("description not capped rune-safely: %d bytes", len(desc))
		}
	}
	if n := strings.Count(logs.String(), "level=WARN msg=\"mcp: tool description truncated\""); n != 1 {
		t.Errorf("truncation warned %d times, want once:\n%s", n, logs.String())
	}
}

func TestBridge_LocalCollisionKeepsFirst(t *testing.T) {
	t.Parallel()
	b, logs := wireTBridge(t, ServerConfig{})
	bts := b.bridge(wireTDefs("foo_bar", "foo-bar", "Foo.Bar", "other"))
	if got := wireTRawNames(bts); !slices.Equal(got, []string{"Foo.Bar", "other"}) {
		t.Fatalf("bridged = %v, want [Foo.Bar other] (first in raw-name order wins)", got)
	}
	if !strings.Contains(logs.String(), "same Thane tool name") {
		t.Errorf("collision not logged:\n%s", logs.String())
	}
}

func TestBridge_UnknownKeysWarnOncePerSet(t *testing.T) {
	t.Parallel()
	b, logs := wireTBridge(t, ServerConfig{
		Include:       []string{"a", "typo"},
		ToolOverrides: map[string]ToolOverride{"ghost": {Description: "x"}},
	})
	b.bridge(wireTDefs("a", "b"))
	b.bridge(wireTDefs("a", "b"))
	out := logs.String()
	if n := strings.Count(out, "does not offer"); n != 1 {
		t.Fatalf("unknown keys warned %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "ghost") || !strings.Contains(out, "typo") {
		t.Errorf("warning does not name the unknown keys:\n%s", out)
	}
	b.bridge(wireTDefs("a", "b", "typo")) // the set changes to [ghost]
	if n := strings.Count(logs.String(), "does not offer"); n != 2 {
		t.Errorf("a changed set should warn again; warned %d times", n)
	}
}

func TestBridge_SchemaNormalizedAndLoggedOnce(t *testing.T) {
	t.Parallel()
	b, logs := wireTBridge(t, ServerConfig{})
	defs := []toolDefinition{{Name: "loose", InputSchema: map[string]any{"properties": map[string]any{"x": map[string]any{"type": "string"}}}}}
	for range 3 {
		p := b.bridge(defs)[0].tool.Parameters
		if p["type"] != "object" {
			t.Fatalf("root type = %v", p["type"])
		}
	}
	if _, ok := defs[0].InputSchema["type"]; ok {
		t.Fatal("the server's schema was mutated")
	}
	if n := strings.Count(logs.String(), "level=INFO msg=\"mcp: adjusted a tool's input schema"); n != 1 {
		t.Errorf("schema change logged at INFO %d times, want once:\n%s", n, logs.String())
	}
}

func TestBridge_SkipsToolWithOversizedSchema(t *testing.T) {
	t.Parallel()
	b, logs := wireTBridge(t, ServerConfig{})
	defs := wireTDefs("huge", "small")
	defs[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{
		"q": map[string]any{"type": "string", "description": strings.Repeat("x", maxToolSchemaBytes)},
	}}
	for range 2 {
		if got := wireTRawNames(b.bridge(defs)); !slices.Equal(got, []string{"small"}) {
			t.Fatalf("bridged = %v, want [small]", got)
		}
	}
	if n := strings.Count(logs.String(), "level=WARN msg=\"mcp: tool input schema too large"); n != 1 {
		t.Errorf("oversized schema warned %d times, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "tool=huge") {
		t.Errorf("warning does not name the tool:\n%s", logs.String())
	}
}

func TestBridge_HandlerRoutesThroughClient(t *testing.T) {
	t.Parallel()
	srv := &clientTFakeServer{}
	routes := clientTLegacyRoutes("", clientTTool("echo"), clientTTool("fail"), clientTTool("silent"))
	routes[methodToolsCall] = clientTLegacyGuard(func(ctx2 context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if strings.Contains(string(req.Params), `"fail"`) {
			return clientTOK(`{"content":[{"type":"text","text":"bad input: use a number"}],"isError":true}`), nil
		}
		if strings.Contains(string(req.Params), `"silent"`) {
			return clientTOK(`{"content":[],"isError":true}`), nil
		}
		return clientTEcho(ctx2, tr, req, info)
	})
	srv.handle = routes.handler()
	c := clientTNew(t, srv, clientTConfig(transportStdio, protocolLegacy))
	b := newServerBridge(ServerConfig{Name: "fake"}, c, coreTLogger(t))

	// Not connected yet: unavailable, not an illegal-tool error.
	bts := b.bridge(wireTDefs("echo", "fail", "silent"))
	_, err := bts[0].tool.Handler(t.Context(), map[string]any{"text": "hi"})
	var unavailable tools.ErrUnavailable
	if !errors.As(err, &unavailable) || unavailable.Tool != "mcp_fake_echo" {
		t.Fatalf("handler before connect = %v, want tools.ErrUnavailable", err)
	}

	clientTStart(t, c)
	clientTReadyConn(t, c)
	out, err := bts[0].tool.Handler(t.Context(), map[string]any{"text": "hi"})
	if err != nil || !strings.Contains(out, `echo echo {"text":"hi"}`) {
		t.Fatalf("echo = %q, %v", out, err)
	}
	_, err = bts[1].tool.Handler(t.Context(), nil)
	var mf *promptfmt.ModelFacingError
	if !errors.As(err, &mf) || !strings.Contains(mf.Text, "bad input: use a number") || !strings.Contains(mf.Text, leadToolError) {
		t.Fatalf("isError result = %v, want a model-facing tool error quoting the server", err)
	}
	_, err = bts[2].tool.Handler(t.Context(), nil)
	if !errors.As(err, &mf) || mf.Text != textToolErrorNoMessage {
		t.Fatalf("isError result without content = %v, want textToolErrorNoMessage", err)
	}
}

// TestBridge_SanitizeParityWithConfig keeps config's copy of sanitize
// (used to reject server names) in step with the bridge's.
func TestBridge_SanitizeParityWithConfig(t *testing.T) {
	t.Parallel()
	names := []string{"github", "home-assistant", "Home_Assistant", "a--b", "a_b", "x!", "x", "ünïcode", "n-code", "--", "MY SERVER", "my.server"}
	validate := func(servers ...string) error {
		cfg := config.Default()
		for _, n := range servers {
			cfg.MCP.Servers = append(cfg.MCP.Servers, config.MCPServerConfig{Name: n, Transport: "stdio", Command: "/bin/true"})
		}
		return cfg.Validate()
	}
	for _, n := range names {
		err := validate(n)
		if wantEmpty := sanitize(n) == ""; wantEmpty != (err != nil) {
			t.Errorf("name %q: sanitize = %q, config.Validate = %v", n, sanitize(n), err)
		}
	}
	for i, a := range names {
		for _, b := range names[i+1:] {
			if sanitize(a) == "" || sanitize(b) == "" {
				continue
			}
			err := validate(a, b)
			if collide := sanitize(a) == sanitize(b); collide != (err != nil) {
				t.Errorf("names %q, %q: sanitize %q/%q, config.Validate = %v", a, b, sanitize(a), sanitize(b), err)
			}
		}
	}
}
