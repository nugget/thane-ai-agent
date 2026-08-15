package forge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

func TestHandleRepoFollowRegistersNamedRoot(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	workspace := t.TempDir()
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
	tools.service.workspacePath = workspace
	tools.service.rootResolver = paths.New(map[string]string{"core": workspace})
	// A local checkout is only meaningful when the poller runs.
	enablePollingForTest(tools.service)
	// The follow now creates the checkout; stub the clone so these
	// tests keep exercising subscription bookkeeping rather than git.
	syncedPath := ""
	tools.checkoutSync = func(_ context.Context, sub ProjectSubscription) (string, error) {
		syncedPath = sub.CheckoutPath
		return "synced-head", nil
	}
	_ = syncedPath

	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"track_releases": false,
		"track_commits":  true,
		"repo_root":      "thanecode",
		"wake_loop":      map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}
	var follow repoFollowResponse
	if err := json.Unmarshal([]byte(raw), &follow); err != nil {
		t.Fatalf("decode follow response: %v", err)
	}
	if follow.RepositoryRoot != "thanecode" {
		t.Fatalf("response repo_root = %q, want thanecode", follow.RepositoryRoot)
	}
	checkoutPath := filepath.Join(workspace, repositoryCheckoutDirectory, "thanecode")

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
	if subs[0].RepositoryRoot != "thanecode" {
		t.Fatalf("RepositoryRoot = %q, want thanecode", subs[0].RepositoryRoot)
	}
	if root, ok := tools.service.RepositoryRoot("thanecode"); !ok || root.Path != checkoutPath || !root.ReadOnly {
		t.Fatalf("registered root = %+v, ok=%v", root, ok)
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
	if len(list.Subscriptions) != 1 || list.Subscriptions[0].RepositoryRoot != "thanecode" {
		t.Fatalf("list repo_root = %+v, want thanecode", list.Subscriptions)
	}
	if strings.Contains(raw, checkoutPath) || strings.Contains(raw, "local_checkout") {
		t.Fatalf("model-facing listing leaked checkout path: %s", raw)
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
	if unfollow.RepositoryRoot != "thanecode" || !unfollow.CheckoutRetained {
		t.Fatalf("unfollow checkout response = %+v, want retained named root", unfollow)
	}
	if _, ok := tools.service.RepositoryRoot("thanecode"); ok {
		t.Fatal("repository root remained registered after unfollow")
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
		RepositoryRoot:    "thanecode",
		CheckoutPath:      t.TempDir(),
		CheckoutRemoteURL: "https://github.com/owner/repo.git",
		TrackCommits:      true,
		WakeTarget:        messages.LoopWakeTarget{Name: "repo_curator"},
		LastSyncedSHA:     "abc123",
		LastSyncedAt:      now,
		CreatedAt:         now,
	}
	if err := store.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := store.Get("sub")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RepositoryRoot != sub.RepositoryRoot || got.CheckoutPath != sub.CheckoutPath || got.CheckoutRemoteURL != sub.CheckoutRemoteURL || got.LastSyncedSHA != sub.LastSyncedSHA || !got.LastSyncedAt.Equal(sub.LastSyncedAt) {
		t.Fatalf("checkout fields = path:%q remote:%q sha:%q, want path:%q remote:%q sha:%q",
			got.CheckoutPath, got.CheckoutRemoteURL, got.LastSyncedSHA,
			sub.CheckoutPath, sub.CheckoutRemoteURL, sub.LastSyncedSHA,
		)
	}
}

func TestHandleRepoFollowRejectsNamedRootWithoutCloneURL(t *testing.T) {
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
	workspace := t.TempDir()
	tools.service.workspacePath = workspace
	tools.service.rootResolver = paths.New(map[string]string{"core": workspace})
	// A local checkout is only meaningful when the poller runs.
	enablePollingForTest(tools.service)
	// The follow now creates the checkout; stub the clone so these
	// tests keep exercising subscription bookkeeping rather than git.
	syncedPath := ""
	tools.checkoutSync = func(_ context.Context, sub ProjectSubscription) (string, error) {
		syncedPath = sub.CheckoutPath
		return "synced-head", nil
	}
	_ = syncedPath

	_, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"track_releases": false,
		"track_commits":  true,
		"repo_root":      "thanecode",
		"wake_loop":      map[string]any{"name": "repo_curator"},
	})
	if err == nil {
		t.Fatal("expected repo_root without clone URL to fail")
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
		RepositoryRoot:    "thanecode",
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
	if got := payload.Events[0].Metadata["repo_root"]; got != sub.RepositoryRoot {
		t.Fatalf("event repo_root = %q, want %q", got, sub.RepositoryRoot)
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

func TestRegisterPersistedRepositoryRootsMigratesLegacyCheckouts(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	workspace := t.TempDir()
	for _, sub := range []ProjectSubscription{
		{ID: "legacy-a", Account: "primary", Repo: "owner/repo", CheckoutPath: filepath.Join(workspace, "old", "repo-a"), CheckoutRemoteURL: "https://example.invalid/owner/repo.git", Branch: "main", TrackCommits: true, WakeTarget: messages.LoopWakeTarget{Name: "watcher"}, CreatedAt: time.Now()},
		{ID: "legacy-b", Account: "primary", Repo: "another/repo", CheckoutPath: filepath.Join(workspace, "old", "repo-b"), CheckoutRemoteURL: "https://example.invalid/another/repo.git", Branch: "main", TrackCommits: true, WakeTarget: messages.LoopWakeTarget{Name: "watcher"}, CreatedAt: time.Now()},
	} {
		if err := store.Add(sub); err != nil {
			t.Fatalf("seed %s: %v", sub.ID, err)
		}
	}
	service := &Service{
		subscriptions: store,
		workspacePath: workspace,
		rootResolver:  paths.New(map[string]string{"core": filepath.Join(workspace, "core")}),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := service.registerPersistedRepositoryRoots(); err != nil {
		t.Fatalf("registerPersistedRepositoryRoots: %v", err)
	}

	first, err := store.Get("legacy-a")
	if err != nil {
		t.Fatalf("Get legacy-a: %v", err)
	}
	second, err := store.Get("legacy-b")
	if err != nil {
		t.Fatalf("Get legacy-b: %v", err)
	}
	if first.RepositoryRoot != "repo" {
		t.Fatalf("first migrated root = %q, want repo", first.RepositoryRoot)
	}
	if second.RepositoryRoot != "repo-legacy-b" {
		t.Fatalf("collision root = %q, want repo-legacy-b", second.RepositoryRoot)
	}
	for _, sub := range []ProjectSubscription{first, second} {
		root, ok := service.RepositoryRoot(sub.RepositoryRoot)
		if !ok || root.Path != sub.CheckoutPath || root.Owner != sub.ID || !root.ReadOnly || root.Kind != paths.RootKindRepository {
			t.Errorf("registered root for %s = %+v, ok=%v", sub.ID, root, ok)
		}
	}

	// A second restore is idempotent and keeps the durable names rather than
	// inventing another collision suffix.
	if err := service.registerPersistedRepositoryRoots(); err != nil {
		t.Fatalf("second registerPersistedRepositoryRoots: %v", err)
	}
}

func TestRegisterPersistedRepositoryRootsWithoutResolverReturnsError(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	workspace := t.TempDir()
	if err := store.Add(ProjectSubscription{
		ID:                "persisted",
		Account:           "primary",
		Repo:              "owner/repo",
		RepositoryRoot:    "repo",
		CheckoutPath:      filepath.Join(workspace, "repos", "repo"),
		CheckoutRemoteURL: "https://example.invalid/owner/repo.git",
		Branch:            "main",
		TrackCommits:      true,
		WakeTarget:        messages.LoopWakeTarget{Name: "watcher"},
		CreatedAt:         time.Now(),
	}); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	service := &Service{
		subscriptions: store,
		workspacePath: workspace,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := service.registerPersistedRepositoryRoots(); err == nil || !strings.Contains(err.Error(), "named-root resolver is unavailable") {
		t.Fatalf("registerPersistedRepositoryRoots error = %v, want unavailable-resolver error", err)
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
