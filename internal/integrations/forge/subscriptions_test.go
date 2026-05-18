package forge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func newForgeTestStore(t *testing.T) *opstate.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := opstate.NewStore(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	return store
}

func TestRepoFollowInitializesHighWaterMarks(t *testing.T) {
	store := NewSubscriptionStore(newForgeTestStore(t), nil, 10)
	provider := &mockProvider{
		name: "github",
		getRepositoryResult: &Repository{
			FullName:      "owner/project",
			Name:          "project",
			DefaultBranch: "main",
			URL:           "https://github.com/owner/project",
		},
		listReleasesResult: []*Release{{
			ID:      10,
			TagName: "v1.0.0",
			Name:    "v1.0.0",
			URL:     "https://github.com/owner/project/releases/tag/v1.0.0",
		}},
		listCommitsResult: []*Commit{{
			SHA:     "abcdef1234567890",
			Message: "Initial release\n\nBody",
			Author:  "Dev",
			Date:    time.Now().UTC(),
			URL:     "https://github.com/owner/project/commit/abcdef1234567890",
		}},
	}
	tools := newTestTools(provider, "owner")
	tools.subscriptions = store

	result, err := tools.HandleRepoFollow(context.Background(), map[string]any{"repo": "project"})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}

	var out repoFollowResponse
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Repo != "owner/project" {
		t.Fatalf("Repo = %q, want owner/project", out.Repo)
	}
	if out.Branch != "main" {
		t.Fatalf("Branch = %q, want main", out.Branch)
	}
	if out.LatestRelease != "v1.0.0" {
		t.Fatalf("LatestRelease = %q, want v1.0.0", out.LatestRelease)
	}
	if out.LatestCommit != "Initial release" {
		t.Fatalf("LatestCommit = %q, want Initial release", out.LatestCommit)
	}

	sub, err := store.Get(out.SubscriptionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if sub.LastRelease != "id:10" {
		t.Fatalf("LastRelease = %q, want id:10", sub.LastRelease)
	}
	if sub.LastCommit != "abcdef1234567890" {
		t.Fatalf("LastCommit = %q, want full commit SHA", sub.LastCommit)
	}
}

func TestRepoSubscriptionsListAndUnfollow(t *testing.T) {
	store := NewSubscriptionStore(newForgeTestStore(t), nil, 10)
	provider := &mockProvider{
		name: "github",
		getRepositoryResult: &Repository{
			FullName:      "owner/project",
			DefaultBranch: "main",
		},
	}
	tools := newTestTools(provider, "owner")
	tools.subscriptions = store

	result, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "project",
		"track_releases": false,
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}

	var follow repoFollowResponse
	if err := json.Unmarshal([]byte(result), &follow); err != nil {
		t.Fatalf("unmarshal follow: %v", err)
	}

	listed, err := tools.HandleRepoSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("HandleRepoSubscriptions: %v", err)
	}
	if !strings.Contains(listed, `"count":1`) {
		t.Fatalf("list response = %s, want count 1", listed)
	}

	if _, err := tools.HandleRepoUnfollow(context.Background(), map[string]any{"subscription_id": follow.SubscriptionID}); err != nil {
		t.Fatalf("HandleRepoUnfollow: %v", err)
	}

	listed, err = tools.HandleRepoSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("HandleRepoSubscriptions after unfollow: %v", err)
	}
	if !strings.Contains(listed, `"count":0`) {
		t.Fatalf("list response after unfollow = %s, want count 0", listed)
	}
}

func TestSubscriptionPollerReportsNewReleasesAndCommits(t *testing.T) {
	state := newForgeTestStore(t)
	store := NewSubscriptionStore(state, nil, 10)

	oldTime := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	sub := ProjectSubscription{
		ID:            "sub1",
		Account:       "test",
		Repo:          "owner/project",
		Name:          "owner/project",
		Branch:        "main",
		TrackReleases: true,
		TrackCommits:  true,
		LastRelease:   "id:1",
		LastCommit:    "oldsha",
		LastChecked:   oldTime,
		CreatedAt:     oldTime,
	}
	if err := store.Add(sub); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	provider := &mockProvider{
		name: "github",
		listReleasesResult: []*Release{
			{ID: 2, TagName: "v2.0.0", Name: "v2.0.0", URL: "https://example.com/v2", PublishedAt: oldTime.Add(time.Hour)},
			{ID: 1, TagName: "v1.0.0", Name: "v1.0.0", URL: "https://example.com/v1", PublishedAt: oldTime.Add(-time.Hour)},
		},
		listCommitsResult: []*Commit{
			{SHA: "newsha", Message: "New change", Author: "Dev", Date: oldTime.Add(time.Hour), URL: "https://example.com/new"},
			{SHA: "oldsha", Message: "Old change", Author: "Dev", Date: oldTime.Add(-time.Hour), URL: "https://example.com/old"},
		},
	}
	mgr := &Manager{
		providers: map[string]ForgeProvider{"test": provider},
		configs:   map[string]AccountConfig{"test": {Name: "test", Owner: "owner"}},
		order:     []string{"test"},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	poller := NewSubscriptionPoller(mgr, store, nil)

	msg, err := poller.CheckSubscriptions(context.Background())
	if err != nil {
		t.Fatalf("CheckSubscriptions: %v", err)
	}
	if !strings.Contains(msg, "v2.0.0") {
		t.Fatalf("message = %q, want new release", msg)
	}
	if !strings.Contains(msg, "New change") {
		t.Fatalf("message = %q, want new commit", msg)
	}
	if strings.Contains(msg, "Old change") {
		t.Fatalf("message = %q, should not include old commit", msg)
	}

	updated, err := store.Get("sub1")
	if err != nil {
		t.Fatalf("store.Get updated: %v", err)
	}
	if updated.LastRelease != "id:2" {
		t.Fatalf("LastRelease = %q, want id:2", updated.LastRelease)
	}
	if updated.LastCommit != "newsha" {
		t.Fatalf("LastCommit = %q, want newsha", updated.LastCommit)
	}
}
