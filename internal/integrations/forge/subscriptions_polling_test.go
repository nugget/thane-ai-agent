package forge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

// enablePollingForTest marks polling live without standing up a real
// poller. It lives in a test file rather than on Service: production
// has no reason to swap a configured poller after construction, and an
// exported setter would let a caller silently disable one.
func enablePollingForTest(s *Service) {
	s.poller = &SubscriptionPoller{}
}

// newPollingTestTools builds follow-capable tools whose polling state
// the caller chooses.
func newPollingTestTools(t *testing.T, pollingEnabled bool) *Tools {
	t.Helper()
	provider := &mockProvider{
		name: "test",
		getRepositoryResult: &Repository{
			FullName:      "owner/repo",
			DefaultBranch: "main",
			URL:           "https://github.com/owner/repo",
			CloneURL:      "https://github.com/owner/repo.git",
		},
	}
	tools := newTestTools(provider, "owner")
	tools.subscriptions = newTestSubscriptionStore(t)
	workspace := t.TempDir()
	tools.service.workspacePath = workspace
	tools.service.rootResolver = paths.New(map[string]string{"core": workspace})
	if pollingEnabled {
		enablePollingForTest(tools.service)
	}
	return tools
}

// TestFollowWarnsWhenSubscriptionIsInert keeps the bare subscription
// allowed — the record becomes live when an operator enables polling —
// while refusing to let it pass as working. A caller told only "ok"
// would reasonably expect wakes that cannot arrive.
func TestFollowWarnsWhenSubscriptionIsInert(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, false)
	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":          "repo",
		"track_commits": true,
		"wake_loop":     map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow() = %v, want the subscription stored", err)
	}
	var resp struct {
		SubscriptionID string `json:"subscription_id"`
		Warning        string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Read it back rather than trusting the response: a returned ID
	// proves the handler built a reply, not that anything persisted.
	// Deleting the store write would leave a response-only assertion
	// green, and "stored but inert" is precisely the behavior this
	// test exists to protect.
	if resp.SubscriptionID == "" {
		t.Fatal("response carried no subscription id")
	}
	stored, err := tools.subscriptions.Get(resp.SubscriptionID)
	if err != nil {
		t.Fatalf("subscription %q was not persisted: %v", resp.SubscriptionID, err)
	}
	if stored.Repo == "" {
		t.Errorf("persisted subscription is empty: %+v", stored)
	}
	for _, want := range []string{"inert", "subscription_check_interval"} {
		if !strings.Contains(resp.Warning, want) {
			t.Errorf("warning = %q\nmissing %q", resp.Warning, want)
		}
	}
}

// TestFollowSilentWhenPollingLive keeps the caveat off the ordinary
// path — a warning that always fires is one nobody reads.
func TestFollowSilentWhenPollingLive(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, true)
	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":          "repo",
		"track_commits": true,
		"wake_loop":     map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}
	var resp struct {
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Warning != "" {
		t.Errorf("warning = %q, want none when polling is live", resp.Warning)
	}
}

// TestFollowCreatesTheCheckoutItPromises is the behavior the tool
// always implied and never delivered. checkout.OpenMirror is lazy by
// contract — it resolves a path and touches no disk, which is right
// for building many mirrors at startup and wrong for a caller who
// just asked for a working tree. Nothing created the checkout until
// the first poll, and where polling is disabled, never: production
// followed a repository, got ok back, and found no directory.
func TestFollowCreatesTheCheckoutItPromises(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, true)
	var synced ProjectSubscription
	tools.checkoutSync = func(_ context.Context, sub ProjectSubscription) (string, error) {
		synced = sub
		return "deadbeef", nil
	}

	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":      "repo",
		"branch":    "main",
		"repo_root": "thanecode",
		"wake_loop": map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}
	if synced.CheckoutPath == "" {
		t.Fatal("follow returned without creating the checkout it was asked for")
	}
	if synced.CheckoutRemoteURL == "" || synced.Branch != "main" {
		t.Errorf("sync got an underspecified subscription: %+v", synced)
	}

	// The head the initial sync reported is recorded, so the first poll
	// compares against reality instead of re-reporting the whole
	// history as new.
	var resp struct {
		SubscriptionID string `json:"subscription_id"`
		RepositoryRoot string `json:"repo_root"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := tools.subscriptions.Get(resp.SubscriptionID)
	if err != nil {
		t.Fatalf("subscription not persisted: %v", err)
	}
	if stored.LastSyncedSHA != "deadbeef" {
		t.Errorf("LastSyncedSHA = %q, want the initial sync head", stored.LastSyncedSHA)
	}
	if resp.RepositoryRoot != "thanecode" {
		t.Errorf("response repo_root = %q, want thanecode", resp.RepositoryRoot)
	}
}

// TestFollowDoesNotPersistWhenCheckoutFails keeps the failure honest.
// A subscription pointing at a checkout that could not be made is the
// phantom path this whole fix exists to remove, so a failed clone
// fails the call rather than persisting a promise.
func TestFollowDoesNotPersistWhenCheckoutFails(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, true)
	tools.checkoutSync = func(_ context.Context, sub ProjectSubscription) (string, error) {
		return "", errors.New("checkout " + sub.CheckoutPath + ": remote repository not found")
	}

	_, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":      "repo",
		"branch":    "main",
		"repo_root": "thanecode",
		"wake_loop": map[string]any{"name": "repo_curator"},
	})
	if err == nil {
		t.Fatal("HandleRepoFollow() reported success after the checkout failed")
	}
	if !strings.Contains(err.Error(), "repository root") {
		t.Errorf("error = %q, want it to name the repository root as the failure", err)
	}
	if strings.Contains(err.Error(), tools.service.workspacePath) {
		t.Errorf("error leaked the internal checkout path: %q", err)
	}
	subs, listErr := tools.subscriptions.List()
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(subs) != 0 {
		t.Errorf("stored %d subscriptions after a failed checkout; want none", len(subs))
	}
	if _, ok := tools.service.RepositoryRoot("thanecode"); ok {
		t.Error("failed checkout left its repository root registered")
	}
}

func TestFollowRollbackKeepsIdenticalRootCreatedByConcurrentWinner(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, true)
	wakeTarget := messages.LoopWakeTarget{Name: "repo_curator"}
	subscriptionID := SubscriptionID("test", "owner/repo", "main", wakeTarget)

	tools.checkoutSync = func(_ context.Context, sub ProjectSubscription) (string, error) {
		if err := tools.subscriptions.Add(sub); err != nil {
			t.Fatalf("persist concurrent winner: %v", err)
		}
		return "deadbeef", nil
	}
	_, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":      "repo",
		"branch":    "main",
		"repo_root": "thanecode",
		"wake_loop": map[string]any{"name": "repo_curator"},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("losing follow error = %v, want duplicate", err)
	}
	root, ok := tools.service.RepositoryRoot("thanecode")
	if !ok || root.Owner != subscriptionID {
		t.Fatalf("concurrent winner root was removed: %+v, ok=%v", root, ok)
	}
}

// TestFollowCreatesCheckoutEvenWithPollingDisabled covers the case
// production actually hit. The checkout is made now and is accurate
// now; what polling adds is that it keeps up. Refusing outright (the
// previous behavior) denied a caller something the tool can plainly
// do, so the caveat moved into the warning.
func TestFollowCreatesCheckoutEvenWithPollingDisabled(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, false)
	called := false
	tools.checkoutSync = func(_ context.Context, _ ProjectSubscription) (string, error) {
		called = true
		return "deadbeef", nil
	}

	raw, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":      "repo",
		"branch":    "main",
		"repo_root": "thanecode",
		"wake_loop": map[string]any{"name": "repo_curator"},
	})
	if err != nil {
		t.Fatalf("HandleRepoFollow: %v", err)
	}
	if !called {
		t.Error("checkout was not created when polling is disabled")
	}
	var resp struct {
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(resp.Warning, "will not refresh") {
		t.Errorf("warning = %q, want it to say the checkout will not stay current", resp.Warning)
	}
}

// TestSubscriptionListingWarnsWhenInert covers the reader who did not
// create these rows. The listing is where someone goes to find out
// whether a subscription is working, and without the caveat there it
// shows a healthy-looking set of records that will never fire.
func TestSubscriptionListingWarnsWhenInert(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		polling     bool
		wantWarning bool
	}{
		{name: "polling disabled warns", polling: false, wantWarning: true},
		{name: "polling live stays quiet", polling: true, wantWarning: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tools := newPollingTestTools(t, tc.polling)
			tools.checkoutSync = func(_ context.Context, _ ProjectSubscription) (string, error) {
				return "deadbeef", nil
			}
			if _, err := tools.HandleRepoFollow(context.Background(), map[string]any{
				"repo":          "repo",
				"track_commits": true,
				"wake_loop":     map[string]any{"name": "repo_curator"},
			}); err != nil {
				t.Fatalf("HandleRepoFollow: %v", err)
			}

			raw, err := tools.HandleRepoSubscriptions(context.Background(), nil)
			if err != nil {
				t.Fatalf("HandleRepoSubscriptions: %v", err)
			}
			var resp struct {
				Count   int    `json:"count"`
				Warning string `json:"warning"`
			}
			if err := json.Unmarshal([]byte(raw), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Count != 1 {
				t.Fatalf("count = %d, want the stored subscription listed", resp.Count)
			}
			if got := resp.Warning != ""; got != tc.wantWarning {
				t.Errorf("warning present = %v, want %v (warning=%q)", got, tc.wantWarning, resp.Warning)
			}
		})
	}
}

// TestFollowDoesNotTouchDiskWhenRejected covers the ordering that made
// a rejected follow destructive. Mirror.Sync resets the working tree
// hard, discarding local modifications by design, so running it before
// the store agrees to take the subscription meant a duplicate ID or a
// full table could wipe a directory and then decline to track it.
func TestFollowDoesNotTouchDiskWhenRejected(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, true)
	syncCalls := 0
	tools.checkoutSync = func(_ context.Context, _ ProjectSubscription) (string, error) {
		syncCalls++
		return "deadbeef", nil
	}

	args := map[string]any{
		"repo":      "repo",
		"branch":    "main",
		"repo_root": "thanecode",
		"wake_loop": map[string]any{"name": "repo_curator"},
	}

	if _, err := tools.HandleRepoFollow(context.Background(), args); err != nil {
		t.Fatalf("first follow: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("syncCalls = %d after the first follow, want 1", syncCalls)
	}

	// The same repo/branch/wake target yields the same subscription ID,
	// so this is refused as a duplicate.
	_, err := tools.HandleRepoFollow(context.Background(), args)
	if err == nil {
		t.Fatal("duplicate follow succeeded")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want the duplicate refusal", err)
	}
	if syncCalls != 1 {
		t.Errorf("syncCalls = %d; the rejected follow reset a working tree it then refused to track", syncCalls)
	}
}
