package forge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

const (
	subscriptionNamespace = "forge_subscription"
	subscriptionIndexKey  = "subscriptions"
)

func subscriptionKeyAccount(id string) string       { return "subscription:" + id + ":account" }
func subscriptionKeyRepo(id string) string          { return "subscription:" + id + ":repo" }
func subscriptionKeyName(id string) string          { return "subscription:" + id + ":name" }
func subscriptionKeyURL(id string) string           { return "subscription:" + id + ":url" }
func subscriptionKeyBranch(id string) string        { return "subscription:" + id + ":branch" }
func subscriptionKeyTrackReleases(id string) string { return "subscription:" + id + ":track_releases" }
func subscriptionKeyTrackCommits(id string) string  { return "subscription:" + id + ":track_commits" }
func subscriptionKeyLastRelease(id string) string   { return "subscription:" + id + ":last_release" }
func subscriptionKeyLastCommit(id string) string    { return "subscription:" + id + ":last_commit" }
func subscriptionKeyLatestRelease(id string) string { return "subscription:" + id + ":latest_release" }
func subscriptionKeyLatestCommit(id string) string  { return "subscription:" + id + ":latest_commit" }
func subscriptionKeyLastChecked(id string) string   { return "subscription:" + id + ":last_checked" }
func subscriptionKeyCreatedAt(id string) string     { return "subscription:" + id + ":created_at" }

// ProjectSubscription tracks releases and/or commits for one forge repo.
type ProjectSubscription struct {
	ID            string    `json:"subscription_id"`
	Account       string    `json:"account"`
	Repo          string    `json:"repo"`
	Name          string    `json:"name"`
	URL           string    `json:"url,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	TrackReleases bool      `json:"track_releases"`
	TrackCommits  bool      `json:"track_commits"`
	LastRelease   string    `json:"last_release,omitempty"`
	LastCommit    string    `json:"last_commit,omitempty"`
	LatestRelease string    `json:"latest_release,omitempty"`
	LatestCommit  string    `json:"latest_commit,omitempty"`
	LastChecked   time.Time `json:"last_checked,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// SubscriptionStore persists forge project subscriptions in opstate.
type SubscriptionStore struct {
	state    *opstate.Store
	logger   *slog.Logger
	maxItems int
}

// NewSubscriptionStore creates a store for runtime-managed forge
// project subscriptions.
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

// SubscriptionID returns a deterministic short ID for an account/repo/branch.
func SubscriptionID(account, repo, branch string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{account, repo, branch}, "\x00")))
	return hex.EncodeToString(h[:6])
}

// Add persists a new subscription and appends it to the index.
func (s *SubscriptionStore) Add(sub ProjectSubscription) error {
	if s.state == nil {
		return fmt.Errorf("nil opstate store")
	}
	if sub.ID == "" {
		return fmt.Errorf("subscription id is required")
	}
	if sub.Account == "" {
		return fmt.Errorf("account is required")
	}
	if sub.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if !sub.TrackReleases && !sub.TrackCommits {
		return fmt.Errorf("at least one of track_releases or track_commits must be true")
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
	if sub.ID == "" {
		return fmt.Errorf("subscription id is required")
	}
	if _, err := s.Get(sub.ID); err != nil {
		return err
	}
	return s.write(sub)
}

// Remove deletes a subscription and all stored keys.
func (s *SubscriptionStore) Remove(id string) error {
	if s.state == nil {
		return fmt.Errorf("nil opstate store")
	}
	sub, err := s.Get(id)
	if err != nil {
		return err
	}
	for _, key := range []string{
		subscriptionKeyAccount(id),
		subscriptionKeyRepo(id),
		subscriptionKeyName(id),
		subscriptionKeyURL(id),
		subscriptionKeyBranch(id),
		subscriptionKeyTrackReleases(id),
		subscriptionKeyTrackCommits(id),
		subscriptionKeyLastRelease(id),
		subscriptionKeyLastCommit(id),
		subscriptionKeyLatestRelease(id),
		subscriptionKeyLatestCommit(id),
		subscriptionKeyLastChecked(id),
		subscriptionKeyCreatedAt(id),
	} {
		if err := s.state.Delete(subscriptionNamespace, key); err != nil {
			s.logger.Warn("failed to delete forge subscription key", "key", key, "error", err)
		}
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
	subs, err := s.List()
	if err != nil {
		return ProjectSubscription{}, err
	}
	for _, sub := range subs {
		if sub.ID == id {
			return sub, nil
		}
	}
	return ProjectSubscription{}, fmt.Errorf("subscription %q not found", id)
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

func (s *SubscriptionStore) write(sub ProjectSubscription) error {
	values := map[string]string{
		subscriptionKeyAccount(sub.ID):       sub.Account,
		subscriptionKeyRepo(sub.ID):          sub.Repo,
		subscriptionKeyName(sub.ID):          sub.Name,
		subscriptionKeyURL(sub.ID):           sub.URL,
		subscriptionKeyBranch(sub.ID):        sub.Branch,
		subscriptionKeyTrackReleases(sub.ID): strconv.FormatBool(sub.TrackReleases),
		subscriptionKeyTrackCommits(sub.ID):  strconv.FormatBool(sub.TrackCommits),
		subscriptionKeyLastRelease(sub.ID):   sub.LastRelease,
		subscriptionKeyLastCommit(sub.ID):    sub.LastCommit,
		subscriptionKeyLatestRelease(sub.ID): sub.LatestRelease,
		subscriptionKeyLatestCommit(sub.ID):  sub.LatestCommit,
		subscriptionKeyLastChecked(sub.ID):   formatSubscriptionTime(sub.LastChecked),
		subscriptionKeyCreatedAt(sub.ID):     formatSubscriptionTime(sub.CreatedAt),
	}
	for key, value := range values {
		if err := s.state.Set(subscriptionNamespace, key, value); err != nil {
			return fmt.Errorf("store %s: %w", key, err)
		}
	}
	return nil
}

func (s *SubscriptionStore) read(id string) (ProjectSubscription, error) {
	get := func(key string) (string, error) {
		return s.state.Get(subscriptionNamespace, key)
	}

	account, err := get(subscriptionKeyAccount(id))
	if err != nil {
		return ProjectSubscription{}, err
	}
	repo, err := get(subscriptionKeyRepo(id))
	if err != nil {
		return ProjectSubscription{}, err
	}
	if account == "" || repo == "" {
		return ProjectSubscription{}, fmt.Errorf("missing account or repo")
	}

	name, _ := get(subscriptionKeyName(id))
	url, _ := get(subscriptionKeyURL(id))
	branch, _ := get(subscriptionKeyBranch(id))
	lastRelease, _ := get(subscriptionKeyLastRelease(id))
	lastCommit, _ := get(subscriptionKeyLastCommit(id))
	latestRelease, _ := get(subscriptionKeyLatestRelease(id))
	latestCommit, _ := get(subscriptionKeyLatestCommit(id))
	lastCheckedRaw, _ := get(subscriptionKeyLastChecked(id))
	createdAtRaw, _ := get(subscriptionKeyCreatedAt(id))
	trackReleasesRaw, _ := get(subscriptionKeyTrackReleases(id))
	trackCommitsRaw, _ := get(subscriptionKeyTrackCommits(id))

	trackReleases := parseSubscriptionBool(trackReleasesRaw, true)
	trackCommits := parseSubscriptionBool(trackCommitsRaw, true)
	lastChecked := parseSubscriptionTime(lastCheckedRaw)
	createdAt := parseSubscriptionTime(createdAtRaw)

	return ProjectSubscription{
		ID:            id,
		Account:       account,
		Repo:          repo,
		Name:          firstNonEmpty(name, repo),
		URL:           url,
		Branch:        branch,
		TrackReleases: trackReleases,
		TrackCommits:  trackCommits,
		LastRelease:   lastRelease,
		LastCommit:    lastCommit,
		LatestRelease: latestRelease,
		LatestCommit:  latestCommit,
		LastChecked:   lastChecked,
		CreatedAt:     createdAt,
	}, nil
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

func parseSubscriptionBool(raw string, fallback bool) bool {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func parseSubscriptionTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

func formatSubscriptionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
