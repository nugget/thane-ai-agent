package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
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

// buildSyncRequest maps a root's git config onto a [checkout.SyncRequest].
// resolve expands a configured path (~, environment variables) — the caller
// supplies the paths.Resolver-backed closure; tests pass an identity function.
// The out-of-tree trust anchor, if any, is a store-construction concern and is
// not carried here.
func buildSyncRequest(gitCfg config.DocumentRootGitConfig, resolve func(string) string) checkout.SyncRequest {
	// Trim every field before use: validateGitRemote validates these trimmed,
	// so a quoted trailing space in YAML ("required ", "bidirectional ", a
	// padded url/branch/key path) is accepted by config Load. An untrimmed
	// consume here would fail open — dropping verification, downgrading to
	// fetch-only, or producing a broken remote/branch/GIT_SSH_COMMAND.
	verify := strings.TrimSpace(gitCfg.VerifySignatures)
	remote := gitCfg.Remote
	req := checkout.SyncRequest{
		Branch:        defaultSyncBranch,
		Mode:          checkout.SyncModeFetch,
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
		req.Mode = checkout.SyncModeBidirectional
	}
	// GIT_SSH_COMMAND is only meaningful for an SSH remote; presence of ssh
	// transport credentials is the signal (known_hosts is required for an SSH
	// url, so at least one is set). It is harmless for https, which git runs
	// without consulting GIT_SSH_COMMAND.
	sshKey := strings.TrimSpace(remote.Auth.SSHKey)
	knownHosts := strings.TrimSpace(remote.Auth.KnownHosts)
	if sshKey != "" || knownHosts != "" {
		req.SSHCommand = checkout.BuildSSHCommand(resolve(sshKey), resolve(knownHosts))
	}
	return req
}

// buildDocRootSyncer constructs a per-root syncer from a root's git config,
// driving the given checkout sync engine. It returns (nil, nil) when the root
// has no remote block. resolve expands configured paths.
//
// An out-of-tree trust_anchor is not yet wired: the default is the in-tree
// .allowed_signers (which the sync engine verifies safely, since a fetch never
// rewrites the worktree before verification), so a configured trust_anchor is
// refused rather than silently ignored.
func buildDocRootSyncer(root string, gitCfg config.DocumentRootGitConfig, engine syncEngine, registry *checkout.SyncStateRegistry, resolve func(string) string, logger *slog.Logger) (*docRootSyncer, error) {
	remote := gitCfg.Remote
	if remote == nil {
		return nil, nil
	}
	if strings.TrimSpace(remote.TrustAnchor) != "" {
		return nil, fmt.Errorf("git.remote.trust_anchor (out-of-tree verification) is not yet wired; omit it to use the in-tree .allowed_signers")
	}
	interval, err := parseSyncInterval(remote.Interval)
	if err != nil {
		return nil, fmt.Errorf("git.remote.interval: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &docRootSyncer{
		root:     root,
		engine:   engine,
		request:  buildSyncRequest(gitCfg, resolve),
		interval: interval,
		registry: registry,
		logger:   logger.With("component", "docroot_syncer", "root", root),
	}, nil
}

type syncState = checkout.SyncState

// syncEngine is the sync surface a docRootSyncer drives — satisfied by
// *checkout.Signed. The interface keeps runOnce unit-testable with a fake,
// without a live git repository.
type syncEngine interface {
	Sync(ctx context.Context, req checkout.SyncRequest) (checkout.SyncResult, error)
}

type syncTransitionKind string

const (
	syncTransitionAttentionRequired syncTransitionKind = "attention_required"
	syncTransitionRecovered         syncTransitionKind = "recovered"
)

type syncStateTransition struct {
	Kind        syncTransitionKind
	Previous    syncState
	Current     syncState
	HasPrevious bool
}

type syncTransitionNotifier func(context.Context, syncStateTransition) error

// docRootSyncer runs timed fast-forward-only sync for one git-remote-backed
// document root. It threads the last-seen remote head into each pass (for
// rewind detection), records the outcome in the registry, and re-indexes the
// root after a fast-forward moves the worktree.
type docRootSyncer struct {
	root             string
	engine           syncEngine
	request          checkout.SyncRequest // LastKnownRemote is filled per pass
	interval         time.Duration        // 0 disables the ticker (sync-on-demand only)
	refresh          func(context.Context) error
	notifyTransition syncTransitionNotifier
	registry         *checkout.SyncStateRegistry
	logger           *slog.Logger
	now              func() time.Time // injectable clock; nil uses time.Now

	// mu guards the log gates: the ticker in Run and an on-demand pass
	// (the operability POST endpoint) can drive runOnce concurrently.
	mu            sync.Mutex
	failGate      syncLogGate
	attentionGate syncLogGate
}

// syncLogCoalesceAfter bounds how long a persistent, unchanged failure
// or attention state stays quiet between WARN reminders.
const syncLogCoalesceAfter = time.Hour

// syncLogGate coalesces a condition that repeats every pass into log
// lines that carry information: admit passes on the first occurrence,
// on a change of detail, and once per syncLogCoalesceAfter as a
// reminder; everything in between belongs at Debug. A multi-day remote
// outage once produced 18,285 identical WARN rows — 64% of a
// production week — one full-severity line per pass at a time.
type syncLogGate struct {
	detail string
	warnAt time.Time
	count  int
}

// admit records one more pass of the condition and reports whether this
// pass should log at full severity, along with the streak length.
func (g *syncLogGate) admit(now time.Time, detail string) (int, bool) {
	g.count++
	changed := detail != g.detail
	g.detail = detail
	if changed || now.Sub(g.warnAt) >= syncLogCoalesceAfter {
		g.warnAt = now
		return g.count, true
	}
	return g.count, false
}

// reset clears the gate and returns how many passes the condition
// lasted, so the clearing pass can say what it recovered from.
func (g *syncLogGate) reset() int {
	n := g.count
	*g = syncLogGate{}
	return n
}

func (s *docRootSyncer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *docRootSyncer) recordState(ctx context.Context, st syncState) {
	prev, hadPrev := s.registry.RecordState(st)
	if s.notifyTransition == nil {
		return
	}
	transition, ok := classifySyncStateTransition(prev, hadPrev, st)
	if !ok {
		return
	}
	if err := s.notifyTransition(ctx, transition); err != nil && ctx.Err() == nil {
		s.logger.Warn("document root sync attention wake failed",
			"root", st.Name,
			"outcome", st.Outcome,
			"transition", transition.Kind,
			"detail", st.Detail,
			"error", err)
	}
}

func classifySyncStateTransition(prev syncState, hadPrev bool, current syncState) (syncStateTransition, bool) {
	if !current.OK {
		return syncStateTransition{}, false
	}
	if syncOutcomeNeedsAttention(current.Outcome) {
		if !hadPrev || !prev.OK || prev.Outcome != current.Outcome || strings.TrimSpace(prev.Detail) != strings.TrimSpace(current.Detail) {
			return syncStateTransition{
				Kind:        syncTransitionAttentionRequired,
				Previous:    prev,
				Current:     current,
				HasPrevious: hadPrev,
			}, true
		}
		return syncStateTransition{}, false
	}
	if hadPrev && prev.OK && syncOutcomeNeedsAttention(prev.Outcome) {
		return syncStateTransition{
			Kind:        syncTransitionRecovered,
			Previous:    prev,
			Current:     current,
			HasPrevious: true,
		}, true
	}
	return syncStateTransition{}, false
}

func syncOutcomeNeedsAttention(outcome checkout.SyncOutcome) bool {
	switch outcome {
	case checkout.SyncBlocked, checkout.SyncDiverged, checkout.SyncRemoteBehind:
		return true
	default:
		return false
	}
}

// runOnce performs one sync pass, records the result, and re-indexes on a
// fast-forward. It never returns an error: an operational failure is recorded
// as state (OK=false) and retried on the next pass.
func (s *docRootSyncer) runOnce(ctx context.Context) syncState {
	req := s.request
	req.LastKnownRemote = s.registry.LastKnownRemote(s.root)

	res, err := s.engine.Sync(ctx, req)
	st := syncState{Name: s.root, LastSyncAt: s.clock()}
	if err != nil {
		st.OK = false
		st.Detail = err.Error()
		s.logFailure(err, st.Detail)
		s.recordState(ctx, st)
		return st
	}

	st.OK = true
	st.Outcome = res.Outcome
	st.Ahead, st.Behind = res.Ahead, res.Behind
	st.LocalHead, st.RemoteHead = res.LocalHead, res.RemoteHead
	st.Detail = res.Detail

	s.logOutcome(res)

	// A fast-forward moved the worktree; re-index so reads see the new content
	// without waiting for the periodic refresher.
	if res.Outcome == checkout.SyncFastForwarded && s.refresh != nil {
		if rerr := s.refresh(ctx); rerr != nil && ctx.Err() == nil {
			s.logger.Warn("re-index after sync fast-forward failed", "root", s.root, "error", rerr)
		}
	}

	s.recordState(ctx, st)
	// Advance the rewind baseline only for outcomes where the remote head was
	// legitimately accepted as authoritative (in sync, or integrated). A
	// refused outcome — diverged, blocked, or remote_behind — must NOT advance
	// the baseline to a head thane never accepted, or a later real rewind of
	// the true line would escape detection and be pushed over.
	switch res.Outcome {
	case checkout.SyncClean, checkout.SyncFastForwarded, checkout.SyncPushed:
		s.registry.AdvanceRemote(s.root, res.RemoteHead)
	}
	return st
}

// logFailure reports a failed sync pass. A persistent outage — a remote
// down for days — warns on arrival, on a change of error, and once per
// syncLogCoalesceAfter, instead of narrating one identical WARN per
// pass; the passes in between log at Debug so the forensic record stays
// complete.
//
// The mutex is held across the gate transition AND its emission (here
// and in logOutcome): released in between, a concurrent pass could
// reset the gate and narrate recovery before this pass narrates its
// failure, leaving the log stream showing a failure newer than the
// recovery while the gate reads clear. Contention is one pass per
// interval per root, so narrating under the lock costs nothing.
func (s *docRootSyncer) logFailure(err error, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, warn := s.failGate.admit(s.clock(), detail)
	if warn {
		s.logger.Warn("document root sync failed",
			"root", s.root, "error", err, "consecutive_failures", n)
		return
	}
	s.logger.Debug("document root sync still failing",
		"root", s.root, "error", err, "consecutive_failures", n)
}

// logOutcome reports a successful pass: recovery from a failure streak,
// attention outcomes coalesced the same way failures are, and the
// steady clean pass demoted to Debug — one Info per interval per root
// narrating "nothing changed" is what buried the warns that mattered.
func (s *docRootSyncer) logOutcome(res checkout.SyncResult) {
	// One lock span for the whole transition — gate mutations and their
	// narration stay ordered against a concurrent pass (see logFailure).
	s.mu.Lock()
	defer s.mu.Unlock()

	if recovered := s.failGate.reset(); recovered > 0 {
		s.logger.Info("document root sync recovered",
			"root", s.root, "outcome", res.Outcome, "failed_passes", recovered)
	}

	switch res.Outcome {
	case checkout.SyncBlocked, checkout.SyncDiverged, checkout.SyncRemoteBehind:
		n, warn := s.attentionGate.admit(s.clock(), string(res.Outcome)+": "+res.Detail)
		if warn {
			s.logger.Warn("document root sync needs attention",
				"root", s.root, "outcome", res.Outcome, "detail", res.Detail,
				"consecutive_passes", n)
		} else {
			s.logger.Debug("document root sync still needs attention",
				"root", s.root, "outcome", res.Outcome, "detail", res.Detail,
				"consecutive_passes", n)
		}
	default:
		if cleared := s.attentionGate.reset(); cleared > 0 {
			s.logger.Info("document root sync attention cleared",
				"root", s.root, "outcome", res.Outcome, "attention_passes", cleared)
		}
		if res.Outcome == checkout.SyncClean {
			s.logger.Debug("document root sync",
				"root", s.root, "outcome", res.Outcome, "ahead", res.Ahead, "behind", res.Behind)
		} else {
			s.logger.Info("document root sync",
				"root", s.root, "outcome", res.Outcome, "ahead", res.Ahead, "behind", res.Behind)
		}
	}
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
