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
	"syscall"
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

	// VerificationUnavailable marks a check that could not be
	// completed — git was killed, timed out, or could not be run at
	// all. It is deliberately not [VerificationFailed]: a check that
	// did not finish says nothing about whether the content is
	// trustworthy, and reporting the two identically sends a reader
	// hunting for a trust problem that may not exist.
	VerificationUnavailable VerificationStatus = "unavailable"
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
	seedSigners        []TrustedSigner
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
		if err := validateAllowedSignersFile(allowedSignersPath); err != nil {
			return nil, err
		}
	} else {
		repoLocal := filepath.Join(absPath, ".allowed_signers")
		if err := validateAllowedSignersFile(repoLocal); err == nil {
			allowedSignersPath = repoLocal
		} else if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("provenance: repo-local .allowed_signers file is required at %s for repository %s", repoLocal, absPath)
		} else {
			return nil, err
		}
	}
	return &Verifier{
		path:               absPath,
		allowedSignersPath: allowedSignersPath,
		seedSigners:        opts.SeedSigners,
		logger:             logger,
	}, nil
}

func validateAllowedSignersFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("provenance: stat allowed signers file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("provenance: allowed signers file %s must be a regular file, not a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("provenance: allowed signers file %s must be a regular file", path)
	}
	return nil
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
		return unavailableVerification("", fmt.Sprintf("git status failed: %v", err))
	} else if strings.TrimSpace(status) != "" {
		return failedVerification("", "worktree has uncommitted changes for "+target)
	}

	var commit string
	if out, err := v.gitOutput(ctx, false, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return unavailableVerification("", fmt.Sprintf("git HEAD lookup failed: %v", err))
	} else {
		commit = strings.TrimSpace(out)
	}
	if commit == "" {
		return failedVerification("", "repository has no HEAD commit")
	}

	if requireTracked {
		out, err := v.gitOutput(ctx, false, "ls-tree", "-r", "--name-only", "HEAD", "--", pathspec)
		if err != nil {
			return unavailableVerification(commit, fmt.Sprintf("git tracked-file lookup failed: %v", err))
		}
		if !pathspecListed(out, pathspec) {
			return failedVerification(commit, "file is not tracked in HEAD: "+pathspec)
		}
	}

	if out, err := v.gitOutput(ctx, true, "verify-commit", commit); err != nil {
		// git verify-commit exits non-zero for a bad or absent signature,
		// which is a verdict, and also when the process is killed or the
		// deadline expires, which is not. Only the first belongs in the
		// seed fallback: running trustedBySeed under a dead context asks
		// a question that cannot be answered and reads the silence as a
		// no. Production reported exactly this as "commit signature
		// verification failed: signal: killed".
		if interrupted, why := executionInterrupted(ctx, err); interrupted {
			return unavailableVerification(commit, "commit signature verification could not complete: "+why)
		}

		// The root's own trust file does not vouch for this commit. Fall back
		// to the seed set, which the root cannot withdraw — see trustedBySeed.
		if !trustedBySeed(ctx, v.path, v.seedSigners, commit) {
			// The fallback itself can be cut short. A seed check that
			// never finished is not a seed check that said no.
			if interrupted, why := executionInterrupted(ctx, nil); interrupted {
				return unavailableVerification(commit, "seed-signer fallback could not complete: "+why)
			}
			msg := strings.TrimSpace(out)
			if msg == "" {
				msg = err.Error()
			}
			return failedVerification(commit, "commit signature verification failed: "+msg)
		}
		logSeedFloorUsed(v.logger, v.path, commit)
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

// unavailableVerification reports that the check could not be run to a
// verdict. The distinction from [failedVerification] is the whole
// point: "we asked git and it said no" and "we could not ask git" lead
// a reader to opposite places, and collapsing them costs a forensic
// detour every time the host is briefly unwell.
func unavailableVerification(commit string, message string) (VerificationResult, error) {
	result := VerificationResult{
		Status:  VerificationUnavailable,
		Commit:  commit,
		Message: strings.TrimSpace(message),
	}
	return result, errors.New(result.Message)
}

func (v *Verifier) gitOutput(ctx context.Context, verify bool, args ...string) (string, error) {
	cmdArgs := []string{"-C", v.path}
	if verify {
		// Pin what decides the signature before naming who may sign it;
		// see signatureTrustArgs for why this cannot be left to config.
		cmdArgs = append(cmdArgs, signatureTrustArgs()...)
		if v.allowedSignersPath != "" {
			cmdArgs = append(cmdArgs, "-c", "gpg.ssh.allowedSignersFile="+v.allowedSignersPath)
		}
	}
	cmdArgs = append(cmdArgs, args...)
	// A context that died while this call waited its turn (the per-root
	// mutex, earlier verifications, the caller's shared budget) must not
	// be reported as a git failure: prod burned an investigation cycle
	// on "git status failed: context deadline exceeded" where git never
	// ran and the repo answered in milliseconds.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("verification skipped, caller context exhausted before git ran: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	// Read-side git must never take the index lock: an opportunistic
	// status refresh can strand .git/index.lock when the deadline kills
	// the process, wedging the root's writer permanently — reproduced,
	// not hypothetical.
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")

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

// executionInterrupted reports whether err (or the context) shows that
// a git invocation was prevented from reaching a verdict, and returns a
// short reason for the message.
//
// The distinction is the whole point of [VerificationUnavailable]. A
// non-zero exit from git is an answer — a bad signature, an untracked
// path — and must stay [VerificationFailed]. A process killed by a
// signal, or a context that expired, produced no answer at all, and
// reporting that as a failed signature sends the reader hunting a
// trust problem that may not exist.
func executionInterrupted(ctx context.Context, err error) (bool, string) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return true, ctxErr.Error()
	}
	if err == nil {
		return false, ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true, err.Error()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A signal means something killed git mid-flight; an ordinary
		// non-zero exit code means git decided.
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return true, exitErr.Error()
		}
	}
	return false, ""
}
