package provenance

import (
	"strings"
	"testing"
)

const (
	testAgentKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIM72/tw9yIXLKQ+TL3E9g3BvJYyYyOaC6l2bSIEfkeHQ"
	testAliceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGyUStZXWURqF4b7IWfSTz2W6zYz5JnXrKbcuPfGAmUo"
	testBobKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO+3xdUdsJA9XoATiuDErHwn2cDSIO1U1/t+BuN6P3Gv"
)

func TestRenderAllowedSigners_AgentAnchorFirst(t *testing.T) {
	t.Parallel()
	got, err := RenderAllowedSigners(testAgentKey, nil)
	if err != nil {
		t.Fatalf("RenderAllowedSigners() error = %v", err)
	}
	want := AgentPrincipal + " " + testAgentKey + "\n"
	if got != want {
		t.Fatalf("RenderAllowedSigners() = %q, want %q", got, want)
	}
}

func TestRenderAllowedSigners_UnionSortedDeterministic(t *testing.T) {
	t.Parallel()
	// Bob before Alice in the input; output must sort by principal, with the
	// agent pinned first regardless.
	got, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{
		{Principal: "bob@example.com", PublicKey: testBobKey, Comment: "Bob laptop"},
		{Principal: "alice@example.com", PublicKey: testAliceKey},
	})
	if err != nil {
		t.Fatalf("RenderAllowedSigners() error = %v", err)
	}
	want := strings.Join([]string{
		AgentPrincipal + " " + testAgentKey,
		"alice@example.com " + testAliceKey,
		"bob@example.com " + testBobKey + " Bob laptop",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("RenderAllowedSigners() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderAllowedSigners_Stable(t *testing.T) {
	t.Parallel()
	ops := []TrustedSigner{
		{Principal: "alice@example.com", PublicKey: testAliceKey},
		{Principal: "bob@example.com", PublicKey: testBobKey},
	}
	first, err := RenderAllowedSigners(testAgentKey, ops)
	if err != nil {
		t.Fatalf("RenderAllowedSigners() error = %v", err)
	}
	// Reversed input must render identically (deterministic sort).
	second, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{ops[1], ops[0]})
	if err != nil {
		t.Fatalf("RenderAllowedSigners() error = %v", err)
	}
	if first != second {
		t.Fatalf("render not stable across input order:\n%q\nvs\n%q", first, second)
	}
}

func TestRenderAllowedSigners_ValidityWindow(t *testing.T) {
	t.Parallel()
	got, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{{
		Principal:   "alice@example.com",
		PublicKey:   testAliceKey,
		ValidAfter:  "2026-01-01T00:00:00Z",
		ValidBefore: "2027-06-15T12:30:45Z",
	}})
	if err != nil {
		t.Fatalf("RenderAllowedSigners() error = %v", err)
	}
	wantLine := `alice@example.com valid-after="20260101000000Z",valid-before="20270615123045Z" ` + testAliceKey
	if !strings.Contains(got, wantLine) {
		t.Fatalf("RenderAllowedSigners() =\n%s\nwant line %q", got, wantLine)
	}
}

func TestRenderAllowedSigners_Rejections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		agent string
		ops   []TrustedSigner
		want  string
	}{
		{
			name:  "operator reuses agent key",
			agent: testAgentKey,
			ops:   []TrustedSigner{{Principal: "evil@example.com", PublicKey: testAgentKey}},
			want:  "agent's own signing key",
		},
		{
			name:  "duplicate operator key under two principals",
			agent: testAgentKey,
			ops: []TrustedSigner{
				{Principal: "alice@example.com", PublicKey: testAliceKey},
				{Principal: "eve@example.com", PublicKey: testAliceKey},
			},
			want: "duplicates the key already trusted",
		},
		{
			name:  "malformed operator key",
			agent: testAgentKey,
			ops:   []TrustedSigner{{Principal: "alice@example.com", PublicKey: "not-a-key"}},
			want:  "not a valid SSH public key",
		},
		{
			name:  "malformed agent key",
			agent: "not-a-key",
			ops:   nil,
			want:  "agent signing key",
		},
		{
			name:  "bad validity timestamp",
			agent: testAgentKey,
			ops:   []TrustedSigner{{Principal: "alice@example.com", PublicKey: testAliceKey, ValidAfter: "nope"}},
			want:  "valid_after",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := RenderAllowedSigners(tc.agent, tc.ops)
			if err == nil {
				t.Fatalf("RenderAllowedSigners() = nil error, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RenderAllowedSigners() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestRenderAllowedSigners_CanonicalizesComments confirms that keys differing
// only by trailing comment are treated as identical (so a comment can't slip a
// duplicate past dedup, and the agent-key check can't be evaded by appending a
// comment).
func TestRenderAllowedSigners_CanonicalizesComments(t *testing.T) {
	t.Parallel()
	_, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{{Principal: "evil@example.com", PublicKey: testAgentKey + " looks-different"}})
	if err == nil || !strings.Contains(err.Error(), "agent's own signing key") {
		t.Fatalf("RenderAllowedSigners() error = %v, want agent-key rejection despite comment", err)
	}
}
