package forge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// forgePollFetchLimit caps how many releases/commits the poller asks
// the provider for per repository per poll. The github.com upstream
// already clamps to 100; using the same value here keeps the API call
// cost predictable while giving substantial headroom over the prior
// 20-item ceiling. If a repository sees more than this many new events
// between polls, the older events at the back of the page are lost when
// the high-water mark advances — collectNewReleases/Commits surface that
// case so checkSubscription can log a warning.
const forgePollFetchLimit = 100

// SubscriptionPoller checks followed forge projects for new releases and
// commits, then wakes the loop declared by each subscription.
type SubscriptionPoller struct {
	manager      *Manager
	store        *SubscriptionStore
	bus          *messages.Bus
	logger       *slog.Logger
	checkoutSync func(context.Context, ProjectSubscription) (string, error)
}

// NewSubscriptionPoller creates a poller for forge project subscriptions.
func NewSubscriptionPoller(manager *Manager, store *SubscriptionStore, bus *messages.Bus, logger *slog.Logger) *SubscriptionPoller {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubscriptionPoller{
		manager:      manager,
		store:        store,
		bus:          bus,
		logger:       logger,
		checkoutSync: mirrorSubscriptionCheckoutSyncer{logger: logger}.Sync,
	}
}

// CheckSubscriptions polls all followed forge projects. It returns the number
// of event-source wakes delivered. Network errors are logged and skipped per
// subscription so one failing repository does not block the rest.
func (p *SubscriptionPoller) CheckSubscriptions(ctx context.Context) (int, error) {
	summary := loop.IterationSummary(ctx)

	if p.manager == nil || p.store == nil || p.bus == nil {
		return 0, fmt.Errorf("forge subscription poller is not configured")
	}

	subs, err := p.store.List()
	if err != nil {
		return 0, fmt.Errorf("list forge subscriptions: %w", err)
	}
	if len(subs) == 0 {
		if summary != nil {
			summary["subscriptions_checked"] = 0
		}
		return 0, nil
	}

	newReleaseCount := 0
	newCommitCount := 0
	eventWakeCount := 0

	for _, sub := range subs {
		updated, events, releases, commits, err := p.checkSubscription(ctx, sub)
		if err != nil {
			p.logger.Warn("forge subscription poll failed",
				"subscription_id", sub.ID,
				"account", sub.Account,
				"repo", sub.Repo,
				"error", err,
			)
			continue
		}
		newReleaseCount += releases
		newCommitCount += commits
		if len(events) == 0 {
			if err := p.store.Update(updated); err != nil {
				p.logger.Warn("forge subscription update failed",
					"subscription_id", sub.ID,
					"account", sub.Account,
					"repo", sub.Repo,
					"error", err,
				)
			}
			continue
		}
		delivered, err := p.dispatchEventBatches(ctx, sub, updated, events)
		eventWakeCount += delivered
		if err != nil {
			p.logger.Warn("forge subscription event batch failed",
				"subscription_id", sub.ID,
				"account", sub.Account,
				"repo", sub.Repo,
				"delivered_events", delivered,
				"error", err,
			)
			continue
		}
	}

	if summary != nil {
		summary["subscriptions_checked"] = len(subs)
		summary["new_releases"] = newReleaseCount
		summary["new_commits"] = newCommitCount
		if eventWakeCount > 0 {
			summary["event_wakes"] = eventWakeCount
		}
	}

	return eventWakeCount, nil
}

func (p *SubscriptionPoller) checkSubscription(ctx context.Context, sub ProjectSubscription) (ProjectSubscription, []messages.LoopEventPayload, int, int, error) {
	provider, err := p.manager.Account(sub.Account)
	if err != nil {
		return sub, nil, 0, 0, err
	}

	cutoff := sub.LastChecked
	if cutoff.IsZero() {
		cutoff = sub.CreatedAt
	}

	var events []messages.LoopEventPayload
	newReleaseCount := 0
	newCommitCount := 0

	if sub.TrackReleases {
		releases, err := provider.ListReleases(ctx, sub.Repo, forgePollFetchLimit)
		if err != nil {
			return sub, nil, 0, 0, err
		}
		newReleases, truncated := collectNewReleases(releases, sub.LastRelease, cutoff)
		if truncated {
			p.logger.Warn("forge release poll may have missed events",
				"subscription_id", sub.ID,
				"account", sub.Account,
				"repo", sub.Repo,
				"prior_marker", sub.LastRelease,
				"fetched", len(releases),
				"reason", "prior release marker not found within fetch window; older events beyond the page are unreachable without API pagination",
			)
		}
		newReleaseCount = len(newReleases)
		for _, release := range newReleases {
			events = append(events, releaseEvent(sub, release))
		}
		if len(releases) > 0 {
			sub.LastRelease = releaseMarker(releases[0])
			sub.LatestRelease = releaseTitle(releases[0])
		}
	}

	if sub.TrackCommits {
		commits, err := provider.ListCommits(ctx, sub.Repo, sub.Branch, forgePollFetchLimit)
		if err != nil {
			return sub, nil, 0, 0, err
		}
		newCommits, truncated := collectNewCommits(commits, sub.LastCommit, cutoff)
		if truncated {
			p.logger.Warn("forge commit poll may have missed events",
				"subscription_id", sub.ID,
				"account", sub.Account,
				"repo", sub.Repo,
				"branch", sub.Branch,
				"prior_marker", sub.LastCommit,
				"fetched", len(commits),
				"reason", "prior commit marker not found within fetch window; older commits beyond the page are unreachable without API pagination",
			)
		}
		newCommitCount = len(newCommits)
		for _, commit := range newCommits {
			events = append(events, commitEvent(sub, commit))
		}
		if len(commits) > 0 {
			sub.LastCommit = commits[0].SHA
			sub.LatestCommit = commitTitle(commits[0])
		}
	}

	if strings.TrimSpace(sub.CheckoutPath) != "" {
		checkoutSync := p.checkoutSync
		if checkoutSync == nil {
			checkoutSync = mirrorSubscriptionCheckoutSyncer{logger: p.logger}.Sync
		}
		remoteHead, err := checkoutSync(ctx, sub)
		if err != nil {
			return sub, nil, 0, 0, fmt.Errorf("sync local checkout: %w", err)
		}
		sub.LastSyncedSHA = remoteHead
		annotateSubscriptionEvents(events, sub)
	}

	sub.LastChecked = time.Now().UTC()
	return sub, events, newReleaseCount, newCommitCount, nil
}

func (p *SubscriptionPoller) dispatchEventBatches(ctx context.Context, sub, final ProjectSubscription, events []messages.LoopEventPayload) (int, error) {
	progress := initialBatchProgress(sub, final, events)
	delivered := 0

	for end := len(events); end > 0; end -= messages.MaxLoopEventsPerWake {
		start := end - messages.MaxLoopEventsPerWake
		if start < 0 {
			start = 0
		}
		chunk := events[start:end]
		if err := p.dispatchEvents(ctx, sub, chunk); err != nil {
			return delivered, err
		}
		delivered += len(chunk)

		progress = advanceSubscriptionProgress(progress, chunk)
		if err := p.store.Update(progress); err != nil {
			return delivered, fmt.Errorf("update forge subscription after event batch: %w", err)
		}
	}

	return delivered, nil
}

func (p *SubscriptionPoller) dispatchEvents(ctx context.Context, sub ProjectSubscription, events []messages.LoopEventPayload) error {
	env, err := messages.NewEventSourceEnvelope(
		messages.Identity{Kind: messages.IdentitySystem, Name: "forge_subscription_poller"},
		sub.WakeTarget,
		"forge_subscription",
		events,
	)
	if err != nil {
		return err
	}
	_, err = p.bus.Send(ctx, env)
	return err
}

func initialBatchProgress(sub, final ProjectSubscription, events []messages.LoopEventPayload) ProjectSubscription {
	progress := sub
	progress.LastChecked = final.LastChecked
	progress.CheckoutPath = final.CheckoutPath
	progress.CheckoutRemoteURL = final.CheckoutRemoteURL
	progress.LastSyncedSHA = final.LastSyncedSHA
	hasReleaseEvents := false
	hasCommitEvents := false
	for _, event := range events {
		switch event.Type {
		case "release":
			hasReleaseEvents = true
		case "commit":
			hasCommitEvents = true
		}
	}
	if !hasReleaseEvents {
		progress.LastRelease = final.LastRelease
		progress.LatestRelease = final.LatestRelease
	}
	if !hasCommitEvents {
		progress.LastCommit = final.LastCommit
		progress.LatestCommit = final.LatestCommit
	}
	return progress
}

func advanceSubscriptionProgress(sub ProjectSubscription, events []messages.LoopEventPayload) ProjectSubscription {
	releaseSeen := false
	commitSeen := false
	for _, event := range events {
		switch event.Type {
		case "release":
			if releaseSeen {
				continue
			}
			marker := event.Metadata["release_marker"]
			if marker == "" {
				marker = event.ID
			}
			sub.LastRelease = marker
			sub.LatestRelease = event.Title
			releaseSeen = true
		case "commit":
			if commitSeen {
				continue
			}
			sha := event.Metadata["sha"]
			if sha == "" {
				sha = event.ID
			}
			sub.LastCommit = sha
			sub.LatestCommit = event.Title
			commitSeen = true
		}
	}
	return sub
}

// collectNewReleases returns releases newer than the prior marker.
// truncated is true when a non-empty marker did not appear in the
// fetched batch — meaning older releases may exist between the prior
// marker and the oldest entry the poller saw, and those events are
// lost when the high-water mark advances. The caller is expected to
// surface that signal so missed events are at least observable.
func collectNewReleases(releases []*Release, marker string, cutoff time.Time) ([]*Release, bool) {
	if len(releases) == 0 {
		return nil, false
	}
	if marker == "" {
		return releasesAfter(releases, cutoff), false
	}

	var out []*Release
	found := false
	for _, release := range releases {
		if releaseMarker(release) == marker {
			found = true
			break
		}
		out = append(out, release)
	}
	if found {
		return out, false
	}
	return releasesAfter(releases, cutoff), true
}

// collectNewCommits returns commits newer than the prior marker;
// truncated has the same meaning as in collectNewReleases.
func collectNewCommits(commits []*Commit, marker string, cutoff time.Time) ([]*Commit, bool) {
	if len(commits) == 0 {
		return nil, false
	}
	if marker == "" {
		return commitsAfter(commits, cutoff), false
	}

	var out []*Commit
	found := false
	for _, commit := range commits {
		if commit.SHA == marker {
			found = true
			break
		}
		out = append(out, commit)
	}
	if found {
		return out, false
	}
	return commitsAfter(commits, cutoff), true
}

func releasesAfter(releases []*Release, cutoff time.Time) []*Release {
	if cutoff.IsZero() {
		return nil
	}
	out := make([]*Release, 0, len(releases))
	for _, release := range releases {
		if releaseTime(release).After(cutoff) {
			out = append(out, release)
		}
	}
	return out
}

func commitsAfter(commits []*Commit, cutoff time.Time) []*Commit {
	if cutoff.IsZero() {
		return nil
	}
	out := make([]*Commit, 0, len(commits))
	for _, commit := range commits {
		if commit.Date.After(cutoff) {
			out = append(out, commit)
		}
	}
	return out
}

func releaseEvent(sub ProjectSubscription, release *Release) messages.LoopEventPayload {
	observedAt := releaseTime(release)
	metadata := subscriptionMetadata(sub)
	metadata["release_marker"] = releaseMarker(release)
	if release != nil {
		metadata["tag"] = release.TagName
		if release.Prerelease {
			metadata["prerelease"] = "true"
		}
	}
	return messages.LoopEventPayload{
		Source:     "forge",
		Type:       "release",
		ID:         metadata["release_marker"],
		Title:      releaseTitle(release),
		URL:        releaseURL(release),
		ObservedAt: observedAt,
		Metadata:   metadata,
	}
}

func commitEvent(sub ProjectSubscription, commit *Commit) messages.LoopEventPayload {
	metadata := subscriptionMetadata(sub)
	if commit != nil {
		metadata["sha"] = commit.SHA
		metadata["author"] = commit.Author
	}
	return messages.LoopEventPayload{
		Source:     "forge",
		Type:       "commit",
		ID:         commitSHA(commit),
		Title:      commitTitle(commit),
		URL:        commitURL(commit),
		ObservedAt: commitTime(commit),
		Metadata:   metadata,
	}
}

func releaseMarker(release *Release) string {
	if release == nil {
		return ""
	}
	if release.ID != 0 {
		return fmt.Sprintf("id:%d", release.ID)
	}
	if release.TagName != "" {
		return "tag:" + release.TagName
	}
	return release.URL
}

func releaseTitle(release *Release) string {
	if release == nil {
		return ""
	}
	title := firstNonEmpty(release.Name, release.TagName, release.URL)
	if release.TagName != "" && release.Name != "" && release.Name != release.TagName {
		title = fmt.Sprintf("%s (%s)", release.Name, release.TagName)
	}
	return title
}

func releaseURL(release *Release) string {
	if release == nil {
		return ""
	}
	return release.URL
}

func releaseTime(release *Release) time.Time {
	if release == nil {
		return time.Time{}
	}
	if !release.PublishedAt.IsZero() {
		return release.PublishedAt
	}
	return release.CreatedAt
}

func commitTitle(commit *Commit) string {
	if commit == nil {
		return ""
	}
	line := commit.Message
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return firstNonEmpty(strings.TrimSpace(line), commit.SHA)
}

func commitSHA(commit *Commit) string {
	if commit == nil {
		return ""
	}
	return commit.SHA
}

func commitURL(commit *Commit) string {
	if commit == nil {
		return ""
	}
	return commit.URL
}

func commitTime(commit *Commit) time.Time {
	if commit == nil {
		return time.Time{}
	}
	return commit.Date
}
