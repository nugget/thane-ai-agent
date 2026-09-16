package listen

import "testing"

func TestTokenSet(t *testing.T) {
	t.Parallel()

	matcher := NewTokenSet(map[string]string{
		"alice-token": "alice",
		"bob-token":   "bob",
	})

	tests := []struct {
		name        string
		token       string
		wantAccount string
		wantOK      bool
	}{
		{"first account resolves", "alice-token", "alice", true},
		{"second account resolves", "bob-token", "bob", true},
		{"unknown token rejected", "carol-token", "", false},
		{"prefix of a token rejected", "alice-tok", "", false},
		{"token with trailing byte rejected", "alice-token ", "", false},
		{"empty token rejected", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			account, ok := matcher.Match(tc.token)
			if ok != tc.wantOK || account != tc.wantAccount {
				t.Fatalf("match(%q) = (%q, %v), want (%q, %v)", tc.token, account, ok, tc.wantAccount, tc.wantOK)
			}
		})
	}
}

func TestTokenSet_EmptyIndexAndEmptyConfiguredToken(t *testing.T) {
	t.Parallel()

	if _, ok := NewTokenSet(nil).Match("anything"); ok {
		t.Fatal("empty index matched a token")
	}
	// A blank configured token must not turn a blank presented token into
	// an authenticated account.
	if _, ok := NewTokenSet(map[string]string{"": "ghost"}).Match(""); ok {
		t.Fatal("empty configured token authenticated an empty presented token")
	}
}
