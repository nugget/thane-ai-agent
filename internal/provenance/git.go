package provenance

import (
	"bytes"
	"context"
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

// ensureRepo initializes the git repository if it doesn't already
// exist, configures the committer identity, and writes the
// .allowed_signers file.
func (s *Store) ensureRepo() error {
	if err := os.MkdirAll(s.path, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	gitDir := filepath.Join(s.path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := s.git(context.Background(), nil, nil, "init"); err != nil {
			return fmt.Errorf("git init: %w", err)
		}
		s.logger.Info("initialized provenance repository", "path", s.path)
	}

	// Configure committer identity. These are repo-local settings.
	for _, kv := range [][2]string{
		{"user.name", "Thane"},
		{"user.email", "thane@provenance.local"},
	} {
		if err := s.git(context.Background(), nil, nil, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}

	// Write .allowed_signers with the signer's public key.
	allowedSigners := fmt.Sprintf("thane@provenance.local %s\n", s.signer.PublicKey())
	allowedPath := filepath.Join(s.path, ".allowed_signers")
	if err := os.WriteFile(allowedPath, []byte(allowedSigners), 0o644); err != nil {
		return fmt.Errorf("write .allowed_signers: %w", err)
	}

	// Tell git where to find allowed signers for verification.
	if err := s.git(context.Background(), nil, nil,
		"config", "gpg.ssh.allowedSignersFile", allowedPath); err != nil {
		return fmt.Errorf("git config allowedSignersFile: %w", err)
	}

	return nil
}

// commitFile stages a file and creates a signed commit.
func (s *Store) commitFile(filename, message string) error {
	ctx := context.Background()

	// Stage the file.
	if err := s.git(ctx, nil, nil, "add", filename); err != nil {
		return fmt.Errorf("git add %s: %w", filename, err)
	}

	// Check if there are staged changes — skip commit if nothing changed.
	if err := s.git(ctx, nil, nil, "diff", "--cached", "--quiet"); err == nil {
		// Exit code 0 means no differences — nothing to commit.
		s.logger.Debug("no changes to commit", "file", filename)
		return nil
	}

	// Get the tree hash.
	var treeBuf bytes.Buffer
	if err := s.git(ctx, nil, &treeBuf, "write-tree"); err != nil {
		return fmt.Errorf("git write-tree: %w", err)
	}
	tree := strings.TrimSpace(treeBuf.String())

	// Get parent commit (may not exist for first commit).
	var parentBuf bytes.Buffer
	parent := ""
	if err := s.git(ctx, nil, &parentBuf, "rev-parse", "HEAD"); err == nil {
		parent = strings.TrimSpace(parentBuf.String())
	}

	// Build the commit object.
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
		return fmt.Errorf("sign commit: %w", err)
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
		return fmt.Errorf("git hash-object: %w", err)
	}
	commitHash := strings.TrimSpace(hashBuf.String())

	// Update HEAD to point to the new commit.
	if err := s.git(ctx, nil, nil,
		"update-ref", "HEAD", commitHash); err != nil {
		return fmt.Errorf("git update-ref: %w", err)
	}

	// Reset the index to match HEAD so subsequent operations see a
	// clean working tree.
	if err := s.git(ctx, nil, nil, "reset", "--mixed", "HEAD"); err != nil {
		return fmt.Errorf("git reset: %w", err)
	}

	return nil
}

// fileHistory reads git log for a file and returns structured metadata.
func (s *Store) fileHistory(filename string) (*FileHistory, error) {
	ctx := context.Background()

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
		t, _ := time.Parse(time.RFC3339, parts[2])
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
		hist.LastAuthor = edits[0].Message
	}

	return hist, nil
}

// git executes a git command in the store's repository. If stdin is
// non-nil, it is piped to the command. If stdout is non-nil, the
// command's stdout is written there; otherwise it is discarded.
func (s *Store) git(ctx context.Context, stdin *bytes.Reader, stdout *bytes.Buffer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", s.path}, args...)...)

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
