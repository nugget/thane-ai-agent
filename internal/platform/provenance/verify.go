package provenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// VerificationStatus describes whether git history currently vouches for
// the checked path.
type VerificationStatus string

const (
	// VerificationTrusted means the worktree path is clean against HEAD
	// and HEAD carries a trusted SSH signature.
	VerificationTrusted VerificationStatus = "trusted"
	// VerificationFailed means the path could not be tied to trusted
	// signed history.
	VerificationFailed VerificationStatus = "failed"
)

// VerificationResult summarizes one provenance verification check.
type VerificationResult struct {
	Status  VerificationStatus
	Commit  string
	Message string
}

// Trusted reports whether the verification result is safe to consume.
func (r VerificationResult) Trusted() bool {
	return r.Status == VerificationTrusted
}

// Verifier checks whether files in a git-backed store are clean and
// covered by a trusted signed commit.
type Verifier struct {
	mu                 sync.Mutex
	path               string
	allowedSignersPath string
	logger             *slog.Logger
}

// NewVerifier creates a verifier for an existing git repository. Unlike
// [New], it never initializes or mutates the repository.
func NewVerifier(path string, logger *slog.Logger, opts Options) (*Verifier, error) {
	if logger == nil {
		logger = slog.Default()
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: resolve verifier path: %w", err)
	}
	allowedSignersPath := strings.TrimSpace(opts.AllowedSignersPath)
	if allowedSignersPath != "" {
		allowedSignersPath, err = filepath.Abs(allowedSignersPath)
		if err != nil {
			return nil, fmt.Errorf("provenance: resolve verifier allowed signers path: %w", err)
		}
		if _, err := os.Stat(allowedSignersPath); err != nil {
			return nil, fmt.Errorf("provenance: stat verifier allowed signers file: %w", err)
		}
	} else {
		repoLocal := filepath.Join(absPath, ".allowed_signers")
		if _, err := os.Stat(repoLocal); err == nil {
			allowedSignersPath = repoLocal
		} else if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("provenance: repo-local .allowed_signers file is required")
		} else {
			return nil, fmt.Errorf("provenance: stat repo-local .allowed_signers file: %w", err)
		}
	}
	return &Verifier{
		path:               absPath,
		allowedSignersPath: allowedSignersPath,
		logger:             logger,
	}, nil
}

// VerifyFile checks one tracked file. The file must be clean against HEAD,
// present in HEAD, and covered by a trusted signed HEAD commit.
func (v *Verifier) VerifyFile(ctx context.Context, filename string) (VerificationResult, error) {
	if err := validateFilename(filename); err != nil {
		return failedVerification("", err.Error())
	}
	filename = filepath.ToSlash(filepath.Clean(filename))
	return v.verifyPathspec(ctx, filename, true)
}

// VerifyTree checks the repository, or a subtree when pathspec is non-empty.
// The pathspec must be clean against HEAD and HEAD must carry a trusted
// signature.
func (v *Verifier) VerifyTree(ctx context.Context, pathspec string) (VerificationResult, error) {
	pathspec = filepath.ToSlash(filepath.Clean(strings.TrimSpace(pathspec)))
	if pathspec == "." {
		pathspec = ""
	}
	if strings.HasPrefix(pathspec, "../") || pathspec == ".." || filepath.IsAbs(pathspec) {
		return failedVerification("", fmt.Sprintf("invalid verification path %q", pathspec))
	}
	return v.verifyPathspec(ctx, pathspec, false)
}

func (v *Verifier) verifyPathspec(ctx context.Context, pathspec string, requireTracked bool) (VerificationResult, error) {
	if v == nil {
		return failedVerification("", "provenance verifier is not configured")
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	target := pathspec
	if target == "" {
		target = "."
	}
	statusArgs := []string{"status", "--porcelain", "--", target}
	if !requireTracked {
		statusArgs = append(statusArgs, v.statusExclusions(target)...)
	}
	if status, err := v.gitOutput(ctx, false, statusArgs...); err != nil {
		return failedVerification("", fmt.Sprintf("git status failed: %v", err))
	} else if strings.TrimSpace(status) != "" {
		return failedVerification("", "worktree has uncommitted changes for "+target)
	}

	var commit string
	if out, err := v.gitOutput(ctx, false, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return failedVerification("", fmt.Sprintf("git HEAD lookup failed: %v", err))
	} else {
		commit = strings.TrimSpace(out)
	}
	if commit == "" {
		return failedVerification("", "repository has no HEAD commit")
	}

	if requireTracked {
		out, err := v.gitOutput(ctx, false, "ls-tree", "-r", "--name-only", "HEAD", "--", pathspec)
		if err != nil {
			return failedVerification(commit, fmt.Sprintf("git tracked-file lookup failed: %v", err))
		}
		if !pathspecListed(out, pathspec) {
			return failedVerification(commit, "file is not tracked in HEAD: "+pathspec)
		}
	}

	if out, err := v.gitOutput(ctx, true, "verify-commit", commit); err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return failedVerification(commit, "commit signature verification failed: "+msg)
	}

	return VerificationResult{
		Status:  VerificationTrusted,
		Commit:  commit,
		Message: "trusted signed HEAD",
	}, nil
}

func pathspecListed(output, pathspec string) bool {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == pathspec {
			return true
		}
	}
	return false
}

func (v *Verifier) statusExclusions(target string) []string {
	allowedPathspec := v.allowedSignersPathspec()
	if allowedPathspec == "" || !pathspecIncludes(target, allowedPathspec) {
		return nil
	}
	return []string{":(exclude)" + allowedPathspec}
}

func (v *Verifier) allowedSignersPathspec() string {
	if v == nil || v.allowedSignersPath == "" {
		return ""
	}
	rel, err := filepath.Rel(v.path, v.allowedSignersPath)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return ""
	}
	return rel
}

func pathspecIncludes(target, candidate string) bool {
	target = filepath.ToSlash(filepath.Clean(strings.TrimSpace(target)))
	candidate = filepath.ToSlash(filepath.Clean(strings.TrimSpace(candidate)))
	if target == "" || target == "." {
		return candidate != "" && candidate != "."
	}
	return candidate == target || strings.HasPrefix(candidate, target+"/")
}

func failedVerification(commit string, message string) (VerificationResult, error) {
	result := VerificationResult{
		Status:  VerificationFailed,
		Commit:  commit,
		Message: strings.TrimSpace(message),
	}
	return result, errors.New(result.Message)
}

func (v *Verifier) gitOutput(ctx context.Context, verify bool, args ...string) (string, error) {
	cmdArgs := []string{"-C", v.path}
	if verify && v.allowedSignersPath != "" {
		cmdArgs = append(cmdArgs, "-c", "gpg.ssh.allowedSignersFile="+v.allowedSignersPath)
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
		if combined != "" {
			return combined, fmt.Errorf("%w: %s", err, combined)
		}
		return "", err
	}
	combined := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
	return combined, nil
}
