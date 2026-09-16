package identity

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestGenerateAPIToken(t *testing.T) {
	t.Parallel()
	a, err := GenerateAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 43 || a == b {
		t.Fatalf("tokens = %q / %q, want 43 distinct base64url chars", a, b)
	}
	if strings.ContainsAny(a, "+/=") {
		t.Fatalf("token %q is not URL-safe", a)
	}
}

// TestRenderCoreConfigMintsOperatorToken pins that a new workspace's config
// closes the native API from the first boot.
func TestRenderCoreConfigMintsOperatorToken(t *testing.T) {
	t.Parallel()
	signing, err := GenerateSigningKeyPair("test")
	if err != nil {
		t.Fatal(err)
	}
	ca, err := GenerateCertificateAuthority("Test CA", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := renderCoreConfig("test", time.Now(), signing, ca, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Listen struct {
			Auth struct {
				Tokens []struct {
					Label string `yaml:"label"`
					Token string `yaml:"token"`
				} `yaml:"tokens"`
			} `yaml:"auth"`
		} `yaml:"listen"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	toks := parsed.Listen.Auth.Tokens
	if len(toks) != 1 || toks[0].Label != "operator" || len(toks[0].Token) != 43 {
		t.Fatalf("listen.auth.tokens = %+v, want one 43-char operator token", toks)
	}
}
