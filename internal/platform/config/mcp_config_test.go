package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateMCP(t *testing.T) {
	t.Parallel()

	stdio := func(name string) MCPServerConfig {
		return MCPServerConfig{Name: name, Transport: "stdio", Command: "/bin/true"}
	}
	httpSrv := func(name, url string) MCPServerConfig {
		return MCPServerConfig{Name: name, Transport: "http", URL: url}
	}
	with := func(s MCPServerConfig, mut func(*MCPServerConfig)) MCPServerConfig {
		mut(&s)
		return s
	}

	cwd := func(s MCPServerConfig, dir string) MCPServerConfig {
		s.Cwd = dir
		return s
	}

	tests := []struct {
		name      string
		servers   []MCPServerConfig
		paths     map[string]string // Config.Paths, the root prefixes
		needsHome bool              // row expands ~ and needs a home directory
		wantErr   string            // "" = valid
	}{
		{name: "stdio ok", servers: []MCPServerConfig{stdio("files")}},
		{name: "http ok", servers: []MCPServerConfig{httpSrv("game", "http://localhost:11212/mcp")}},
		{name: "https ok", servers: []MCPServerConfig{httpSrv("gh", "https://api.example.com/mcp")}},
		{name: "empty name", servers: []MCPServerConfig{stdio("")}, wantErr: "name must not be empty"},
		{name: "duplicate name", servers: []MCPServerConfig{stdio("a"), stdio("a")}, wantErr: "duplicate"},
		{name: "name sanitizes to empty", servers: []MCPServerConfig{stdio("--!!--")}, wantErr: "at least one letter or digit"},
		{name: "sanitized collision", servers: []MCPServerConfig{stdio("my-server"), stdio("My_Server")}, wantErr: `both become "my_server"`},
		{name: "distinct sanitized names", servers: []MCPServerConfig{stdio("my-server"), stdio("my-server-2")}},
		{name: "unknown transport", servers: []MCPServerConfig{{Name: "x", Transport: "sse", URL: "http://h"}}, wantErr: "transport"},
		{name: "stdio without command", servers: []MCPServerConfig{{Name: "x", Transport: "stdio"}}, wantErr: "requires a command"},
		{name: "http without url", servers: []MCPServerConfig{{Name: "x", Transport: "http"}}, wantErr: "requires a url"},
		{name: "url scheme ws", servers: []MCPServerConfig{httpSrv("x", "ws://localhost/mcp")}, wantErr: "scheme"},
		{name: "url without scheme", servers: []MCPServerConfig{httpSrv("x", "localhost:11212/mcp")}, wantErr: "scheme"},
		{name: "url unparseable", servers: []MCPServerConfig{httpSrv("x", "http://[::1")}, wantErr: "does not parse"},
		{name: "url without host", servers: []MCPServerConfig{httpSrv("x", "http:///mcp")}, wantErr: "host"},
		{name: "protocol auto", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.Protocol = "auto" })}},
		{name: "protocol modern", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.Protocol = "modern" })}},
		{name: "protocol legacy", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.Protocol = "legacy" })}},
		{name: "protocol unknown", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.Protocol = "2025-06-18" })}, wantErr: "protocol"},
		{name: "negative connect_timeout", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.ConnectTimeout = -time.Second })}, wantErr: "connect_timeout"},
		{name: "negative call_timeout", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.CallTimeout = -time.Second })}, wantErr: "call_timeout"},
		{name: "include and exclude", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) {
			s.IncludeTools = []string{"a"}
			s.ExcludeTools = []string{"b"}
		})}, wantErr: "both include_tools and exclude_tools"},
		// Recoverable oddities keep booting; the MCP manager drops them
		// with a warning.
		{name: "reserved header kept valid", servers: []MCPServerConfig{with(httpSrv("x", "http://h/mcp"), func(s *MCPServerConfig) {
			s.Headers = map[string]string{"Accept": "application/json", "Authorization": "Bearer t"}
		})}},
		{name: "env without equals kept valid", servers: []MCPServerConfig{with(stdio("x"), func(s *MCPServerConfig) { s.Env = []string{"NOEQUALS"} })}},
		// A stdio cwd must resolve to an absolute path the way the app
		// resolves it (root prefixes from paths, then ~), or Thane would
		// refuse to start after validation passed.
		{name: "cwd absolute", servers: []MCPServerConfig{cwd(stdio("x"), "/srv/x")}},
		{name: "cwd home", servers: []MCPServerConfig{cwd(stdio("x"), "~/x")}, needsHome: true},
		{name: "cwd root prefix", servers: []MCPServerConfig{cwd(stdio("x"), "kb:tools")}, paths: map[string]string{"kb": "/srv/kb"}},
		{name: "cwd relative", servers: []MCPServerConfig{cwd(stdio("x"), "relative/dir")}, wantErr: "cwd"},
		{name: "cwd unknown root prefix", servers: []MCPServerConfig{cwd(stdio("x"), "kb:tools")}, wantErr: "cwd"},
		{name: "cwd root with relative path", servers: []MCPServerConfig{cwd(stdio("x"), "kb:tools")}, paths: map[string]string{"kb": "relative/kb"}, wantErr: "cwd"},
		{name: "cwd ignored for http", servers: []MCPServerConfig{cwd(httpSrv("x", "http://h/mcp"), "relative/dir")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.needsHome {
				if _, err := os.UserHomeDir(); err != nil {
					t.Skip("no home directory")
				}
			}
			c := &Config{MCP: MCPConfig{Servers: tt.servers}, Paths: tt.paths}
			err := c.validateMCP()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateMCP() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateMCP() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateMCP() = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidateMCP_URLParseErrorHidesCredentials: an unparseable url is
// reported by its reason only, never with the userinfo or query it may
// carry (url.Parse's own error repeats the whole URL).
func TestValidateMCP_URLParseErrorHidesCredentials(t *testing.T) {
	c := &Config{MCP: MCPConfig{Servers: []MCPServerConfig{{
		Name: "x", Transport: "http", URL: "https://user:hunter2@host:bad/mcp?api_key=SECRET123",
	}}}}
	err := c.validateMCP()
	if err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Fatalf("validateMCP() = %v, want a does-not-parse error", err)
	}
	for _, leak := range []string{"hunter2", "SECRET123", "user:", "api_key"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("validateMCP() = %q leaks %q", err, leak)
		}
	}
}

func TestSanitizeMCPName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"home-assistant": "home_assistant",
		"My Server":      "my_server",
		"a--b":           "a_b",
		"_leading_":      "leading",
		"special!@#":     "special",
		"ünïcode":        "n_code",
		"":               "",
		"---":            "",
		"x1_y2":          "x1_y2",
	}
	for in, want := range tests {
		if got := sanitizeMCPName(in); got != want {
			t.Errorf("sanitizeMCPName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoad_MCPDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
mcp:
  servers:
    - name: files
      transport: stdio
      command: /bin/true
    - name: game
      transport: http
      url: http://localhost:11212/mcp
      protocol: legacy
      connect_timeout: 5s
      call_timeout: 10m
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	files, game := cfg.MCP.Servers[0], cfg.MCP.Servers[1]
	if files.Protocol != "auto" || files.ConnectTimeout != 30*time.Second || files.CallTimeout != 2*time.Minute {
		t.Errorf("defaults = protocol %q, connect %s, call %s; want auto, 30s, 2m",
			files.Protocol, files.ConnectTimeout, files.CallTimeout)
	}
	if game.Protocol != "legacy" || game.ConnectTimeout != 5*time.Second || game.CallTimeout != 10*time.Minute {
		t.Errorf("explicit = protocol %q, connect %s, call %s; want legacy, 5s, 10m",
			game.Protocol, game.ConnectTimeout, game.CallTimeout)
	}
}

func TestLoad_MCPInvalidProtocolRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
mcp:
  servers:
    - name: files
      transport: stdio
      command: /bin/true
      protocol: newest
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("Load = %v, want a protocol error", err)
	}
}
