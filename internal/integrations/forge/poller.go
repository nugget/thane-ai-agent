package forge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// SubscriptionPoller checks followed forge projects for new releases
// and commits by comparing provider state against persisted high-water
// marks.
type SubscriptionPoller struct {
	manager *Manager
	store   *SubscriptionStore
	logger  *slog.Logger
}

// NewSubscriptionPoller creates a poller for forge project subscriptions.
func NewSubscriptionPoller(manager *Manager, store *SubscriptionStore, logger *slog.Logger) *SubscriptionPoller {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubscriptionPoller{
		manager: manager,
		store:   store,
		logger:  logger,
	}
}

// CheckSubscriptions checks all followed forge projects. It returns a
// formatted wake message when updates are found, or an empty string when
// there is nothing to dispatch.
func (p *SubscriptionPoller) CheckSubscriptions(ctx context.Context) (string, error) {
	summary := loop.IterationSummary(ctx)

	if p.manager == nil || p.store == nil {
		return "", fmt.Errorf("forge subscription poller is not configured")
	}

	subs, err := p.store.List()
	if err != nil {
		return "", fmt.Errorf("list forge subscriptions: %w", err)
	}
	if len(subs) == 0 {
		if summary != nil {
			summary["subscriptions_checked"] = 0
		}
		return "", nil
	}

	var sections []string
	newReleaseCount := 0
	newCommitCount := 0

	for _, sub := range subs {
		section, releases, commits, err := p.checkSubscription(ctx, sub)
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
		if section != "" {
			sections = append(sections, section)
		}
	}

	if summary != nil {
		summary["subscriptions_checked"] = len(subs)
		summary["new_releases"] = newReleaseCount
		summary["new_commits"] = newCommitCount
	}

	if len(sections) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("New code forge project updates detected:\n")
	for _, section := range sections {
		sb.WriteString("\n")
		sb.WriteString(section)
	}
	return sb.String(), nil
}

func (p *SubscriptionPoller) checkSubscription(ctx context.Context, sub ProjectSubscription) (string, int, int, error) {
	provider, err := p.manager.Account(sub.Account)
	if err != nil {
		return "", 0, 0, err
	}

	cutoff := sub.LastChecked
	if cutoff.IsZero() {
		cutoff = sub.CreatedAt
	}

	var newReleases []*Release
	var newCommits []*Commit

	if sub.TrackReleases {
		releases, err := provider.ListReleases(ctx, sub.Repo, 20)
		if err != nil {
			return "", 0, 0, err
		}
		newReleases = collectNewReleases(releases, sub.LastRelease, cutoff)
		if len(releases) > 0 {
			sub.LastRelease = releaseMarker(releases[0])
			sub.LatestRelease = releaseTitle(releases[0])
		}
	}

	if sub.TrackCommits {
		commits, err := provider.ListCommits(ctx, sub.Repo, sub.Branch, 20)
		if err != nil {
			return "", 0, 0, err
		}
		newCommits = collectNewCommits(commits, sub.LastCommit, cutoff)
		if len(commits) > 0 {
			sub.LastCommit = commits[0].SHA
			sub.LatestCommit = commitTitle(commits[0])
		}
	}

	sub.LastChecked = time.Now().UTC()
	if err := p.store.Update(sub); err != nil {
		return "", 0, 0, err
	}

	if len(newReleases) == 0 && len(newCommits) == 0 {
		return "", 0, 0, nil
	}

	return formatSubscriptionUpdate(sub, newReleases, newCommits), len(newReleases), len(newCommits), nil
}

func collectNewReleases(releases []*Release, marker string, cutoff time.Time) []*Release {
	if len(releases) == 0 {
		return nil
	}
	if marker == "" {
		return releasesAfter(releases, cutoff)
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
		return out
	}
	return releasesAfter(releases, cutoff)
}

func collectNewCommits(commits []*Commit, marker string, cutoff time.Time) []*Commit {
	if len(commits) == 0 {
		return nil
	}
	if marker == "" {
		return commitsAfter(commits, cutoff)
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
		return out
	}
	return commitsAfter(commits, cutoff)
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

func formatSubscriptionUpdate(sub ProjectSubscription, releases []*Release, commits []*Commit) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** (account: %s, repo: %s", sub.Name, sub.Account, sub.Repo)
	if sub.Branch != "" {
		fmt.Fprintf(&sb, ", branch: %s", sub.Branch)
	}
	fmt.Fprintf(&sb, ", subscription_id: %s)\n", sub.ID)

	if len(releases) > 0 {
		sb.WriteString("Releases:\n")
		for _, release := range releases {
			fmt.Fprintf(&sb, "- %s", releaseTitle(release))
			if release.Prerelease {
				sb.WriteString(" [prerelease]")
			}
			if release.URL != "" {
				fmt.Fprintf(&sb, "\n  %s", release.URL)
			}
			sb.WriteByte('\n')
		}
	}

	if len(commits) > 0 {
		sb.WriteString("Changes:\n")
		for _, commit := range commits {
			fmt.Fprintf(&sb, "- %s %s", shortSHA(commit.SHA), commitTitle(commit))
			if commit.Author != "" {
				fmt.Fprintf(&sb, " (%s)", commit.Author)
			}
			if commit.URL != "" {
				fmt.Fprintf(&sb, "\n  %s", commit.URL)
			}
			sb.WriteByte('\n')
		}
	}

	return sb.String()
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

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}
