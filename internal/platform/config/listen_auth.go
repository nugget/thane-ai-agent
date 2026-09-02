package config

import (
	"fmt"
	"strings"
)

// validateListenAuth checks the native API's credential list: every token
// non-empty, no token configured twice, no token shared with a companion
// account (the two are different principals and must not collide), and
// a non-negative session lifetime.
func (c *Config) validateListenAuth() error {
	a := c.Listen.Auth
	if a.SessionTTL < 0 {
		return fmt.Errorf("listen.auth.session_ttl must not be negative")
	}
	seen := make(map[string]int, len(a.Tokens))
	companion := c.Companion.TokenIndex()
	for i, t := range a.Tokens {
		if strings.TrimSpace(t.Token) == "" {
			return fmt.Errorf("listen.auth.tokens[%d] has an empty token", i)
		}
		if t.Token != strings.TrimSpace(t.Token) {
			return fmt.Errorf("listen.auth.tokens[%d] has surrounding whitespace", i)
		}
		if j, dup := seen[t.Token]; dup {
			return fmt.Errorf("listen.auth.tokens[%d] repeats the token at index %d", i, j)
		}
		seen[t.Token] = i
		if account, shared := companion[t.Token]; shared {
			return fmt.Errorf("listen.auth.tokens[%d] is also a companion token for account %q; a credential must name one principal", i, account)
		}
	}
	return nil
}
