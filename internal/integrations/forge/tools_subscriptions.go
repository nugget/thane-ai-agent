package forge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/checkout"
)

type repoFollowResponse struct {
	SubscriptionID string                  `json:"subscription_id"`
	Account        string                  `json:"account"`
	Repo           string                  `json:"repo"`
	Name           string                  `json:"name"`
	URL            string                  `json:"url,omitempty"`
	Branch         string                  `json:"branch,omitempty"`
	TrackReleases  bool                    `json:"track_releases"`
	TrackCommits   bool                    `json:"track_commits"`
	WakeLoop       messages.LoopWakeTarget `json:"wake_loop"`
	LocalCheckout  string                  `json:"local_checkout,omitempty"`
	LatestRelease  string                  `json:"latest_release,omitempty"`
	LatestCommit   string                  `json:"latest_commit,omitempty"`
	LastSyncedSHA  string                  `json:"last_synced_sha,omitempty"`
}

type repoSubscriptionEntry struct {
	SubscriptionID string                  `json:"subscription_id"`
	Account        string                  `json:"account"`
	Repo           string                  `json:"repo"`
	Name           string                  `json:"name"`
	URL            string                  `json:"url,omitempty"`
	Branch         string                  `json:"branch,omitempty"`
	TrackReleases  bool                    `json:"track_releases"`
	TrackCommits   bool                    `json:"track_commits"`
	WakeLoop       messages.LoopWakeTarget `json:"wake_loop"`
	LocalCheckout  string                  `json:"local_checkout,omitempty"`
	LatestRelease  string                  `json:"latest_release,omitempty"`
	LatestCommit   string                  `json:"latest_commit,omitempty"`
	LastSyncedSHA  string                  `json:"last_synced_sha,omitempty"`
	LastChecked    string                  `json:"last_checked,omitempty"`
	Created        string                  `json:"created,omitempty"`
}

type repoSubscriptionsResponse struct {
	Count         int                     `json:"count"`
	Subscriptions []repoSubscriptionEntry `json:"subscriptions"`
}

type repoUnfollowResponse struct {
	Action           string `json:"action"`
	SubscriptionID   string `json:"subscription_id"`
	LocalCheckout    string `json:"local_checkout,omitempty"`
	CheckoutRetained bool   `json:"checkout_retained,omitempty"`
}

// HandleRepoFollow follows a repository and wakes an existing loop when new
// releases and/or commits are detected.
func (t *Tools) HandleRepoFollow(ctx context.Context, args map[string]any) (string, error) {
	if t.subscriptions == nil {
		return "", fmt.Errorf("forge repository subscriptions are unavailable")
	}

	wakeTarget, wakeConfigured, err := messages.ParseLoopWakeTarget(args["wake_loop"])
	if err != nil {
		return "", fmt.Errorf("wake_loop: %w", err)
	}
	if !wakeConfigured {
		return "", fmt.Errorf("wake_loop is required")
	}
	if err := messages.VerifyLoopWakeTarget(wakeTarget, t.loopResolver); err != nil {
		return "", err
	}

	provider, repo, acct, err := t.resolveAccountAndRepo(args)
	if err != nil {
		return "", err
	}

	meta, err := provider.GetRepository(ctx, repo)
	if err != nil {
		return "", err
	}
	if meta == nil {
		return "", fmt.Errorf("repository %s not found", repo)
	}

	branch := strings.TrimSpace(stringArg(args, "branch"))
	if branch == "" {
		branch = meta.DefaultBranch
	}
	localCheckout := strings.TrimSpace(stringArg(args, "local_checkout"))
	checkoutRemoteURL := ""
	subscriptionID := SubscriptionID(acct, repo, branch, wakeTarget)
	if localCheckout != "" {
		if branch == "" {
			return "", fmt.Errorf("local_checkout requires branch because repository %s has no default branch; set branch", repo)
		}
		checkoutRemoteURL = repositoryCheckoutRemoteURL(meta)
		if checkoutRemoteURL == "" {
			return "", fmt.Errorf("local_checkout requires a clone URL for repository %s", repo)
		}
		mirror, err := checkout.OpenMirror(checkout.MirrorSpec{
			Name:         "forge subscription " + subscriptionID,
			WorktreePath: localCheckout,
			Logger:       t.logger,
		})
		if err != nil {
			return "", err
		}
		localCheckout = mirror.WorktreePath
	}
	trackReleases := boolArg(args, "track_releases", true)
	trackCommits := boolArg(args, "track_commits", true)
	if !trackReleases && !trackCommits {
		return "", fmt.Errorf("at least one of track_releases or track_commits must be true")
	}

	name := stringArg(args, "name")
	if name == "" {
		name = firstNonEmpty(meta.FullName, repo)
	}

	now := time.Now().UTC()
	sub := ProjectSubscription{
		ID:                subscriptionID,
		Account:           acct,
		Repo:              repo,
		Name:              name,
		URL:               meta.URL,
		Branch:            branch,
		CheckoutPath:      localCheckout,
		CheckoutRemoteURL: checkoutRemoteURL,
		TrackReleases:     trackReleases,
		TrackCommits:      trackCommits,
		WakeTarget:        wakeTarget,
		LastChecked:       now,
		CreatedAt:         now,
	}

	if trackReleases {
		releases, err := provider.ListReleases(ctx, repo, 1)
		if err != nil {
			return "", err
		}
		if len(releases) > 0 {
			sub.LastRelease = releaseMarker(releases[0])
			sub.LatestRelease = releaseTitle(releases[0])
		}
	}
	if trackCommits {
		commits, err := provider.ListCommits(ctx, repo, branch, 1)
		if err != nil {
			return "", err
		}
		if len(commits) > 0 {
			sub.LastCommit = commits[0].SHA
			sub.LatestCommit = commitTitle(commits[0])
		}
	}

	if err := t.subscriptions.Add(sub); err != nil {
		return "", err
	}

	t.recordOp("forge_repo_follow", acct, repo, sub.ID)
	return marshalResponse(repoFollowResponse{
		SubscriptionID: sub.ID,
		Account:        sub.Account,
		Repo:           sub.Repo,
		Name:           sub.Name,
		URL:            sub.URL,
		Branch:         sub.Branch,
		TrackReleases:  sub.TrackReleases,
		TrackCommits:   sub.TrackCommits,
		WakeLoop:       sub.WakeTarget,
		LocalCheckout:  sub.CheckoutPath,
		LatestRelease:  sub.LatestRelease,
		LatestCommit:   sub.LatestCommit,
		LastSyncedSHA:  sub.LastSyncedSHA,
	})
}

// HandleRepoUnfollow removes a repository subscription.
func (t *Tools) HandleRepoUnfollow(_ context.Context, args map[string]any) (string, error) {
	if t.subscriptions == nil {
		return "", fmt.Errorf("forge repository subscriptions are unavailable")
	}
	id := stringArg(args, "subscription_id")
	if id == "" {
		return "", fmt.Errorf("subscription_id is required")
	}
	sub, err := t.subscriptions.Get(id)
	if err != nil {
		return "", err
	}
	if err := t.subscriptions.Remove(id); err != nil {
		return "", err
	}
	return marshalResponse(repoUnfollowResponse{
		Action:           "unfollowed",
		SubscriptionID:   id,
		LocalCheckout:    sub.CheckoutPath,
		CheckoutRetained: sub.CheckoutPath != "",
	})
}

// HandleRepoSubscriptions lists repository subscriptions.
func (t *Tools) HandleRepoSubscriptions(_ context.Context, _ map[string]any) (string, error) {
	if t.subscriptions == nil {
		return "", fmt.Errorf("forge repository subscriptions are unavailable")
	}
	subs, err := t.subscriptions.List()
	if err != nil {
		return "", err
	}

	now := time.Now()
	entries := make([]repoSubscriptionEntry, 0, len(subs))
	for _, sub := range subs {
		entry := repoSubscriptionEntry{
			SubscriptionID: sub.ID,
			Account:        sub.Account,
			Repo:           sub.Repo,
			Name:           sub.Name,
			URL:            sub.URL,
			Branch:         sub.Branch,
			TrackReleases:  sub.TrackReleases,
			TrackCommits:   sub.TrackCommits,
			WakeLoop:       sub.WakeTarget,
			LocalCheckout:  sub.CheckoutPath,
			LatestRelease:  sub.LatestRelease,
			LatestCommit:   sub.LatestCommit,
			LastSyncedSHA:  sub.LastSyncedSHA,
		}
		if !sub.LastChecked.IsZero() {
			entry.LastChecked = promptfmt.FormatDeltaOnly(sub.LastChecked, now)
		}
		if !sub.CreatedAt.IsZero() {
			entry.Created = promptfmt.FormatDeltaOnly(sub.CreatedAt, now)
		}
		entries = append(entries, entry)
	}

	return marshalResponse(repoSubscriptionsResponse{
		Count:         len(entries),
		Subscriptions: entries,
	})
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}

func repositoryCheckoutRemoteURL(meta *Repository) string {
	if meta == nil {
		return ""
	}
	return strings.TrimSpace(meta.CloneURL)
}
