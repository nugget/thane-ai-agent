package checkout

import (
	"os"
	"testing"
)

// TestMain detaches git from the developer's own configuration for every test
// in this package.
//
// These tests create signed repositories and verify signatures, and both sides
// read `gpg.ssh.program`. A developer whose global config points that at a
// managed signer — 1Password's is the common one — hands it test keys it has
// never seen, and the suite fails for reasons that have nothing to do with the
// code. Ambient `commit.gpgsign`, hooks, and templates are the same class of
// problem. Each test repository configures what it needs locally, so nothing
// ambient should reach it.
//
// See internal/platform/provenance for the fuller account.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	os.Exit(m.Run())
}
