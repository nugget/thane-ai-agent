package provenance_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// ExampleStore_Sync shows how an operability layer runs one fast-forward-only
// sync pass for a git-backed document root and acts on the result. The store
// wraps the root's repository and verifies incoming commits against an
// out-of-tree trust anchor (see [provenance.NewWithOptions] and
// [provenance.Options]). A pass never blocks document writes: fetch happens
// before the lock and push after it, and a hostile or diverged remote yields a
// [provenance.SyncBlocked] or [provenance.SyncDiverged] result rather than
// mutating local history.
func ExampleStore_Sync() {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := provenance.NewSSHSignerFromKey(key)
	if err != nil {
		log.Fatal(err)
	}
	store, err := provenance.NewWithOptions("/srv/thane/knowledge", signer, nil,
		provenance.Options{AllowedSignersPath: "/etc/thane/knowledge.allowed_signers"})
	if err != nil {
		log.Fatal(err)
	}

	res, err := store.Sync(context.Background(), provenance.SyncRequest{
		RemoteURL:     "git@example.com:org/knowledge.git",
		Branch:        "main",
		SSHCommand:    provenance.BuildSSHCommand("/etc/thane/transport_ed25519", "/etc/thane/known_hosts"),
		Mode:          provenance.SyncModeBidirectional,
		RequireVerify: true, // a signed root always verifies incoming commits
	})
	if err != nil {
		// An operational failure — network, git, or misconfiguration. The
		// pass could not be evaluated; retry on the next tick.
		log.Fatal(err)
	}

	switch res.Outcome {
	case provenance.SyncClean, provenance.SyncFastForwarded, provenance.SyncPushed:
		// Forward progress, or already in sync — nothing to escalate.
	case provenance.SyncDiverged, provenance.SyncBlocked:
		// The engine refused to integrate. Surface Detail to the operator: a
		// diverged history to resolve on the workstation, or a blocked pass
		// (an untrusted commit, a dirty worktree, a detached HEAD).
		fmt.Printf("%s: %s\n", res.Outcome, res.Detail)
	}
}
