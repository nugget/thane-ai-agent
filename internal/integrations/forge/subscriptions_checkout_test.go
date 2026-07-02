package forge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

func TestHandleRepoFollowStoresLocalCheckout(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	checkoutPath := t.TempDir()
	provider := &mockProvider{
		name: "test",
		getRepositoryResult: &Repository{
			FullName:      "owner/repo",
			DefaultBranch: "main",
			URL:           "https://github.com/owner/repo",
			CloneURL:      "https://github.com/owner/repo.git",
		},
		listCommitsResult: []*Commit{{
			SHA:     "abcdef123",
			Message: "initial commit",
			Author:  "Dev",
			Date:    time.Now(),
			URL:     "https://github.com/owner/repo/commit/abcdef123",
		}},
	}
	tools := newTestTools(provider, "owner")
	tools.subscriptions = store

	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"track_releases": false,
		"track_commits":  true,
		"local_checkout": checkoutPath,
		"wake_loop":      map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}
	var follow repoFollowResponse
	if err := json.Unmarshal([]byte(raw), &follow); err != nil {
		t.Fatalf("decode follow response: %v", err)
	}
	if follow.LocalCheckout != checkoutPath {
		t.Fatalf("response local_checkout = %q, want %q", follow.LocalCheckout, checkoutPath)
	}

	subs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscriptions len = %d, want 1", len(subs))
	}
	if subs[0].CheckoutPath != checkoutPath {
		t.Fatalf("CheckoutPath = %q, want %q", subs[0].CheckoutPath, checkoutPath)
	}
	if subs[0].CheckoutRemoteURL != "https://github.com/owner/repo.git" {
		t.Fatalf("CheckoutRemoteURL = %q, want clone URL", subs[0].CheckoutRemoteURL)
	}

	raw, err = tools.HandleRepoSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("HandleRepoSubscriptions: %v", err)
	}
	var list repoSubscriptionsResponse
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("decode subscriptions response: %v", err)
	}
	if len(list.Subscriptions) != 1 || list.Subscriptions[0].LocalCheckout != checkoutPath {
		t.Fatalf("list local_checkout = %+v, want %q", list.Subscriptions, checkoutPath)
	}

	raw, err = tools.HandleRepoUnfollow(context.Background(), map[string]any{
		"subscription_id": follow.SubscriptionID,
	})
	if err != nil {
		t.Fatalf("HandleRepoUnfollow: %v", err)
	}
	var unfollow repoUnfollowResponse
	if err := json.Unmarshal([]byte(raw), &unfollow); err != nil {
		t.Fatalf("decode unfollow response: %v", err)
	}
	if unfollow.LocalCheckout != checkoutPath || !unfollow.CheckoutRetained {
		t.Fatalf("unfollow checkout response = %+v, want retained %q", unfollow, checkoutPath)
	}
}

func TestSubscriptionStoreLocalCheckoutRoundTrip(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	now := time.Now().UTC()
	sub := ProjectSubscription{
		ID:                "sub",
		Account:           "test",
		Repo:              "owner/repo",
		Name:              "owner/repo",
		Branch:            "main",
		CheckoutPath:      t.TempDir(),
		CheckoutRemoteURL: "https://github.com/owner/repo.git",
		TrackCommits:      true,
		WakeTarget:        messages.LoopWakeTarget{Name: "repo_curator"},
		LastSyncedSHA:     "abc123",
		CreatedAt:         now,
	}
	if err := store.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := store.Get("sub")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CheckoutPath != sub.CheckoutPath || got.CheckoutRemoteURL != sub.CheckoutRemoteURL || got.LastSyncedSHA != sub.LastSyncedSHA {
		t.Fatalf("checkout fields = path:%q remote:%q sha:%q, want path:%q remote:%q sha:%q",
			got.CheckoutPath, got.CheckoutRemoteURL, got.LastSyncedSHA,
			sub.CheckoutPath, sub.CheckoutRemoteURL, sub.LastSyncedSHA,
		)
	}
}

func TestHandleRepoFollowRejectsLocalCheckoutWithoutCloneURL(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	provider := &mockProvider{
		name: "test",
		getRepositoryResult: &Repository{
			FullName:      "owner/repo",
			DefaultBranch: "main",
			URL:           "https://github.com/owner/repo",
		},
		listCommitsResult: []*Commit{{SHA: "abcdef123", Date: time.Now()}},
	}
	tools := newTestTools(provider, "owner")
	tools.subscriptions = store

	_, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"track_releases": false,
		"track_commits":  true,
		"local_checkout": t.TempDir(),
		"wake_loop":      map[string]any{"name": "repo_curator"},
	})
	if err == nil {
		t.Fatal("expected local_checkout without clone URL to fail")
	}
	if !strings.Contains(err.Error(), "requires a clone URL") {
		t.Fatalf("error = %q, want clone URL guidance", err)
	}
}

func TestSubscriptionCheckoutSyncRequiresRemoteURL(t *testing.T) {
	t.Parallel()

	syncer := mirrorSubscriptionCheckoutSyncer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err := syncer.Sync(context.Background(), ProjectSubscription{
		ID:           "sub",
		Repo:         "owner/repo",
		Branch:       "main",
		URL:          "https://github.com/owner/repo",
		CheckoutPath: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected missing checkout_remote_url to fail")
	}
	if !strings.Contains(err.Error(), "no checkout_remote_url") {
		t.Fatalf("error = %q, want checkout_remote_url guidance", err)
	}
}

func TestSubscriptionPollerSyncsLocalCheckoutBeforeWake(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	now := time.Now().UTC()
	sub := ProjectSubscription{
		ID:                "sub",
		Account:           "test",
		Repo:              "owner/repo",
		Name:              "owner/repo",
		Branch:            "main",
		CheckoutPath:      t.TempDir(),
		CheckoutRemoteURL: "https://github.com/owner/repo.git",
		TrackCommits:      true,
		WakeTarget:        messages.LoopWakeTarget{Name: "repo_curator"},
		LastCommit:        "oldsha",
		LastChecked:       now.Add(-2 * time.Hour),
		CreatedAt:         now.Add(-24 * time.Hour),
	}
	if err := store.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	provider := &mockProvider{
		name: "test",
		listCommitsResult: []*Commit{
			{SHA: "newsha", Message: "add feature", Author: "Dev", Date: now.Add(-30 * time.Minute), URL: "https://commit"},
			{SHA: "oldsha", Message: "old feature", Author: "Dev", Date: now.Add(-3 * time.Hour), URL: "https://old-commit"},
		},
	}
	mgr := &Manager{
		providers: map[string]ForgeProvider{"test": provider},
		configs:   map[string]AccountConfig{"test": {Name: "test", Owner: "owner"}},
		order:     []string{"test"},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	syncer := &recordingSubscriptionCheckoutSyncer{sha: "syncedsha"}
	var delivered messages.Envelope
	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		delivered = env
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})

	poller := NewSubscriptionPoller(mgr, store, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	poller.checkoutSync = syncer.Sync
	count, err := poller.CheckSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("CheckSubscriptions: %v", err)
	}
	if count != 1 {
		t.Fatalf("event count = %d, want 1", count)
	}
	if len(syncer.calls) != 1 {
		t.Fatalf("checkout sync calls = %d, want 1", len(syncer.calls))
	}
	if syncer.calls[0].CheckoutPath != sub.CheckoutPath || syncer.calls[0].CheckoutRemoteURL != sub.CheckoutRemoteURL {
		t.Fatalf("checkout sync subscription = %+v, want path %q remote %q", syncer.calls[0], sub.CheckoutPath, sub.CheckoutRemoteURL)
	}
	payload, ok := delivered.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", delivered.Payload)
	}
	if got := payload.Events[0].Metadata["local_checkout"]; got != sub.CheckoutPath {
		t.Fatalf("event local_checkout = %q, want %q", got, sub.CheckoutPath)
	}
	if got := payload.Events[0].Metadata["last_synced_sha"]; got != "syncedsha" {
		t.Fatalf("event last_synced_sha = %q, want syncedsha", got)
	}

	subs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscriptions len = %d, want 1", len(subs))
	}
	if subs[0].LastSyncedSHA != "syncedsha" {
		t.Fatalf("LastSyncedSHA = %q, want syncedsha", subs[0].LastSyncedSHA)
	}
}

type recordingSubscriptionCheckoutSyncer struct {
	sha   string
	err   error
	calls []ProjectSubscription
}

func (s *recordingSubscriptionCheckoutSyncer) Sync(_ context.Context, sub ProjectSubscription) (string, error) {
	s.calls = append(s.calls, sub)
	return s.sha, s.err
}
