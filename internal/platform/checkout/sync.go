package checkout

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// SyncMode selects whether a checkout sync pushes local commits back to the
// remote. The zero value is fetch-only.
type SyncMode = provenance.SyncMode

const (
	// SyncModeFetch pulls fast-forwards from the remote but never pushes.
	SyncModeFetch = provenance.SyncModeFetch
	// SyncModeBidirectional additionally pushes local commits that are a
	// fast-forward of the remote.
	SyncModeBidirectional = provenance.SyncModeBidirectional
)

// SyncOutcome classifies the result of one checkout sync pass.
type SyncOutcome = provenance.SyncOutcome

const (
	// SyncClean means local and remote already agree, or a fetch-only sync saw
	// local commits that do not need pushing.
	SyncClean = provenance.SyncClean
	// SyncFastForwarded means the local branch was advanced to the remote.
	SyncFastForwarded = provenance.SyncFastForwarded
	// SyncPushed means local commits were pushed to the remote.
	SyncPushed = provenance.SyncPushed
	// SyncDiverged means both sides have unique commits.
	SyncDiverged = provenance.SyncDiverged
	// SyncBlocked means the engine refused to integrate.
	SyncBlocked = provenance.SyncBlocked
	// SyncRemoteBehind means the remote rewound behind the caller's last
	// accepted remote head.
	SyncRemoteBehind = provenance.SyncRemoteBehind
)

// SyncRequest parameterizes one fast-forward-only checkout sync pass.
type SyncRequest = provenance.SyncRequest

// SyncResult reports what one checkout sync pass observed and did.
type SyncResult = provenance.SyncResult

// BuildSSHCommand assembles the hardened GIT_SSH_COMMAND used for SSH remotes.
func BuildSSHCommand(sshKey, knownHosts string) string {
	return provenance.BuildSSHCommand(sshKey, knownHosts)
}

// Sync runs one fast-forward-only sync pass for the signed checkout.
func (c *Signed) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	if c == nil || c.Store == nil {
		return SyncResult{}, fmt.Errorf("signed checkout is not configured")
	}
	return c.Store.Sync(ctx, req)
}
