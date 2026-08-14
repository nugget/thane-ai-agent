package forge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
	if pollingEnabled {
		enablePollingForTest(tools.service)
	}
	return tools
}

// TestFollowRefusesCheckoutWhenPollingDisabled covers a promise the
// site cannot keep. A local checkout is populated by the subscription
// poller and by nothing else, so with polling disabled the argument
// names a directory that will never exist. Production hit exactly
// this: forge_repo_follow returned ok, recorded the path, and no
// working tree ever appeared — indistinguishable, from the outside,
// from a clone that failed.
func TestFollowRefusesCheckoutWhenPollingDisabled(t *testing.T) {
	t.Parallel()

	tools := newPollingTestTools(t, false)
	_, err := tools.HandleRepoFollow(context.Background(), map[string]any{
		"repo":           "repo",
		"branch":         "main",
		"local_checkout": t.TempDir(),
		"wake_loop":      map[string]any{"name": "repo_curator"},
	})
	if err == nil {
		t.Fatal("HandleRepoFollow() accepted a checkout that nothing will populate")
	}
	for _, want := range []string{"polling is disabled", "subscription_check_interval", "without local_checkout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q\nmissing %q", err.Error(), want)
		}
	}
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
