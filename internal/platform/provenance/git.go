package provenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// recentEditsCap is the maximum number of recent edits returned by
// [Store.History].
const recentEditsCap = 10

// gitTimeout is the default timeout for git operations that don't
// receive a caller-provided context (e.g., repository initialization).
const gitTimeout = 30 * time.Second

// defaultBirthGitignore is written by [Store.BootstrapBirthCommit] when
// the repository has no .gitignore yet. Kept minimal — broader patterns
// belong to the operator's own .gitignore.
const defaultBirthGitignore = `.DS_Store
*~
.tmp/
`

// BootstrapBirthCommit creates an initial signed commit on an empty
// repository so verification has a baseline to verify against. No-op
// when HEAD already exists. Stages only the bootstrap files
// (.gitignore and the repo-local .allowed_signers if present);
// existing user content stays untracked and must be added explicitly.
func (s *Store) BootstrapBirthCommit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.git(ctx, nil, nil, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		return nil
	}

	// The commit about to be created is this root's birth, and it will carry
	// the agent's signature. If the declared seed set does not include the
	// agent key, the root is inadmissible from its very first commit — a
	// state no later commit can repair, because the birth is the thing being
	// judged. Refuse while the repository is still empty and the remedy is a
	// config line rather than a history rewrite.
	if len(s.seedSigners) > 0 {
		admits, err := seedsInclude(s.seedSigners, s.signer.PublicKey())
		if err != nil {
			return fmt.Errorf("birth commit: %w", err)
		}
		if !admits {
			return fmt.Errorf("birth commit: this root's seed signers do not include the agent key, so the commit it is about to sign could never be admitted; declare %s with the agent's public key in this root's seed_signers, or establish the root by hand with a commit signed by a declared seed", AgentPrincipal)
		}
	}

	gitignorePath := filepath.Join(s.path, ".gitignore")
	if _, err := os.Stat(gitignorePath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat .gitignore: %w", err)
		}
		if err := os.WriteFile(gitignorePath, []byte(defaultBirthGitignore), 0o644); err != nil {
			return fmt.Errorf("write default .gitignore: %w", err)
		}
	}

	bootstrapFiles := []string{".gitignore"}
	if _, err := os.Stat(filepath.Join(s.path, ".allowed_signers")); err == nil {
		bootstrapFiles = append(bootstrapFiles, ".allowed_signers")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat .allowed_signers: %w", err)
	}

	commitHash, err := s.commitFiles(ctx, bootstrapFiles, "bootstrap document root")
	if err != nil {
		return fmt.Errorf("birth commit: %w", err)
	}
	if commitHash != "" {
		s.logger.Info("created document root birth commit",
			"path", s.path,
			"files", bootstrapFiles,
		)
	}
	return nil
}

// ensureRepo initializes the git repository if it doesn't already
// exist, configures the committer identity, and writes the
// .allowed_signers file.
func (s *Store) ensureRepo() error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	if err := os.MkdirAll(s.path, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	gitDir := filepath.Join(s.path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := s.git(ctx, nil, nil, "init"); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
		s.logger.Info("initialized provenance repository", "path", s.path)
	}

	// Configure committer identity. These are repo-local settings.
	for _, kv := range [][2]string{
		{"user.name", "Thane"},
		{"user.email", "thane@provenance.local"},
	} {
		if err := s.git(ctx, nil, nil, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}

	allowedPath := s.allowedSignersPath
	if allowedPath == "" {
		allowedPath = filepath.Join(s.path, ".allowed_signers")
		if err := validateAllowedSignersFile(allowedPath); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			// Establish this repository's trust surface: the agent key
			// plus the seed signers entitled to found this root. It is
			// written once, here, and never rewritten from config —
			// afterwards the file is the root's own record, extended by
			// commits signed with keys it already trusts.
			allowedSigners, err := RenderAllowedSigners(s.signer.PublicKey(), s.seedSigners)
			if err != nil {
				return fmt.Errorf("render seed allowed_signers: %w", err)
			}
			// Rename-based, and re-validated after: a plain write leaves a
			// window between the not-exist check above and the write in
			// which a symlink can be dropped in, redirecting the file that
			// decides which signatures count to somewhere outside the repo.
			if err := atomicWriteFile(allowedPath, []byte(allowedSigners), 0o644); err != nil {
				return fmt.Errorf("write .allowed_signers: %w", err)
			}
			if err := validateAllowedSignersFile(allowedPath); err != nil {
				return fmt.Errorf("seed .allowed_signers: %w", err)
			}
		}
	} else if err := validateAllowedSignersFile(allowedPath); err != nil {
		return err
	}

	// Tell git where to find allowed signers for verification.
	if err := s.git(ctx, nil, nil,
		"config", "gpg.ssh.allowedSignersFile", allowedPath); err != nil {
		return fmt.Errorf("git config allowedSignersFile: %w", err)
	}

	return nil
}

// commitFile stages a file and creates a signed commit. It returns the exact
// commit hash, or an empty string when the file already has the requested
// content.
func (s *Store) commitFile(ctx context.Context, filename, message string) (string, error) {
	return s.commitFiles(ctx, []string{filename}, message)
}

func (s *Store) fileTracked(ctx context.Context, filename string) (bool, error) {
	err := s.git(ctx, nil, nil, "--literal-pathspecs", "ls-files", "--error-unmatch", "--", filename)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// commitFiles stages files and creates one signed commit containing all
// staged changes. It returns the exact commit hash, or an empty string when no
// commit was needed. Once HEAD advances, later index cleanup is best-effort so
// callers never receive a failure for a mutation that was actually committed.
func (s *Store) commitFiles(ctx context.Context, filenames []string, message string) (string, error) {
	if len(filenames) == 0 {
		return "", fmt.Errorf("no files to commit")
	}

	// Stage additions, modifications, and removals for the pathspecs.
	args := append([]string{"add", "-A", "--"}, filenames...)
	if err := s.git(ctx, nil, nil, args...); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	// Check if there are staged changes — skip commit if nothing changed.
	// git diff --cached --quiet exits 1 when there are differences, 0
	// when clean, and >1 on real errors (corruption, bad args, etc.).
	diffErr := s.git(ctx, nil, nil, "diff", "--cached", "--quiet")
	if diffErr == nil {
		// Exit code 0 means no differences — nothing to commit.
		s.logger.Debug("no changes to commit", "files", filenames)
		return "", nil
	}
	var exitErr *exec.ExitError
	if errors.As(diffErr, &exitErr) && exitErr.ExitCode() != 1 {
		return "", fmt.Errorf("git diff --cached: %w", diffErr)
	}

	// Get the tree hash.
	var treeBuf bytes.Buffer
	if err := s.git(ctx, nil, &treeBuf, "write-tree"); err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	tree := strings.TrimSpace(treeBuf.String())

	// Get parent commit (may not exist for first commit).
	var parentBuf bytes.Buffer
	parent := ""
	if err := s.git(ctx, nil, &parentBuf, "rev-parse", "HEAD"); err == nil {
		parent = strings.TrimSpace(parentBuf.String())
	}

	commitHash, err := s.writeSignedCommitObject(ctx, tree, parent, message)
	if err != nil {
		return "", err
	}

	// Update HEAD only if it still names the parent used above. The Store mutex
	// coordinates Thane writers; update-ref's old-value guard also catches a
	// concurrent operator git operation or transport fast-forward.
	expectedHead := parent
	if expectedHead == "" {
		expectedHead = strings.Repeat("0", len(commitHash))
	}
	if err := s.git(ctx, nil, nil,
		"update-ref", "HEAD", commitHash, expectedHead); err != nil {
		return "", fmt.Errorf("git update-ref: %w", err)
	}

	// Reset the index to match HEAD so subsequent operations see a
	// clean index. HEAD already contains the mutation, so context cancellation
	// or an index-cleanup failure must not turn that success into an apparent
	// failure that a caller may retry.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitTimeout)
	defer cancel()
	if err := s.git(cleanupCtx, nil, nil, "reset", "--mixed", "HEAD"); err != nil {
		s.logger.Warn("provenance commit succeeded but index cleanup failed",
			"commit", shorten(commitHash),
			"files", filenames,
			"error", err,
		)
	}

	return commitHash, nil
}

// writeSignedCommitObject creates a signed commit object without moving a ref.
// Callers choose and guard the parent when they publish the returned object.
func (s *Store) writeSignedCommitObject(ctx context.Context, tree, parent, message string) (string, error) {
	now := time.Now()
	unixTime := now.Unix()
	_, offset := now.Zone()
	tzSign := "+"
	if offset < 0 {
		tzSign = "-"
		offset = -offset
	}
	tz := fmt.Sprintf("%s%02d%02d", tzSign, offset/3600, (offset%3600)/60)
	timestamp := fmt.Sprintf("%d %s", unixTime, tz)

	identity := "Thane <thane@provenance.local>"

	var commitObj strings.Builder
	fmt.Fprintf(&commitObj, "tree %s\n", tree)
	if parent != "" {
		fmt.Fprintf(&commitObj, "parent %s\n", parent)
	}
	fmt.Fprintf(&commitObj, "author %s %s\n", identity, timestamp)
	fmt.Fprintf(&commitObj, "committer %s %s\n", identity, timestamp)

	// Sign the commit content (without the gpgsig header — git signs
	// the commit object as it would appear without the signature).
	commitForSigning := commitObj.String() + "\n" + message + "\n"
	armoredSig, err := s.signer.Sign([]byte(commitForSigning))
	if err != nil {
		return "", fmt.Errorf("sign commit: %w", err)
	}

	// Insert the gpgsig header between the last header line and the
	// blank line before the message. Each continuation line of the
	// signature is indented with a single space.
	sigLines := strings.Split(string(armoredSig), "\n")
	fmt.Fprintf(&commitObj, "gpgsig %s\n", sigLines[0])
	for _, line := range sigLines[1:] {
		fmt.Fprintf(&commitObj, " %s\n", line)
	}
	fmt.Fprintf(&commitObj, "\n%s\n", message)

	// Write the signed commit object.
	var hashBuf bytes.Buffer
	commitBytes := []byte(commitObj.String())
	if err := s.git(ctx, bytes.NewReader(commitBytes), &hashBuf,
		"hash-object", "-t", "commit", "-w", "--stdin"); err != nil {
		return "", fmt.Errorf("git hash-object: %w", err)
	}
	return strings.TrimSpace(hashBuf.String()), nil
}

// fileHistory reads git log for a file and returns structured metadata.
func (s *Store) fileHistory(ctx context.Context, filename string) (*FileHistory, error) {

	// Check if the file has any commits.
	var countBuf bytes.Buffer
	if err := s.git(ctx, nil, &countBuf,
		"rev-list", "--count", "HEAD", "--", filename); err != nil {
		// No commits yet (empty repo or file not tracked).
		return &FileHistory{}, nil
	}
	count, err := strconv.Atoi(strings.TrimSpace(countBuf.String()))
	if err != nil || count == 0 {
		return &FileHistory{}, nil
	}

	// Get recent commits.
	limit := min(count, recentEditsCap)
	format := "%H%x00%s%x00%aI"
	var logBuf bytes.Buffer
	if err := s.git(ctx, nil, &logBuf,
		"log", fmt.Sprintf("--format=%s", format),
		fmt.Sprintf("-n%d", limit),
		"HEAD", "--", filename); err != nil {
		return &FileHistory{RevisionCount: count}, nil
	}

	var edits []EditEntry
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		t, err := time.Parse(time.RFC3339, parts[2])
		if err != nil {
			s.logger.Warn("skipping commit with unparseable timestamp",
				"hash", parts[0], "raw", parts[2], "error", err)
			continue
		}
		edits = append(edits, EditEntry{
			Hash:      parts[0],
			Message:   parts[1],
			Timestamp: t,
		})
	}

	hist := &FileHistory{
		RevisionCount: count,
		RecentEdits:   edits,
	}

	if len(edits) > 0 {
		hist.LastModified = edits[0].Timestamp
		hist.LastMessage = edits[0].Message
	}

	return hist, nil
}

// git executes a git command in the store's repository. If stdin is
// non-nil, it is piped to the command. If stdout is non-nil, the
// command's stdout is written there; otherwise it is discarded.
func (s *Store) git(ctx context.Context, stdin *bytes.Reader, stdout *bytes.Buffer, args ...string) error {
	return s.gitWithEnv(ctx, nil, stdin, stdout, args...)
}

// gitWithEnv is git with an optional extra environment appended to the
// process environment — the seam used to inject GIT_SSH_COMMAND for network
// transport. A nil (or empty) env inherits the process environment
// unchanged, so gitWithEnv(ctx, nil, …) is byte-for-byte the local git path.
func (s *Store) gitWithEnv(ctx context.Context, env []string, stdin *bytes.Reader, stdout *bytes.Buffer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", s.path}, args...)...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	if stdin != nil {
		cmd.Stdin = stdin
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if stdout != nil {
		cmd.Stdout = stdout
	}

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%w: %s", err, errMsg)
		}
		return err
	}

	return nil
}

// HeadCommit reports the full commit hash at repoPath's HEAD. It returns an
// error when the path is not a git repository or carries no commits yet, so a
// caller recording HEAD as context can distinguish "no history" from a hash.
func HeadCommit(ctx context.Context, repoPath string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gitTimeout)
		defer cancel()
	}
	out, err := runGitText(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD of %s: %w", repoPath, err)
	}
	head := strings.TrimSpace(out)
	if head == "" {
		return "", fmt.Errorf("resolve HEAD of %s: no commits", repoPath)
	}
	return head, nil
}
