package forge

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

const repositoryCheckoutDirectory = "repos"

func hideRepositoryCheckoutPath(err error, checkoutPath string) error {
	if err == nil {
		return nil
	}
	detail := err.Error()
	if strings.TrimSpace(checkoutPath) != "" {
		detail = strings.ReplaceAll(detail, checkoutPath, "[internal repository checkout]")
	}
	return errors.New(detail)
}

// repositoryRootName validates and canonicalizes a model-facing repository
// root handle. The conservative alphabet keeps the root unambiguous in the
// existing name:path grammar on every supported platform.
func repositoryRootName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ":"))
	if name == "" {
		return "", fmt.Errorf("repo_root is required")
	}
	if len(name) > 64 {
		return "", fmt.Errorf("repo_root %q is too long; use at most 64 characters", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (i > 0 && (c == '.' || c == '_' || c == '-')) {
			continue
		}
		return "", fmt.Errorf("repo_root %q is invalid; use lowercase ASCII letters, digits, '.', '_', or '-', starting with a letter or digit", name)
	}
	return name, nil
}

func (s *Service) repositoryCheckoutPath(rootName string) (string, error) {
	if strings.TrimSpace(s.workspacePath) == "" {
		return "", fmt.Errorf("repo_root requires workspace.path so Thane can choose the checkout location")
	}
	absWorkspace, err := filepath.Abs(paths.ExpandHome(s.workspacePath))
	if err != nil {
		return "", fmt.Errorf("resolve workspace for repo_root %q: %w", rootName, err)
	}
	return filepath.Join(absWorkspace, repositoryCheckoutDirectory, rootName), nil
}

func (s *Service) validatePersistedRepositoryCheckout(checkoutPath string) error {
	if strings.TrimSpace(s.workspacePath) == "" {
		return fmt.Errorf("workspace.path is not configured")
	}
	absWorkspace, err := filepath.Abs(paths.ExpandHome(s.workspacePath))
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	if !paths.ContainsPath(absWorkspace, checkoutPath) {
		return fmt.Errorf("checkout is outside workspace.path")
	}
	return nil
}

func (s *Service) registerRepositoryRoot(name, checkoutPath, owner string) (bool, error) {
	if s.rootResolver == nil {
		return false, fmt.Errorf("repo_root %q cannot be registered because the named-root resolver is unavailable", name)
	}
	return s.rootResolver.RegisterCreated(paths.Root{
		Name:     name,
		Path:     checkoutPath,
		Kind:     paths.RootKindRepository,
		ReadOnly: true,
		Owner:    owner,
	})
}

func (s *Service) unregisterRepositoryRoot(name, owner string) {
	if s == nil || s.rootResolver == nil {
		return
	}
	s.rootResolver.Unregister(name, owner)
}

// RepositoryRoot reports a registered repository root by name.
func (s *Service) RepositoryRoot(name string) (paths.Root, bool) {
	if s == nil || s.rootResolver == nil {
		return paths.Root{}, false
	}
	root, ok := s.rootResolver.Root(name)
	return root, ok && root.Kind == paths.RootKindRepository
}

// ValidateRepositoryRootBinding verifies that name is a canonical live
// repository root and, when account is non-empty, that the subscription which
// owns the root uses that forge account. Loop hydration uses this to keep its
// account, context, file, and repository-history boundaries aligned.
func (s *Service) ValidateRepositoryRootBinding(name, account string) error {
	canonical, err := repositoryRootName(name)
	if err != nil {
		return err
	}
	if name != canonical {
		return fmt.Errorf("repo_root %q is not canonical; use %q", name, canonical)
	}
	root, ok := s.RepositoryRoot(canonical)
	if !ok {
		return fmt.Errorf("no repository subscription exposes named root %q", canonical)
	}
	if account == "" {
		return nil
	}
	if s.subscriptions == nil {
		return fmt.Errorf("repository subscription state is unavailable")
	}
	sub, err := s.subscriptions.Get(root.Owner)
	if err != nil {
		return fmt.Errorf("load subscription owning repo_root %q: %w", canonical, err)
	}
	if sub.RepositoryRoot != root.Name || sub.CheckoutPath != root.Path {
		return fmt.Errorf("repo_root %q is not backed by its owning repository subscription", canonical)
	}
	if sub.Account != account {
		return fmt.Errorf("repo_root %q belongs to forge account %q, not bound account %q", canonical, sub.Account, account)
	}
	return nil
}

// registerPersistedRepositoryRoots restores the in-memory root registry from
// the durable subscription store before loop definitions hydrate. Legacy
// subscriptions that predate repo_root receive a deterministic handle and are
// rewritten once so subsequent boots keep the same model-facing name.
func (s *Service) registerPersistedRepositoryRoots() error {
	if s == nil || s.subscriptions == nil {
		return nil
	}

	subs, err := s.subscriptions.List()
	if err != nil {
		return fmt.Errorf("restore repository roots: %w", err)
	}
	for _, sub := range subs {
		if strings.TrimSpace(sub.CheckoutPath) == "" {
			continue
		}
		if err := s.validatePersistedRepositoryCheckout(sub.CheckoutPath); err != nil {
			s.logSkippedRepositoryRoot(sub, sub.RepositoryRoot, err)
			continue
		}
		name := strings.TrimSpace(sub.RepositoryRoot)
		migrated := false
		if name == "" {
			name = s.legacyRepositoryRootName(sub)
			migrated = true
		}
		storedName := strings.TrimSuffix(name, ":")
		name, err = repositoryRootName(name)
		if err != nil {
			s.logSkippedRepositoryRoot(sub, name, err)
			continue
		}
		if name != storedName {
			migrated = true
		}
		sub.RepositoryRoot = name
		created, err := s.registerRepositoryRoot(name, sub.CheckoutPath, sub.ID)
		if err != nil {
			s.logSkippedRepositoryRoot(sub, name, err)
			continue
		}
		if migrated {
			if err := s.subscriptions.Update(sub); err != nil {
				if created {
					s.unregisterRepositoryRoot(name, sub.ID)
				}
				s.logSkippedRepositoryRoot(sub, name, fmt.Errorf("persist migrated root: %w", err))
				continue
			}
			if s.logger != nil {
				s.logger.Info("migrated forge checkout to named repository root",
					"subscription_id", sub.ID,
					"repo", sub.Repo,
					"repo_root", name)
			}
		}
	}
	return nil
}

func (s *Service) logSkippedRepositoryRoot(sub ProjectSubscription, name string, err error) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Warn("skipping unavailable persisted repository root",
		"subscription_id", sub.ID,
		"repo", sub.Repo,
		"repo_root", strings.TrimSpace(name),
		"error", err)
}

func (s *Service) legacyRepositoryRootName(sub ProjectSubscription) string {
	name := sanitizeLegacyRootComponent(filepath.Base(strings.TrimSpace(sub.Repo)))
	if name == "" {
		name = "repo"
	}
	name = truncateRootName(name, 64)
	if existing, ok := s.rootResolver.Root(name); !ok || (existing.Kind == paths.RootKindRepository && existing.Owner == sub.ID) {
		return name
	}
	suffix := sanitizeLegacyRootComponent(sub.ID)
	if suffix == "" {
		suffix = "legacy"
	}
	suffix = truncateRootName(suffix, 24)
	for collision := 0; ; collision++ {
		tail := suffix
		if collision > 0 {
			tail = fmt.Sprintf("%s-%d", suffix, collision)
		}
		base := truncateRootName(name, 64-len(tail)-1)
		candidate := strings.Trim(base, "._-") + "-" + tail
		if existing, ok := s.rootResolver.Root(candidate); !ok || (existing.Kind == paths.RootKindRepository && existing.Owner == sub.ID) {
			return candidate
		}
	}
}

func sanitizeLegacyRootComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '.' || r == '_' || r == '-':
			if b.Len() > 0 && !lastDash {
				b.WriteRune(r)
				lastDash = r == '-'
			}
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "._-")
}

func truncateRootName(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	var b strings.Builder
	for _, r := range name {
		if b.Len()+len(string(r)) > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "._-")
}
