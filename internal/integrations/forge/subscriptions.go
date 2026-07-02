package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

const (
	subscriptionNamespace = "forge_subscription"
	subscriptionIndexKey  = "subscriptions"
)

// ProjectSubscription tracks releases and/or commits for one repository
// and delivers new events to an existing loop. The subscription tools
// author it, the poller advances its cursor fields each cycle, and the
// optional checkout fields — always configured as a pair — additionally
// keep a local read-only mirror of the repository in sync so loops can
// read source without network calls.
type ProjectSubscription struct {
	// ID uniquely identifies the subscription across its lifetime;
	// tools use it for update and delete targeting.
	ID string `json:"subscription_id"`

	// Account names the configured forge account whose credentials and
	// host serve this subscription's API calls.
	Account string `json:"account"`

	// Repo is the owner/name slug of the repository being watched.
	Repo string `json:"repo"`

	// Name is the operator- or model-chosen display label for the
	// subscription, used in event payloads and status output.
	Name string `json:"name"`

	// URL is the repository's browse URL, carried for display and
	// event enrichment. Empty when the forge doesn't report one.
	URL string `json:"url,omitempty"`

	// Branch restricts commit tracking to one branch. Empty means the
	// repository's default branch.
	Branch string `json:"branch,omitempty"`

	// CheckoutPath is the absolute path of the local read-only mirror
	// checkout. Empty disables the checkout features for this
	// subscription.
	CheckoutPath string `json:"local_checkout,omitempty"`

	// CheckoutRemoteURL is the git remote the local mirror fetches
	// from. Set exactly when a checkout is configured; the
	// subscription tools validate and store it alongside the path.
	CheckoutRemoteURL string `json:"checkout_remote_url,omitempty"`

	// TrackReleases enables release polling for this subscription.
	TrackReleases bool `json:"track_releases"`

	// TrackCommits enables commit polling for this subscription.
	TrackCommits bool `json:"track_commits"`

	// WakeTarget names the loop that receives new forge events for
	// this subscription via the message bus.
	WakeTarget messages.LoopWakeTarget `json:"wake_loop"`

	// LastRelease is the poller's release cursor — the marker of the
	// newest release already delivered. Releases at or before it are
	// not re-announced. Empty until the first release is seen.
	LastRelease string `json:"last_release,omitempty"`

	// LastCommit is the poller's commit cursor — the SHA of the newest
	// commit already delivered. Empty until the first commit is seen.
	LastCommit string `json:"last_commit,omitempty"`

	// LatestRelease is the display title of the newest release seen,
	// carried for status output; the cursor twin is LastRelease.
	LatestRelease string `json:"latest_release,omitempty"`

	// LatestCommit is the display title of the newest commit seen,
	// carried for status output; the cursor twin is LastCommit.
	LatestCommit string `json:"latest_commit,omitempty"`

	// LastSyncedSHA is the remote head SHA at the last successful
	// mirror-checkout sync. Empty when no checkout is configured or no
	// sync has completed yet.
	LastSyncedSHA string `json:"last_synced_sha,omitempty"`

	// LastChecked is when the poller last completed a poll for this
	// subscription, successful or not. Zero before the first poll.
	LastChecked time.Time `json:"last_checked,omitempty"`

	// CreatedAt is when the subscription was authored.
	CreatedAt time.Time `json:"created_at"`
}

// SubscriptionStore persists forge project subscriptions in opstate.
type SubscriptionStore struct {
	state    *opstate.Store
	logger   *slog.Logger
	maxItems int
}

// NewSubscriptionStore creates a store for runtime-managed forge project
// subscriptions.
func NewSubscriptionStore(state *opstate.Store, logger *slog.Logger, maxItems int) *SubscriptionStore {
	if logger == nil {
		logger = slog.Default()
	}
	if maxItems <= 0 {
		maxItems = 50
	}
	return &SubscriptionStore{
		state:    state,
		logger:   logger,
		maxItems: maxItems,
	}
}

// SubscriptionID returns a deterministic short ID for a repository wake target.
func SubscriptionID(account, repo, branch string, target messages.LoopWakeTarget) string {
	selector := target.LoopID
	if selector == "" {
		selector = target.Name
	}
	h := sha256.Sum256([]byte(strings.Join([]string{account, repo, branch, selector}, "\x00")))
	return hex.EncodeToString(h[:6])
}

// Add persists a new subscription and appends it to the index.
func (s *SubscriptionStore) Add(sub ProjectSubscription) error {
	if s.state == nil {
		return fmt.Errorf("nil opstate store")
	}
	if err := validateSubscription(sub); err != nil {
		return err
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now().UTC()
	}

	ids, err := s.loadIndex()
	if err != nil {
		return fmt.Errorf("load subscription index: %w", err)
	}
	for _, id := range ids {
		if id == sub.ID {
			return fmt.Errorf("subscription %q already exists", sub.ID)
		}
	}
	if len(ids) >= s.maxItems {
		return fmt.Errorf("forge subscription limit reached (%d/%d)", len(ids), s.maxItems)
	}

	if err := s.write(sub); err != nil {
		return err
	}
	ids = append(ids, sub.ID)
	if err := s.saveIndex(ids); err != nil {
		return fmt.Errorf("save subscription index: %w", err)
	}
	return nil
}

// Update rewrites state for an existing subscription.
func (s *SubscriptionStore) Update(sub ProjectSubscription) error {
	if s.state == nil {
		return fmt.Errorf("nil opstate store")
	}
	if err := validateSubscription(sub); err != nil {
		return err
	}
	if _, err := s.Get(sub.ID); err != nil {
		return err
	}
	return s.write(sub)
}

// Remove deletes a subscription.
func (s *SubscriptionStore) Remove(id string) error {
	if s.state == nil {
		return fmt.Errorf("nil opstate store")
	}
	sub, err := s.Get(id)
	if err != nil {
		return err
	}
	if err := s.state.Delete(subscriptionNamespace, subscriptionKey(id)); err != nil {
		s.logger.Warn("failed to delete forge subscription", "id", id, "error", err)
	}

	ids, err := s.loadIndex()
	if err != nil {
		return fmt.Errorf("load subscription index: %w", err)
	}
	filtered := make([]string, 0, len(ids))
	for _, existing := range ids {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	if err := s.saveIndex(filtered); err != nil {
		return fmt.Errorf("save subscription index: %w", err)
	}

	s.logger.Info("forge subscription removed", "id", id, "repo", sub.Repo)
	return nil
}

// Get returns one subscription by ID.
func (s *SubscriptionStore) Get(id string) (ProjectSubscription, error) {
	sub, err := s.read(id)
	if err != nil {
		return ProjectSubscription{}, err
	}
	return sub, nil
}

// List returns all persisted subscriptions in index order.
func (s *SubscriptionStore) List() ([]ProjectSubscription, error) {
	if s.state == nil {
		return nil, fmt.Errorf("nil opstate store")
	}
	ids, err := s.loadIndex()
	if err != nil {
		return nil, fmt.Errorf("load subscription index: %w", err)
	}

	subs := make([]ProjectSubscription, 0, len(ids))
	for _, id := range ids {
		sub, err := s.read(id)
		if err != nil {
			s.logger.Warn("skipping invalid forge subscription", "id", id, "error", err)
			continue
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

func validateSubscription(sub ProjectSubscription) error {
	if strings.TrimSpace(sub.ID) == "" {
		return fmt.Errorf("subscription id is required")
	}
	if strings.TrimSpace(sub.Account) == "" {
		return fmt.Errorf("account is required")
	}
	if strings.TrimSpace(sub.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if !sub.TrackReleases && !sub.TrackCommits {
		return fmt.Errorf("at least one of track_releases or track_commits must be true")
	}
	if sub.WakeTarget.Empty() {
		return fmt.Errorf("wake_loop is required")
	}
	return nil
}

func subscriptionKey(id string) string {
	return "subscription:" + id
}

func (s *SubscriptionStore) write(sub ProjectSubscription) error {
	data, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("marshal subscription: %w", err)
	}
	if err := s.state.Set(subscriptionNamespace, subscriptionKey(sub.ID), string(data)); err != nil {
		return fmt.Errorf("store subscription %s: %w", sub.ID, err)
	}
	return nil
}

func (s *SubscriptionStore) read(id string) (ProjectSubscription, error) {
	raw, err := s.state.Get(subscriptionNamespace, subscriptionKey(id))
	if err != nil {
		return ProjectSubscription{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return ProjectSubscription{}, fmt.Errorf("subscription %q not found", id)
	}
	var sub ProjectSubscription
	if err := json.Unmarshal([]byte(raw), &sub); err != nil {
		return ProjectSubscription{}, fmt.Errorf("decode subscription %q: %w", id, err)
	}
	// The store key is the source of truth for the subscription's
	// identity. Reject — rather than silently accept — a stored
	// payload whose subscription_id contradicts its key: that state
	// is reachable only via corruption or a buggy writer, and
	// continuing would let later Update calls write the modified
	// record under a different key, orphaning the original entry.
	switch {
	case sub.ID == "":
		sub.ID = id
	case sub.ID != id:
		return ProjectSubscription{}, fmt.Errorf("subscription %q stored with mismatched payload id %q", id, sub.ID)
	}
	if err := validateSubscription(sub); err != nil {
		return ProjectSubscription{}, err
	}
	return sub, nil
}

func (s *SubscriptionStore) loadIndex() ([]string, error) {
	raw, err := s.state.Get(subscriptionNamespace, subscriptionIndexKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("decode index: %w", err)
	}
	return ids, nil
}

func (s *SubscriptionStore) saveIndex(ids []string) error {
	data, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	return s.state.Set(subscriptionNamespace, subscriptionIndexKey, string(data))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
