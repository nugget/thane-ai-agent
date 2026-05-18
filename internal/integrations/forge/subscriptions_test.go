package forge

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func newTestSubscriptionStore(t *testing.T) *SubscriptionStore {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	state, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	return NewSubscriptionStore(state, slog.New(slog.NewTextHandler(io.Discard, nil)), 10)
}

func TestSubscriptionStoreRequiresWakeTarget(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	err := store.Add(ProjectSubscription{
		ID:            "sub",
		Account:       "test",
		Repo:          "owner/repo",
		TrackReleases: true,
		CreatedAt:     time.Now(),
	})
	if err == nil {
		t.Fatal("expected wake_loop validation error")
	}
}

func TestHandleRepoFollowStoresWakeTarget(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	provider := &mockProvider{
		name: "test",
		getRepositoryResult: &Repository{
			FullName:      "owner/repo",
			DefaultBranch: "main",
			URL:           "https://github.com/owner/repo",
		},
		listReleasesResult: []*Release{{
			ID:          100,
			TagName:     "v1.0.0",
			Name:        "v1.0.0",
			URL:         "https://github.com/owner/repo/releases/tag/v1.0.0",
			PublishedAt: time.Now(),
		}},
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

	_, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"track_releases": true,
		"track_commits":  true,
		"wake_loop":      map[string]any{"name": "repo_curator", "force_supervisor": true},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}

	subs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscriptions len = %d, want 1", len(subs))
	}
	if subs[0].WakeTarget.Name != "repo_curator" || !subs[0].WakeTarget.ForceSupervisor {
		t.Fatalf("WakeTarget = %+v, want repo_curator supervisor target", subs[0].WakeTarget)
	}
	if subs[0].Repo != "owner/repo" || subs[0].Branch != "main" {
		t.Fatalf("subscription repo/branch = %s/%s", subs[0].Repo, subs[0].Branch)
	}
}

func TestSubscriptionPollerDispatchesStructuredEvents(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	now := time.Now().UTC()
	wakeTarget := messages.LoopWakeTarget{Name: "repo_curator", ForceSupervisor: true}
	sub := ProjectSubscription{
		ID:            "sub",
		Account:       "test",
		Repo:          "owner/repo",
		Name:          "owner/repo",
		Branch:        "main",
		TrackReleases: true,
		TrackCommits:  true,
		WakeTarget:    wakeTarget,
		LastRelease:   "tag:v1.0.0",
		LastCommit:    "oldsha",
		LastChecked:   now.Add(-2 * time.Hour),
		CreatedAt:     now.Add(-24 * time.Hour),
	}
	if err := store.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	provider := &mockProvider{
		name: "test",
		listReleasesResult: []*Release{
			{ID: 101, TagName: "v1.1.0", Name: "v1.1.0", URL: "https://release", PublishedAt: now.Add(-time.Hour)},
			{ID: 100, TagName: "v1.0.0", Name: "v1.0.0", URL: "https://old-release", PublishedAt: now.Add(-3 * time.Hour)},
		},
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

	var delivered messages.Envelope
	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		delivered = env
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})

	poller := NewSubscriptionPoller(mgr, store, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	count, err := poller.CheckSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("CheckSubscriptions: %v", err)
	}
	if count != 2 {
		t.Fatalf("event count = %d, want 2", count)
	}
	if delivered.To.Target != "repo_curator" {
		t.Fatalf("delivered target = %q, want repo_curator", delivered.To.Target)
	}
	payload, ok := delivered.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", delivered.Payload)
	}
	if !payload.ForceSupervisor {
		t.Fatal("expected force_supervisor on payload")
	}
	if len(payload.Events) != 2 {
		t.Fatalf("events len = %d, want 2", len(payload.Events))
	}
	if payload.Events[0].Type != "release" || payload.Events[1].Type != "commit" {
		t.Fatalf("event types = %s, %s", payload.Events[0].Type, payload.Events[1].Type)
	}
	if got := payload.Events[0].Metadata["subscription_id"]; got != "sub" {
		t.Fatalf("subscription metadata = %q, want sub", got)
	}
}

func TestSubscriptionPollerDeliveryFailureKeepsHighWater(t *testing.T) {
	t.Parallel()

	store := newTestSubscriptionStore(t)
	now := time.Now().UTC()
	sub := ProjectSubscription{
		ID:            "sub",
		Account:       "test",
		Repo:          "owner/repo",
		Name:          "owner/repo",
		Branch:        "main",
		TrackReleases: true,
		TrackCommits:  true,
		WakeTarget:    messages.LoopWakeTarget{Name: "repo_curator"},
		LastRelease:   "tag:v1.0.0",
		LastCommit:    "oldsha",
		LastChecked:   now.Add(-2 * time.Hour),
		CreatedAt:     now.Add(-24 * time.Hour),
	}
	if err := store.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	provider := &mockProvider{
		name: "test",
		listReleasesResult: []*Release{
			{ID: 101, TagName: "v1.1.0", Name: "v1.1.0", URL: "https://release", PublishedAt: now.Add(-time.Hour)},
			{ID: 100, TagName: "v1.0.0", Name: "v1.0.0", URL: "https://old-release", PublishedAt: now.Add(-3 * time.Hour)},
		},
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
	bus := messages.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)))

	poller := NewSubscriptionPoller(mgr, store, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	count, err := poller.CheckSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("CheckSubscriptions: %v", err)
	}
	if count != 0 {
		t.Fatalf("event count = %d, want 0 after failed wake delivery", count)
	}

	subs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscriptions len = %d, want 1", len(subs))
	}
	if subs[0].LastRelease != "tag:v1.0.0" || subs[0].LastCommit != "oldsha" {
		t.Fatalf("high-water markers advanced after failed wake: release=%q commit=%q", subs[0].LastRelease, subs[0].LastCommit)
	}
}
