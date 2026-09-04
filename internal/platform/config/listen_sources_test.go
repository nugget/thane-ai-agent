package config

import (
	"net/netip"
	"strings"
	"testing"
)

func TestValidateListenSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{"absent lists leave both surfaces open", func(c *Config) {}, ""},
		{"empty list is valid", func(c *Config) { c.OllamaAPI.AllowedSources = []string{} }, ""},
		{"CIDR prefixes", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{"192.168.1.0/24", "10.0.0.0/8", "2001:db8::/32"}
		}, ""},
		{"bare addresses", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{"192.168.1.44", "::1"}
		}, ""},
		{"surrounding whitespace is tolerated", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{"  192.168.1.0/24  "}
		}, ""},
		{"unparseable ollama entry names the block and the entry", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{"192.168.1.0/24", "192.168.1"}
		}, `ollama_api.allowed_sources[1] "192.168.1"`},
		{"bad ollama mask names the block", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{"192.168.1.0/33"}
		}, `ollama_api.allowed_sources[0] "192.168.1.0/33"`},
		{"empty ollama entry is refused", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{""}
		}, `ollama_api.allowed_sources[0] ""`},
		{"hostnames are not sources", func(c *Config) {
			c.OllamaAPI.AllowedSources = []string{"homeassistant.example.net"}
		}, "not an address or CIDR prefix"},
		{"unparseable openai entry names its own block", func(c *Config) {
			c.OpenAIAPI.AllowedSources = []string{"not-an-address"}
		}, `openai_api.allowed_sources[0] "not-an-address"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			tc.mutate(cfg)
			err := cfg.validateListenSources()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateListenSources: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestAllowedPrefixes(t *testing.T) {
	t.Parallel()

	t.Run("a bare address becomes a single-host prefix", func(t *testing.T) {
		t.Parallel()
		got, err := OllamaAPIConfig{AllowedSources: []string{"192.168.1.44", "2001:db8::5"}}.AllowedPrefixes()
		if err != nil {
			t.Fatalf("AllowedPrefixes: %v", err)
		}
		want := []netip.Prefix{
			netip.MustParsePrefix("192.168.1.44/32"),
			netip.MustParsePrefix("2001:db8::5/128"),
		}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("prefix %d = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("an IPv4-mapped bare address names the IPv4 host it means", func(t *testing.T) {
		t.Parallel()
		got, err := OpenAIAPIConfig{AllowedSources: []string{"::ffff:192.168.1.44"}}.AllowedPrefixes()
		if err != nil {
			t.Fatalf("AllowedPrefixes: %v", err)
		}
		if want := netip.MustParsePrefix("192.168.1.44/32"); got[0] != want {
			t.Fatalf("prefix = %v, want %v", got[0], want)
		}
	})

	t.Run("host bits in a prefix are masked away", func(t *testing.T) {
		t.Parallel()
		got, err := OllamaAPIConfig{AllowedSources: []string{"192.168.1.5/24"}}.AllowedPrefixes()
		if err != nil {
			t.Fatalf("AllowedPrefixes: %v", err)
		}
		if want := netip.MustParsePrefix("192.168.1.0/24"); got[0] != want {
			t.Fatalf("prefix = %v, want %v", got[0], want)
		}
	})

	t.Run("an absent list yields no prefixes", func(t *testing.T) {
		t.Parallel()
		got, err := OllamaAPIConfig{}.AllowedPrefixes()
		if err != nil || got != nil {
			t.Fatalf("AllowedPrefixes = (%v, %v), want (nil, nil)", got, err)
		}
	})
}
