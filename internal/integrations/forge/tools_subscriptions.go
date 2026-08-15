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
	RepositoryRoot string                  `json:"repo_root,omitempty"`
	LatestRelease  string                  `json:"latest_release,omitempty"`
	LatestCommit   string                  `json:"latest_commit,omitempty"`
	LastSyncedSHA  string                  `json:"last_synced_sha,omitempty"`
	LastSyncedAge  string                  `json:"last_synced_age,omitempty"`

	// Warning names a condition that makes this subscription inert.
	// It is part of the success payload rather than an error because
	// the record is real and becomes live the moment the condition is
	// fixed — but a caller told only "ok" would reasonably expect
	// wakes that will never arrive.
	Warning string `json:"warning,omitempty"`
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
	RepositoryRoot string                  `json:"repo_root,omitempty"`
	LatestRelease  string                  `json:"latest_release,omitempty"`
	LatestCommit   string                  `json:"latest_commit,omitempty"`
	LastSyncedSHA  string                  `json:"last_synced_sha,omitempty"`
	LastSyncedAge  string                  `json:"last_synced_age,omitempty"`
	LastChecked    string                  `json:"last_checked,omitempty"`
	Created        string                  `json:"created,omitempty"`
}

type repoSubscriptionsResponse struct {
	Count         int                     `json:"count"`
	Subscriptions []repoSubscriptionEntry `json:"subscriptions"`

	// Warning repeats the inert-subscription caveat on the listing.
	// A reader who did not make these rows has no other way to learn
	// that none of them will fire, and the listing is where someone
	// goes to find out whether a subscription is working.
	Warning string `json:"warning,omitempty"`
}

type repoUnfollowResponse struct {
	Action           string `json:"action"`
	SubscriptionID   string `json:"subscription_id"`
	RepositoryRoot   string `json:"repo_root,omitempty"`
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

	provider, repo, acct, err := t.resolveAccountAndRepo(ctx, args)
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
	if legacyPath := strings.TrimSpace(stringArg(args, "local_checkout")); legacyPath != "" {
		return "", fmt.Errorf("local_checkout is no longer accepted because models cannot safely choose host paths; set repo_root to a named handle and Thane will choose the checkout location")
	}
	repositoryRoot := strings.TrimSpace(stringArg(args, "repo_root"))
	if repositoryRoot != "" {
		repositoryRoot, err = repositoryRootName(repositoryRoot)
		if err != nil {
			return "", err
		}
	}
	if bound := boundRepositoryRoot(ctx); bound != "" {
		if repositoryRoot != "" && repositoryRoot != bound {
			return "", fmt.Errorf("repository root %q is not available here: this loop is bound to root %q; retry with repo_root=%q, or omit repo_root to follow events without creating a checkout", repositoryRoot, bound, bound)
		}
	}
	localCheckout := ""
	checkoutRemoteURL := ""
	subscriptionID := SubscriptionID(acct, repo, branch, wakeTarget)
	if repositoryRoot != "" {
		if branch == "" {
			return "", fmt.Errorf("repo_root requires branch because repository %s has no default branch; set branch", repo)
		}
		checkoutRemoteURL = repositoryCheckoutRemoteURL(meta)
		if checkoutRemoteURL == "" {
			return "", fmt.Errorf("repo_root requires a clone URL for repository %s", repo)
		}
		localCheckout, err = t.service.repositoryCheckoutPath(repositoryRoot)
		if err != nil {
			return "", err
		}
		mirror, err := checkout.OpenMirror(checkout.MirrorSpec{
			Name:         "forge subscription " + subscriptionID,
			WorktreePath: localCheckout,
			Logger:       t.logger,
		})
		if err != nil {
			return "", hideRepositoryCheckoutPath(err, localCheckout)
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
		RepositoryRoot:    repositoryRoot,
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

	// Asking for a repository root is asking for a usable working tree, so
	// the follow creates one rather than merely filing an intention. OpenMirror is
	// lazy by contract — it resolves a path and touches no disk, which
	// is right for constructing many mirrors at startup and wrong here
	// — so nothing created the checkout until the first poll, and where
	// polling is disabled, never. Production followed a repository,
	// got ok, and found no directory.
	//
	// Synced before the record is stored: a subscription pointing at a
	// root that could not be made is the phantom state this prevents,
	// so a failed clone fails the call instead of persisting a promise.
	// Doing it here also keeps the failure attached to the moment that
	// can act on it — the caller still holds the repository, the path,
	// and the reason it asked. Deferred to the first poll, the same
	// error surfaces hours later in a loop that knows none of that.
	// Nothing irreversible before the store agrees to take this. A
	// duplicate ID or a full table would otherwise be discovered after
	// the clone, and Mirror.Sync resets the tree hard — so a rejected
	// follow could destroy a working directory it then declines to
	// track. Add re-checks; this is the early look that keeps the
	// destructive step from running first.
	if err := t.subscriptions.CheckAdmission(sub); err != nil {
		return "", err
	}
	rootCreated := false
	if sub.RepositoryRoot != "" {
		rootCreated, err = t.service.registerRepositoryRoot(sub.RepositoryRoot, sub.CheckoutPath, sub.ID)
		if err != nil {
			return "", err
		}
	}
	rollbackRoot := func() {
		if !rootCreated {
			return
		}
		// Another identical follow can persist the subscription after this
		// call creates the shared root but before this call reaches Add. Keep
		// that winner's now-durable root when this call loses the final race.
		persisted, getErr := t.subscriptions.Get(sub.ID)
		if getErr == nil &&
			persisted.RepositoryRoot == sub.RepositoryRoot &&
			persisted.CheckoutPath == sub.CheckoutPath {
			rootCreated = false
			return
		}
		t.service.unregisterRepositoryRoot(sub.RepositoryRoot, sub.ID)
		rootCreated = false
	}

	if strings.TrimSpace(sub.CheckoutPath) != "" {
		checkoutSync := t.checkoutSync
		if checkoutSync == nil {
			checkoutSync = mirrorSubscriptionCheckoutSyncer{logger: t.logger}.Sync
		}
		head, err := checkoutSync(ctx, sub)
		if err != nil {
			rollbackRoot()
			return "", fmt.Errorf("create repository root %q: %w", sub.RepositoryRoot, hideRepositoryCheckoutPath(err, sub.CheckoutPath))
		}
		sub.LastSyncedSHA = head
		sub.LastSyncedAt = time.Now().UTC()
	}

	if err := t.subscriptions.Add(sub); err != nil {
		rollbackRoot()
		return "", err
	}
	rootCreated = false

	t.recordOp("forge_repo_follow", acct, repo, sub.ID)
	// The agent gets this in its response; the operator gets it here.
	// Whoever set subscription_check_interval to zero is not the one
	// reading tool output, and a subscription that quietly does nothing
	// is exactly what they would want to know they just created.
	if warning := subscriptionInertWarning(t.service); warning != "" && t.logger != nil {
		t.logger.Warn("forge subscription created while repository polling is disabled",
			"subscription_id", sub.ID,
			"repo", sub.Repo,
			"account", sub.Account,
			"repo_root", sub.RepositoryRoot,
			"detail", warning)
	}

	response := repoFollowResponse{
		SubscriptionID: sub.ID,
		Account:        sub.Account,
		Repo:           sub.Repo,
		Name:           sub.Name,
		URL:            sub.URL,
		Branch:         sub.Branch,
		TrackReleases:  sub.TrackReleases,
		TrackCommits:   sub.TrackCommits,
		WakeLoop:       sub.WakeTarget,
		RepositoryRoot: sub.RepositoryRoot,
		LatestRelease:  sub.LatestRelease,
		LatestCommit:   sub.LatestCommit,
		LastSyncedSHA:  sub.LastSyncedSHA,
		Warning:        subscriptionInertWarning(t.service),
	}
	if !sub.LastSyncedAt.IsZero() {
		response.LastSyncedAge = promptfmt.FormatDeltaOnly(sub.LastSyncedAt, time.Now())
	}
	return marshalResponse(response)
}

// HandleRepoUnfollow removes a repository subscription.
func (t *Tools) HandleRepoUnfollow(ctx context.Context, args map[string]any) (string, error) {
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
	// Subscriptions are addressed by opaque ID rather than by account,
	// so without this check a bound caller could delete another
	// account's subscription simply by naming its ID — a destructive
	// reach the account binding is supposed to have closed.
	if bound := boundAccount(ctx); bound != "" && sub.Account != bound {
		return "", fmt.Errorf("subscription %q belongs to forge account %q, and this loop is bound to %q; it is not yours to remove",
			id, sub.Account, bound)
	}
	if bound := boundRepositoryRoot(ctx); bound != "" && sub.RepositoryRoot != bound {
		return "", fmt.Errorf("subscription %q owns repository root %q, and this loop is bound to root %q; it is not yours to remove",
			id, sub.RepositoryRoot, bound)
	}
	if err := t.subscriptions.Remove(id); err != nil {
		return "", err
	}
	t.service.unregisterRepositoryRoot(sub.RepositoryRoot, sub.ID)
	return marshalResponse(repoUnfollowResponse{
		Action:           "unfollowed",
		SubscriptionID:   id,
		RepositoryRoot:   sub.RepositoryRoot,
		CheckoutRetained: sub.CheckoutPath != "",
	})
}

// HandleRepoSubscriptions lists repository subscriptions.
func (t *Tools) HandleRepoSubscriptions(ctx context.Context, _ map[string]any) (string, error) {
	if t.subscriptions == nil {
		return "", fmt.Errorf("forge repository subscriptions are unavailable")
	}
	subs, err := t.subscriptions.List()
	if err != nil {
		return "", err
	}

	// Each entry names an account and a repository, so an unfiltered
	// listing hands a bound caller exactly the inventory of other
	// accounts its binding exists to withhold.
	bound := boundAccount(ctx)
	boundRoot := boundRepositoryRoot(ctx)

	now := time.Now()
	entries := make([]repoSubscriptionEntry, 0, len(subs))
	for _, sub := range subs {
		if bound != "" && sub.Account != bound {
			continue
		}
		if boundRoot != "" && sub.RepositoryRoot != boundRoot {
			continue
		}
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
			RepositoryRoot: sub.RepositoryRoot,
			LatestRelease:  sub.LatestRelease,
			LatestCommit:   sub.LatestCommit,
			LastSyncedSHA:  sub.LastSyncedSHA,
		}
		if !sub.LastChecked.IsZero() {
			entry.LastChecked = promptfmt.FormatDeltaOnly(sub.LastChecked, now)
		}
		if !sub.LastSyncedAt.IsZero() {
			entry.LastSyncedAge = promptfmt.FormatDeltaOnly(sub.LastSyncedAt, now)
		}
		if !sub.CreatedAt.IsZero() {
			entry.Created = promptfmt.FormatDeltaOnly(sub.CreatedAt, now)
		}
		entries = append(entries, entry)
	}

	return marshalResponse(repoSubscriptionsResponse{
		Count:         len(entries),
		Subscriptions: entries,
		Warning:       subscriptionListingInertWarning(t.service, len(entries)),
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

// subscriptionInertWarning returns the caveat to attach to a stored
// subscription that cannot currently do anything, or "" when polling
// is live.
//
// A subscription's only effect is delivered by the poller: it wakes a
// loop on new releases and commits, and syncs any local checkout. With
// polling disabled the record is durable and correct and completely
// silent, which is the shape most likely to be mistaken for working.
// subscriptionListingInertWarning is the listing's version of the
// caveat. It differs from the follow's in two ways that matter to a
// reader: a listed checkout reflects its last successful sync rather
// than this moment, and an empty listing has nothing to caveat — a
// warning attached to zero rows (an account filter matching nothing,
// say) describes a problem the reader does not have.
func subscriptionListingInertWarning(service *Service, listed int) string {
	if listed == 0 || service.SubscriptionPollingEnabled() {
		return ""
	}
	return "Repository polling is disabled at this site (forge.subscription_check_interval is unset or zero), so none of these subscriptions will wake a loop, and any repository root reflects its last successful sync rather than the current remote. The records persist and become live when polling is enabled."
}

func subscriptionInertWarning(service *Service) string {
	if service.SubscriptionPollingEnabled() {
		return ""
	}
	return "Stored, but inert: repository polling is disabled at this site (forge.subscription_check_interval is unset or zero), so this subscription will not wake any loop until an operator enables it. Any repository root was created now and is current as of this moment, but it will not refresh. The record persists and becomes live when polling is enabled."
}
