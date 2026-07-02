package checkout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMirrorSyncTimeout bounds one mirror fetch/reset pass.
const DefaultMirrorSyncTimeout = 2 * time.Minute

const mirrorOwnedConfigKey = "checkout.mirror"

// MirrorSpec describes a read-only mirror checkout.
type MirrorSpec struct {
	// Name is a caller-facing identifier used in logs and errors.
	Name string
	// WorktreePath is the local path maintained as a mirror.
	WorktreePath string
	// Logger receives setup and sync logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// Mirror is a local checkout maintained as an exact read-only copy of a remote
// branch. It never records the remote URL in .git/config; every sync supplies
// transport details for that command only.
type Mirror struct {
	Name         string
	WorktreePath string

	logger *slog.Logger
}

// MirrorSyncRequest parameterizes one mirror sync pass.
type MirrorSyncRequest struct {
	// RemoteURL is the git remote (ssh, https, or a local path).
	RemoteURL string
	// Branch is the remote branch mirrored into the local worktree.
	Branch string
	// SSHCommand, when non-empty, is exported as GIT_SSH_COMMAND for fetch.
	SSHCommand string
}

// MirrorSyncResult reports what one mirror sync pass observed and applied.
type MirrorSyncResult struct {
	// PreviousHead is the local HEAD before the mirror was reset. It is empty
	// on the first sync into a fresh checkout.
	PreviousHead string
	// RemoteHead is the fetched remote branch head that the mirror now matches.
	RemoteHead string
	// Changed is true when the mirror's HEAD moved during the sync.
	Changed bool
}

// OpenMirror resolves a mirror checkout path. The repository is created lazily
// by [Mirror.Sync] so callers can construct mirrors during startup without
// touching disk until the first sync pass.
func OpenMirror(spec MirrorSpec) (*Mirror, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "checkout"
	}
	worktreePath := strings.TrimSpace(spec.WorktreePath)
	if worktreePath == "" {
		return nil, fmt.Errorf("%s: worktree path is required", name)
	}
	absWorktreePath, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("%s: resolve worktree path: %w", name, err)
	}
	logger := spec.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Mirror{Name: name, WorktreePath: absWorktreePath, logger: logger}, nil
}

// Sync fetches the requested branch and resets the mirror worktree to match it
// exactly. Local modifications and untracked files are discarded by design.
func (m *Mirror) Sync(ctx context.Context, req MirrorSyncRequest) (MirrorSyncResult, error) {
	if m == nil {
		return MirrorSyncResult{}, fmt.Errorf("mirror checkout is not configured")
	}
	ctx, cancel := withMirrorTimeout(ctx)
	defer cancel()

	branch, err := validateMirrorSyncRequest(ctx, req)
	if err != nil {
		return MirrorSyncResult{}, fmt.Errorf("%s: %w", m.Name, err)
	}
	if err := m.ensureRepo(ctx); err != nil {
		return MirrorSyncResult{}, err
	}

	previousHead, err := m.optionalRevParse(ctx, "HEAD")
	if err != nil {
		return MirrorSyncResult{}, err
	}
	if err := m.fetch(ctx, req, branch); err != nil {
		return MirrorSyncResult{}, err
	}
	if err := m.removeFetchHead(ctx); err != nil {
		return MirrorSyncResult{}, err
	}
	remoteRef := "refs/remotes/origin/" + branch
	remoteHead, err := m.revParse(ctx, remoteRef)
	if err != nil {
		return MirrorSyncResult{}, err
	}

	localRef := "refs/heads/" + branch
	if err := m.git(ctx, nil, nil, nil, "update-ref", localRef, remoteHead); err != nil {
		return MirrorSyncResult{}, fmt.Errorf("%s: update mirror branch %q: %w", m.Name, branch, err)
	}
	if err := m.git(ctx, nil, nil, nil, "symbolic-ref", "HEAD", localRef); err != nil {
		return MirrorSyncResult{}, fmt.Errorf("%s: set mirror HEAD to %q: %w", m.Name, branch, err)
	}
	if err := m.git(ctx, nil, nil, nil, "reset", "--hard", "--end-of-options", remoteHead); err != nil {
		return MirrorSyncResult{}, fmt.Errorf("%s: reset mirror to %s: %w", m.Name, shortenForLog(remoteHead), err)
	}
	if err := m.git(ctx, nil, nil, nil, "clean", "-fdx"); err != nil {
		return MirrorSyncResult{}, fmt.Errorf("%s: clean mirror worktree: %w", m.Name, err)
	}
	if err := m.removeOriginConfig(ctx); err != nil {
		return MirrorSyncResult{}, err
	}

	res := MirrorSyncResult{
		PreviousHead: previousHead,
		RemoteHead:   remoteHead,
		Changed:      previousHead != remoteHead,
	}
	m.logger.Info("mirror checkout synced",
		"name", m.Name,
		"path", m.WorktreePath,
		"branch", branch,
		"remote_head", shortenForLog(remoteHead),
		"changed", res.Changed,
	)
	return res, nil
}

func validateMirrorSyncRequest(ctx context.Context, req MirrorSyncRequest) (string, error) {
	remoteURL := strings.TrimSpace(req.RemoteURL)
	if err := checkMirrorRemoteArg("remote url", remoteURL); err != nil {
		return "", err
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		return "", fmt.Errorf("branch is empty")
	}
	if strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("branch %q must not begin with '-'", branch)
	}
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("invalid branch name %q: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return branch, nil
}

func checkMirrorRemoteArg(kind, url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("%s is empty", kind)
	}
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("%s %q must not begin with '-'", kind, url)
	}
	return nil
}

func (m *Mirror) ensureRepo(ctx context.Context) error {
	info, err := os.Stat(m.WorktreePath)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%s: mirror path %s is not a directory", m.Name, m.WorktreePath)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(m.WorktreePath, 0o755); err != nil {
			return fmt.Errorf("%s: create mirror directory: %w", m.Name, err)
		}
	default:
		return fmt.Errorf("%s: stat mirror path: %w", m.Name, err)
	}

	if ok, err := m.isGitWorktree(ctx); err != nil {
		return err
	} else if ok {
		return m.requireMirrorOwned(ctx)
	}
	empty, err := isEmptyDir(m.WorktreePath)
	if err != nil {
		return fmt.Errorf("%s: inspect mirror directory: %w", m.Name, err)
	}
	if !empty {
		return fmt.Errorf("%s: mirror path %s exists but is not an empty directory or git checkout", m.Name, m.WorktreePath)
	}
	if err := m.git(ctx, nil, nil, nil, "init"); err != nil {
		return fmt.Errorf("%s: initialize mirror repository: %w", m.Name, err)
	}
	if err := m.markMirrorOwned(ctx); err != nil {
		return err
	}
	return nil
}

func (m *Mirror) markMirrorOwned(ctx context.Context) error {
	if err := m.git(ctx, nil, nil, nil, "config", mirrorOwnedConfigKey, "true"); err != nil {
		return fmt.Errorf("%s: mark mirror repository owned: %w", m.Name, err)
	}
	return nil
}

func (m *Mirror) requireMirrorOwned(ctx context.Context) error {
	var out bytes.Buffer
	err := m.git(ctx, nil, nil, &out, "config", "--bool", "--get", mirrorOwnedConfigKey)
	if err != nil {
		if isGitExit(err, 1) {
			return fmt.Errorf("%s: mirror path %s is an existing git checkout but is not marked as mirror-owned; refusing destructive reset/clean", m.Name, m.WorktreePath)
		}
		return fmt.Errorf("%s: inspect mirror ownership marker: %w", m.Name, err)
	}
	if strings.TrimSpace(out.String()) != "true" {
		return fmt.Errorf("%s: mirror path %s has %s=%q, want true; refusing destructive reset/clean", m.Name, m.WorktreePath, mirrorOwnedConfigKey, strings.TrimSpace(out.String()))
	}
	return nil
}

func (m *Mirror) isGitWorktree(ctx context.Context) (bool, error) {
	var out bytes.Buffer
	err := m.git(ctx, nil, nil, &out, "rev-parse", "--is-inside-work-tree")
	if err == nil {
		return strings.TrimSpace(out.String()) == "true", nil
	}
	if isGitExit(err, 128) {
		return false, nil
	}
	return false, fmt.Errorf("%s: inspect git worktree: %w", m.Name, err)
}

func (m *Mirror) fetch(ctx context.Context, req MirrorSyncRequest, branch string) error {
	var env []string
	if req.SSHCommand != "" {
		env = []string{"GIT_SSH_COMMAND=" + req.SSHCommand}
	}
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	if err := m.git(ctx, env, nil, nil, "fetch", "--no-tags", "--prune", "--no-write-fetch-head", "--end-of-options", strings.TrimSpace(req.RemoteURL), refspec); err != nil {
		return fmt.Errorf("%s: fetch mirror branch %q: %w", m.Name, branch, err)
	}
	return nil
}

func (m *Mirror) optionalRevParse(ctx context.Context, rev string) (string, error) {
	sha, err := m.revParse(ctx, rev)
	if err == nil {
		return sha, nil
	}
	if isGitExit(err, 128) {
		return "", nil
	}
	return "", err
}

func (m *Mirror) revParse(ctx context.Context, rev string) (string, error) {
	var out bytes.Buffer
	if err := m.git(ctx, nil, nil, &out, "rev-parse", "--verify", "--end-of-options", rev); err != nil {
		return "", fmt.Errorf("%s: resolve %s: %w", m.Name, rev, err)
	}
	sha := strings.TrimSpace(out.String())
	if sha == "" {
		return "", fmt.Errorf("%s: rev-parse %s returned nothing", m.Name, rev)
	}
	return sha, nil
}

func (m *Mirror) removeOriginConfig(ctx context.Context) error {
	if err := m.git(ctx, nil, nil, nil, "config", "--get-regexp", "^remote\\.origin\\."); err != nil {
		if isGitExit(err, 1) {
			return nil
		}
		return fmt.Errorf("%s: inspect origin config: %w", m.Name, err)
	}
	if err := m.git(ctx, nil, nil, nil, "config", "--remove-section", "remote.origin"); err != nil {
		return fmt.Errorf("%s: remove origin config: %w", m.Name, err)
	}
	return nil
}

func (m *Mirror) removeFetchHead(ctx context.Context) error {
	fetchHead, err := m.gitPath(ctx, "FETCH_HEAD")
	if err != nil {
		return err
	}
	if err := os.Remove(fetchHead); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: remove FETCH_HEAD transport trace: %w", m.Name, err)
	}
	return nil
}

func (m *Mirror) gitPath(ctx context.Context, name string) (string, error) {
	var out bytes.Buffer
	if err := m.git(ctx, nil, nil, &out, "rev-parse", "--git-path", name); err != nil {
		return "", fmt.Errorf("%s: resolve git path %s: %w", m.Name, name, err)
	}
	p := strings.TrimSpace(out.String())
	if p == "" {
		return "", fmt.Errorf("%s: git path %s resolved to empty path", m.Name, name)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(m.WorktreePath, p)
	}
	return p, nil
}

func (m *Mirror) git(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", m.WorktreePath}, args...)...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &gitCommandError{
			args:   sanitizeGitArgs(args),
			err:    err,
			stderr: strings.TrimSpace(stderr.String()),
		}
	}
	return nil
}

type gitCommandError struct {
	args   []string
	err    error
	stderr string
}

func (e *gitCommandError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, e.stderr)
}

func (e *gitCommandError) Unwrap() error {
	return e.err
}

func isGitExit(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

func sanitizeGitArgs(args []string) []string {
	out := append([]string(nil), args...)
	if len(out) > 0 && out[0] == "fetch" {
		for i, arg := range out {
			if arg == "--end-of-options" && i+1 < len(out) {
				out[i+1] = "<remote>"
				break
			}
		}
	}
	return out
}

func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func withMirrorTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultMirrorSyncTimeout)
}

func shortenForLog(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}
