package provenance

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
			name:  "operator reuses agent key under another principal",
			agent: testAgentKey,
			ops:   []TrustedSigner{{Principal: "evil@example.com", PublicKey: testAgentKey}},
			want:  "duplicates the key already trusted",
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
		{
			name:  "operator key smuggles a second key via embedded newline",
			agent: testAgentKey,
			ops:   []TrustedSigner{{Principal: "alice@example.com", PublicKey: testAliceKey + "\n" + testBobKey}},
			want:  "exactly one SSH public key",
		},
		{
			name:  "inverted validity window",
			agent: testAgentKey,
			ops: []TrustedSigner{{
				Principal:   "alice@example.com",
				PublicKey:   testAliceKey,
				ValidAfter:  "2027-01-01T00:00:00Z",
				ValidBefore: "2026-01-01T00:00:00Z",
			}},
			want: "strictly before",
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
// duplicate past dedup, and the agent-key spoof check can't be evaded by
// appending a comment).
func TestRenderAllowedSigners_CanonicalizesComments(t *testing.T) {
	t.Parallel()
	_, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{{Principal: "evil@example.com", PublicKey: testAgentKey + " looks-different"}})
	if err == nil || !strings.Contains(err.Error(), "duplicates the key already trusted") {
		t.Fatalf("RenderAllowedSigners() error = %v, want agent-key spoof rejection despite comment", err)
	}
}

// TestRenderAllowedSigners_AcceptsAgentPrincipal confirms a root may declare
// the agent key entitled to establish it. The rendered file is unchanged —
// the agent line is already emitted implicitly — but the declaration has to be
// accepted, because refusing it is what would make an agent-founded root
// impossible to admit.
func TestRenderAllowedSigners_AcceptsAgentPrincipal(t *testing.T) {
	t.Parallel()
	withAgent, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{{Principal: AgentPrincipal, PublicKey: testAgentKey}})
	if err != nil {
		t.Fatalf("RenderAllowedSigners() with agent principal = %v, want nil", err)
	}
	withoutAgent, err := RenderAllowedSigners(testAgentKey, nil)
	if err != nil {
		t.Fatalf("RenderAllowedSigners() bare = %v, want nil", err)
	}
	if withAgent != withoutAgent {
		t.Fatalf("declaring the agent key changed the rendered file:\n got %q\nwant %q", withAgent, withoutAgent)
	}
}

// TestRenderSeedSigners_OmitsImplicitAgentKey is the property admission rests
// on: the seed file must contain only what config declared, so an
// agent-founded root cannot admit itself.
func TestRenderSeedSigners_OmitsImplicitAgentKey(t *testing.T) {
	t.Parallel()
	got, err := RenderSeedSigners([]TrustedSigner{{Principal: "alice@example.com", PublicKey: testAliceKey}})
	if err != nil {
		t.Fatalf("RenderSeedSigners() = %v, want nil", err)
	}
	if strings.Contains(got, AgentPrincipal) {
		t.Fatalf("RenderSeedSigners() = %q, want no implicit %s line", got, AgentPrincipal)
	}
	if !strings.Contains(got, "alice@example.com") {
		t.Fatalf("RenderSeedSigners() = %q, want the declared seed", got)
	}
}

// TestRenderSeedSigners_KeepsDeclaredAgentKey confirms the agent is entitled
// where — and only where — config says so.
func TestRenderSeedSigners_KeepsDeclaredAgentKey(t *testing.T) {
	t.Parallel()
	got, err := RenderSeedSigners([]TrustedSigner{{Principal: AgentPrincipal, PublicKey: testAgentKey}})
	if err != nil {
		t.Fatalf("RenderSeedSigners() = %v, want nil", err)
	}
	if !strings.Contains(got, AgentPrincipal) {
		t.Fatalf("RenderSeedSigners() = %q, want the declared agent seed", got)
	}
}

// TestRenderAllowedSigners_CollapsesSamePrincipalDuplicate confirms the same
// key under the same principal (e.g. listed in both the shared block and a
// root's own list, which union) collapses to one line rather than erroring.
func TestRenderAllowedSigners_CollapsesSamePrincipalDuplicate(t *testing.T) {
	t.Parallel()
	got, err := RenderAllowedSigners(testAgentKey, []TrustedSigner{
		{Principal: "alice@example.com", PublicKey: testAliceKey},
		{Principal: "alice@example.com", PublicKey: testAliceKey, Comment: "listed twice"},
	})
	if err != nil {
		t.Fatalf("RenderAllowedSigners() error = %v", err)
	}
	if n := strings.Count(got, testAliceKey); n != 1 {
		t.Fatalf("alice key appears %d times, want 1:\n%s", n, got)
	}
}

// TestSeedAllowedSignersWritesOnceAndThenLeavesTheRootAlone covers the full
// I/O path: rendering the agent+seed union into the repo's .allowed_signers,
// committing it as signed history, keeping HEAD verifiable, and then never
// touching the file again — including when config later disagrees with it,
// which is the root exercising its own delegation rather than drift.
func TestSeedAllowedSignersWritesOnceAndThenLeavesTheRootAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	signer := testSigner(t)
	// The agent signs the birth commit, so it has to be among the keys
	// entitled to establish the root or the root is born inadmissible.
	ops := []TrustedSigner{
		{Principal: AgentPrincipal, PublicKey: signer.PublicKey()},
		{Principal: "alice@example.com", PublicKey: testAliceKey, Comment: "Alice laptop"},
	}
	s, err := NewWithOptions(dir, signer, slog.Default(), Options{SeedSigners: ops})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if err := s.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	// The seed landed when the repository was established; asking again
	// must not rewrite it.
	changed, err := s.SeedAllowedSigners(t.Context(), ops)
	if err != nil {
		t.Fatalf("SeedAllowedSigners: %v", err)
	}
	if changed {
		t.Fatal("seeding an established root changed = true, want false")
	}

	got, err := os.ReadFile(filepath.Join(dir, ".allowed_signers"))
	if err != nil {
		t.Fatalf("read .allowed_signers: %v", err)
	}
	if !strings.HasPrefix(string(got), AgentPrincipal+" ") {
		t.Fatalf("agent anchor is not the first line:\n%s", got)
	}
	if !strings.Contains(string(got), "alice@example.com "+testAliceKey+" Alice laptop") {
		t.Fatalf("operator line missing or malformed:\n%s", got)
	}

	// HEAD (the seed commit) must still verify against the rendered
	// trust file — the agent key that signed it is in the file.
	if err := s.git(t.Context(), nil, nil, "verify-commit", "HEAD"); err != nil {
		t.Fatalf("verify-commit HEAD after seed: %v", err)
	}

	// Idempotent: an unchanged set makes no commit and does not move HEAD.
	before := headHash(t, s)
	changed, err = s.SeedAllowedSigners(t.Context(), ops)
	if err != nil {
		t.Fatalf("second SeedAllowedSigners: %v", err)
	}
	if changed {
		t.Fatal("second seed changed = true, want false — an existing trust set is the root's own")
	}
	if after := headHash(t, s); after != before {
		t.Fatalf("HEAD moved on re-seed: %s -> %s", before, after)
	}
}

// TestSeedAllowedSignersRejectsExternalTrustFile confirms in-tree seeding
// refuses to run when the Store verifies against an external
// allowed_signers file it could not commit anyway.
func TestSeedAllowedSignersRejectsExternalTrustFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	signer := testSigner(t)
	allowedPath := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowedPath, []byte(AgentPrincipal+" "+signer.PublicKey()+"\n"), 0o644); err != nil {
		t.Fatalf("write external allowed_signers: %v", err)
	}
	s, err := NewWithOptions(filepath.Join(dir, "repo"), signer, slog.Default(), Options{AllowedSignersPath: allowedPath})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	_, err = s.SeedAllowedSigners(t.Context(), []TrustedSigner{{Principal: "alice@example.com", PublicKey: testAliceKey}})
	if err == nil || !strings.Contains(err.Error(), "external allowed_signers") {
		t.Fatalf("ReconcileAllowedSigners error = %v, want external-file rejection", err)
	}
}

// TestVerifyHead covers the boot round-trip: HEAD verifies against a trust
// file that includes its signer, and fails against one that does not.
func TestVerifyHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	signer := testSigner(t)
	s, err := New(dir, signer, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	// HEAD is signed by the agent key, which the bootstrapped trust file
	// trusts — the round-trip passes.
	if err := s.VerifyHead(t.Context()); err != nil {
		t.Fatalf("VerifyHead on a well-formed repo: %v", err)
	}

	// Point the trust file at an unrelated key: HEAD's signer is no longer
	// trusted, so the round-trip must fail.
	other := testSigner(t)
	if err := os.WriteFile(filepath.Join(dir, ".allowed_signers"),
		[]byte(AgentPrincipal+" "+other.PublicKey()+"\n"), 0o644); err != nil {
		t.Fatalf("overwrite .allowed_signers: %v", err)
	}
	if err := s.VerifyHead(t.Context()); err == nil {
		t.Fatal("VerifyHead with an untrusted signer = nil, want error")
	}
}

func headHash(t *testing.T, s *Store) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.git(t.Context(), nil, &buf, "rev-parse", "HEAD"); err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(buf.String())
}

// TestSeedSignersLandAtRepositoryCreation is the property the whole model
// rests on: a root's trust surface is established once, from its own
// declared seed, and is not the shared list that every other root drew
// from.
func TestSeedSignersLandAtRepositoryCreation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	seed := []TrustedSigner{{Principal: "alice@example.com", PublicKey: testAliceKey, Comment: "Alice laptop"}}
	if _, err := NewWithOptions(dir, testSigner(t), slog.Default(), Options{SeedSigners: seed}); err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, ".allowed_signers"))
	if err != nil {
		t.Fatalf("read .allowed_signers: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "alice@example.com") {
		t.Fatalf(".allowed_signers should carry the seed signer at creation:\n%s", got)
	}
	if !strings.Contains(got, AgentPrincipal) {
		t.Fatalf(".allowed_signers should still carry the agent key:\n%s", got)
	}
}

// TestSeedIsNotReappliedAfterTheRootDiverges covers the case that
// separates seeding from reconciling: once a root's own file says
// something config does not, config does not win. Divergence is the root
// exercising its delegation, not drift.
func TestSeedIsNotReappliedAfterTheRootDiverges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	signer := testSigner(t)
	s, err := NewWithOptions(dir, signer, slog.Default(), Options{})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if err := s.BootstrapBirthCommit(t.Context()); err != nil {
		t.Fatalf("BootstrapBirthCommit: %v", err)
	}

	// A key config never mentioned, added the way a root legitimately
	// extends its own trust.
	path := filepath.Join(dir, ".allowed_signers")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	delegated := string(body) + "carol@example.com " + testAliceKey + "\n"
	if err := os.WriteFile(path, []byte(delegated), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := s.SeedAllowedSigners(t.Context(), []TrustedSigner{
		{Principal: "alice@example.com", PublicKey: testAliceKey},
	}); err != nil {
		t.Fatalf("SeedAllowedSigners: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !strings.Contains(string(after), "carol@example.com") {
		t.Fatalf("the root's own delegation was overwritten from config:\n%s", string(after))
	}
}
