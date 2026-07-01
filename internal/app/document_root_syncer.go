package app

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// defaultSyncBranch and defaultSyncInterval are applied where a root's
// git.remote config leaves them unset (config.Load deliberately does not fill
// them, so the defaults live at the one point of consumption — here).
const (
	defaultSyncBranch   = "main"
	defaultSyncInterval = 60 * time.Second
)

// parseSyncInterval maps a git.remote.interval string onto a poll cadence.
// Empty uses [defaultSyncInterval]; "0" (a zero duration) disables the timer,
// leaving the root sync-on-demand only. validateGitRemote has already checked
// that a non-empty value parses, so an error here is defensive.
func parseSyncInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw) // match validateGitRemote, which trims first
	if raw == "" {
		return defaultSyncInterval, nil
	}
	return time.ParseDuration(raw)
}

// buildSyncRequest maps a root's git config onto a [provenance.SyncRequest].
// resolve expands a configured path (~, environment variables) — the caller
// supplies the paths.Resolver-backed closure; tests pass an identity function.
// The out-of-tree trust anchor, if any, is a store-construction concern and is
// not carried here.
func buildSyncRequest(gitCfg config.DocumentRootGitConfig, resolve func(string) string) provenance.SyncRequest {
	// Trim every field before use: validateGitRemote validates these trimmed,
	// so a quoted trailing space in YAML ("required ", "bidirectional ", a
	// padded url/branch/key path) is accepted by config Load. An untrimmed
	// consume here would fail open — dropping verification, downgrading to
	// fetch-only, or producing a broken remote/branch/GIT_SSH_COMMAND.
	verify := strings.TrimSpace(gitCfg.VerifySignatures)
	remote := gitCfg.Remote
	req := provenance.SyncRequest{
		Branch:        defaultSyncBranch,
		Mode:          provenance.SyncModeFetch,
		RequireVerify: verify == "warn" || verify == "required",
	}
	if remote == nil {
		return req
	}
	req.RemoteURL = strings.TrimSpace(remote.URL)
	if b := strings.TrimSpace(remote.Branch); b != "" {
		req.Branch = b
	}
	if strings.TrimSpace(remote.Mode) == "bidirectional" {
		req.Mode = provenance.SyncModeBidirectional
	}
	// GIT_SSH_COMMAND is only meaningful for an SSH remote; presence of ssh
	// transport credentials is the signal (known_hosts is required for an SSH
	// url, so at least one is set). It is harmless for https, which git runs
	// without consulting GIT_SSH_COMMAND.
	sshKey := strings.TrimSpace(remote.Auth.SSHKey)
	knownHosts := strings.TrimSpace(remote.Auth.KnownHosts)
	if sshKey != "" || knownHosts != "" {
		req.SSHCommand = provenance.BuildSSHCommand(resolve(sshKey), resolve(knownHosts))
	}
	return req
}

// syncState is the in-memory, per-root observed state of remote sync. It is
// re-derived every pass (never persisted) and read by the operability surface.
type syncState struct {
	Root       string
	OK         bool // false when the last pass errored operationally
	Outcome    provenance.SyncOutcome
	Ahead      int
	Behind     int
	LocalHead  string
	RemoteHead string
	Detail     string // reason for a blocked/diverged/remote_behind outcome, or the error message
	LastSyncAt time.Time
}

// syncStateRegistry holds per-root sync state and the remote head observed on
// each root's previous pass (for the engine's rewind detection). It is safe
// for concurrent access: the syncer loop writes, the operability surface reads.
type syncStateRegistry struct {
	mu         sync.Mutex
	state      map[string]syncState
	lastRemote map[string]string
}

func newSyncStateRegistry() *syncStateRegistry {
	return &syncStateRegistry{
		state:      make(map[string]syncState),
		lastRemote: make(map[string]string),
	}
}

// setState records the latest observed state for a root.
func (r *syncStateRegistry) setState(st syncState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[st.Root] = st
}

// advanceRemote records the remote head a root legitimately accepted, so the
// next pass can detect a rewind past it. The caller advances it only for
// accepted outcomes (clean / fast-forwarded / pushed); a refused outcome
// (diverged, blocked, remote_behind) leaves it untouched, so the rewind keeps
// being detected until the operator resolves it.
func (r *syncStateRegistry) advanceRemote(root, sha string) {
	if sha == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRemote[root] = sha
}

// lastKnownRemote returns the remote head recorded for a root's previous pass,
// or "" if none.
func (r *syncStateRegistry) lastKnownRemote(root string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRemote[root]
}

// get returns the recorded state for a root.
func (r *syncStateRegistry) get(root string) (syncState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.state[root]
	return st, ok
}

// all returns every recorded state, sorted by root for a stable listing.
func (r *syncStateRegistry) all() []syncState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]syncState, 0, len(r.state))
	for _, st := range r.state {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

// syncEngine is the sync surface a docRootSyncer drives — satisfied by
// *provenance.Store. The interface keeps runOnce unit-testable with a fake,
// without a live git repository.
type syncEngine interface {
	Sync(ctx context.Context, req provenance.SyncRequest) (provenance.SyncResult, error)
}

// docRootSyncer runs timed fast-forward-only sync for one git-remote-backed
// document root. It threads the last-seen remote head into each pass (for
// rewind detection), records the outcome in the registry, and re-indexes the
// root after a fast-forward moves the worktree.
type docRootSyncer struct {
	root     string
	engine   syncEngine
	request  provenance.SyncRequest // LastKnownRemote is filled per pass
	interval time.Duration          // 0 disables the ticker (sync-on-demand only)
	refresh  func(context.Context) error
	registry *syncStateRegistry
	logger   *slog.Logger
	now      func() time.Time // injectable clock; nil uses time.Now
}

func (s *docRootSyncer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// runOnce performs one sync pass, records the result, and re-indexes on a
// fast-forward. It never returns an error: an operational failure is recorded
// as state (OK=false) and retried on the next pass.
func (s *docRootSyncer) runOnce(ctx context.Context) syncState {
	req := s.request
	req.LastKnownRemote = s.registry.lastKnownRemote(s.root)

	res, err := s.engine.Sync(ctx, req)
	st := syncState{Root: s.root, LastSyncAt: s.clock()}
	if err != nil {
		st.OK = false
		st.Detail = err.Error()
		s.logger.Warn("document root sync failed", "root", s.root, "error", err)
		s.registry.setState(st)
		return st
	}

	st.OK = true
	st.Outcome = res.Outcome
	st.Ahead, st.Behind = res.Ahead, res.Behind
	st.LocalHead, st.RemoteHead = res.LocalHead, res.RemoteHead
	st.Detail = res.Detail

	switch res.Outcome {
	case provenance.SyncBlocked, provenance.SyncDiverged, provenance.SyncRemoteBehind:
		s.logger.Warn("document root sync needs attention",
			"root", s.root, "outcome", res.Outcome, "detail", res.Detail)
	default:
		s.logger.Info("document root sync",
			"root", s.root, "outcome", res.Outcome, "ahead", res.Ahead, "behind", res.Behind)
	}

	// A fast-forward moved the worktree; re-index so reads see the new content
	// without waiting for the periodic refresher.
	if res.Outcome == provenance.SyncFastForwarded && s.refresh != nil {
		if rerr := s.refresh(ctx); rerr != nil && ctx.Err() == nil {
			s.logger.Warn("re-index after sync fast-forward failed", "root", s.root, "error", rerr)
		}
	}

	s.registry.setState(st)
	// Advance the rewind baseline only for outcomes where the remote head was
	// legitimately accepted as authoritative (in sync, or integrated). A
	// refused outcome — diverged, blocked, or remote_behind — must NOT advance
	// the baseline to a head thane never accepted, or a later real rewind of
	// the true line would escape detection and be pushed over.
	switch res.Outcome {
	case provenance.SyncClean, provenance.SyncFastForwarded, provenance.SyncPushed:
		s.registry.advanceRemote(s.root, res.RemoteHead)
	}
	return st
}

// Run drives runOnce immediately and then on the configured interval until the
// context is cancelled. A non-positive interval runs a single pass and then
// blocks on ctx (sync-on-demand only — a trigger or the operability POST
// endpoint drives further passes).
func (s *docRootSyncer) Run(ctx context.Context) {
	s.runOnce(ctx)
	if s.interval <= 0 {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}
