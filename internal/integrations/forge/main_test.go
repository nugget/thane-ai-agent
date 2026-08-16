package forge

import (
	"os"
	"testing"
)

// TestMain detaches git from the developer's own configuration for
// every test in this package.
//
// The follow tests clone real repositories, so ambient
// `commit.gpgsign`, `gpg.ssh.program`, hooks, and templates all reach
// them. A developer whose global config points signing at a managed
// signer — 1Password's is the common one — fails this suite for
// reasons that have nothing to do with the code under test. Each
// fixture configures what it needs locally.
//
// Mirrors internal/platform/checkout, which learned this the same way.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	os.Exit(m.Run())
}
