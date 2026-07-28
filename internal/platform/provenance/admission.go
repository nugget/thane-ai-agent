package provenance

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// TrustFileName is the repo-relative path of a root's in-tree trust file —
// the record of which keys the root itself vouches for.
const TrustFileName = ".allowed_signers"

// AdmissionReport records what admission observed, so a caller can log or
// display the attribution rather than only the verdict.
type AdmissionReport struct {
	// RootCommit is the repository's single parentless commit.
	RootCommit string
	// TrustFileCommits are the commits that created or changed the in-tree
	// trust file, newest first.
	TrustFileCommits []string
}

// VerifyAdmission checks a repository's history from first principles: that
// its birth is attributable to a key someone declared entitled to establish
// it, and that every change to its trust file since was made by such a key.
//
// The two rules are one idea. A signature only means something if someone
// decided whose signatures count, and the in-tree trust file cannot answer
// that about itself — a repository that vouches for its own trust surface
// establishes nothing, because whoever wrote the file also chose what it says.
// Seed signers come from configuration, outside the repository they govern, so
// admission is the one question a root cannot answer in its own favor.
//
// Verification runs against a rendered seed-only allowed_signers file rather
// than the repository's own, so a commit that added a key to the in-tree file
// cannot be validated by the very entry it introduced.
//
// Delegation is deliberately strict for now: keys added to the in-tree file
// may sign ordinary content, but only a seed signer may change the file
// itself. A delegated key cannot delegate further. That forbids a legitimate
// pattern — a collaborator admitting the next collaborator — and the cost is
// visible and recoverable when it bites, whereas a permissive chain that
// proves unsound is neither.
func VerifyAdmission(ctx context.Context, repoPath string, seeds []TrustedSigner) (AdmissionReport, error) {
	var report AdmissionReport

	if len(seeds) == 0 {
		return report, fmt.Errorf("no seed signers are declared for %s, so nothing decides whose signatures may establish it; list the entitled keys under roots.<name>.seed_signers", repoPath)
	}

	seedFile, cleanup, err := materializeSeedSigners(seeds)
	if err != nil {
		return report, err
	}
	defer cleanup()

	roots, err := rootCommits(ctx, repoPath)
	if err != nil {
		return report, err
	}
	switch {
	case len(roots) == 0:
		return report, fmt.Errorf("%s has no commit history, so its birth cannot be attributed", repoPath)
	case len(roots) > 1:
		// Independent histories joined by a merge each carry their own
		// birth. Admitting the repository because one of them checks out
		// would let an unattributable history in through the side door.
		return report, fmt.Errorf("%s has %d parentless commits (%s), so it carries grafted history with more than one birth; a root must descend from a single admitted commit",
			repoPath, len(roots), strings.Join(roots, ", "))
	}
	report.RootCommit = roots[0]

	if err := verifyAgainstSeeds(ctx, repoPath, seedFile, report.RootCommit); err != nil {
		return report, fmt.Errorf("the root commit %s of %s is not signed by a declared seed signer, so this root's birth is unattributed%s",
			shortCommit(report.RootCommit), repoPath, attributionHint(ctx, repoPath, report.RootCommit))
	}

	report.TrustFileCommits, err = trustFileCommits(ctx, repoPath)
	if err != nil {
		return report, err
	}
	for _, commit := range report.TrustFileCommits {
		if err := verifyAgainstSeeds(ctx, repoPath, seedFile, commit); err != nil {
			return report, fmt.Errorf("commit %s of %s changed %s without a declared seed signer's signature, so the root's trust surface was widened by someone not entitled to widen it%s",
				shortCommit(commit), repoPath, TrustFileName, attributionHint(ctx, repoPath, commit))
		}
	}

	return report, nil
}

// attributionHint appends who git says actually signed a commit, and what to
// do about it. It is diagnostic only and never affects the verdict, which is
// decided against the seed file alone.
//
// The overwhelmingly common refusal is a root the agent founded for an
// instance that did not declare the agent entitled to found it. Without the
// principal in the message that reads as a mysterious signature failure; with
// it, the fix is one config line and the operator can see whether granting it
// is what they want.
func attributionHint(ctx context.Context, repoPath, commit string) string {
	// Ask against the root's own trust file: it is the only place likely to
	// name the signer, and naming is all this is for.
	inTree := filepath.Join(repoPath, TrustFileName)
	if err := validateAllowedSignersFile(inTree); err != nil {
		inTree = ""
	}
	signer, err := signerFor(ctx, repoPath, inTree, commit)
	if err != nil {
		return ""
	}
	switch {
	case signer.Kind == SignerKindAgent:
		return fmt.Sprintf(": it was signed by %s, the agent's own key — declare that principal in this root's seed_signers if the agent is entitled to establish it, or re-establish the root with a commit signed by a declared seed",
			signer.Principal)
	case signer.Principal != "":
		return fmt.Sprintf(": it was signed by %s, which this root does not declare as a seed signer", signer.Principal)
	case signer.Reason != "":
		return ": " + signer.Reason
	default:
		return ""
	}
}

// trustedBySeed reports whether a commit is signed by one of the root's
// declared seed signers.
//
// This is the floor: seed signers are entitled to sign a root permanently, and
// nothing inside the repository can withdraw that. Without it the guarantee the
// design leads with — that a seed key can always re-assert control over a root
// whose .allowed_signers has been polluted — is not true, because verification
// reads only the in-tree file and an edit to that file could remove the very
// key needed to repair it.
//
// It is consulted only after in-tree verification has already failed, so the
// ordinary path costs nothing and the fallback runs on a signature the root
// itself does not vouch for. A missing or unreadable seed set is not an error
// here: it simply means there is no floor to stand on, and the in-tree failure
// stands as the answer.
func trustedBySeed(ctx context.Context, repoPath string, seeds []TrustedSigner, commit string) bool {
	if len(seeds) == 0 || strings.TrimSpace(commit) == "" {
		return false
	}
	seedFile, cleanup, err := materializeSeedSigners(seeds)
	if err != nil {
		return false
	}
	defer cleanup()
	_, err = runGitTextVerify(ctx, repoPath, seedFile, "verify-commit", commit)
	return err == nil
}

// logSeedFloorUsed records that a commit was trusted by the seed floor rather
// than by the root's own trust file.
//
// The floor is a recovery mechanism, not a normal path: reaching it means the
// in-tree .allowed_signers has stopped vouching for history it previously
// covered. Succeeding silently would hide exactly the condition the floor
// exists to survive, and would defeat the boot-time round-trip that
// [Store.VerifyHead] performs — whose whole purpose is to surface a malformed
// or polluted trust file early rather than let it emerge later as a puzzling
// read failure.
//
// Both repairs are legitimate, so the message names both rather than assuming
// which one the operator meant.
func logSeedFloorUsed(logger *slog.Logger, repoPath, commit string) {
	if logger == nil {
		return
	}
	logger.Warn("commit trusted by the seed floor; this root's own .allowed_signers no longer vouches for it",
		"repo", repoPath,
		"commit", shortCommit(commit),
		"remedy", "restore the seed signer to .allowed_signers, or remove it from seed_signers if dropping it was intended",
	)
}

// rootCommits returns every parentless commit reachable from HEAD.
//
// The three ways this can fail are deliberately not collapsed. A repository
// with no commits yet is a finding the caller states in those words; a
// directory git cannot read as a work tree is an operational fault; and a
// rev-list that fails on a readable repository is neither. Reporting the
// second as "no commit history" would describe a permission or ownership
// problem as an empty history and send an operator to re-establish a root
// whose history is perfectly intact — the failure mode is not hypothetical,
// since a repository owned by another user makes git refuse every command
// with an error that has nothing to do with commits.
// rootCommits lists the repository's parentless commits.
//
// The trailing "--" is load-bearing. A document root is a directory an agent
// writes files into, and a file named HEAD there makes the revision ambiguous
// with a path — git then refuses with usage advice rather than an answer, and
// admission reports it as though the history were unreadable. The separator
// says everything before it is a revision, which is the same guard the read
// surface already applies to every revision it passes.
func rootCommits(ctx context.Context, repoPath string) ([]string, error) {
	out, err := runGitText(ctx, repoPath, "rev-list", "--max-parents=0", "--end-of-options", "HEAD", "--")
	if err == nil {
		return nonEmptyLines(out), nil
	}

	inside, repoErr := runGitText(ctx, repoPath, "rev-parse", "--is-inside-work-tree")
	switch {
	case repoErr != nil:
		return nil, fmt.Errorf("read git repository at %s: %w", repoPath, repoErr)
	case strings.TrimSpace(inside) != "true":
		return nil, fmt.Errorf("%s is not a git work tree, so it has no history to admit", repoPath)
	}
	if _, headErr := runGitText(ctx, repoPath, "rev-parse", "--verify", "--end-of-options", "HEAD"); headErr != nil {
		return nil, nil
	}
	return nil, fmt.Errorf("list root commits of %s: %w", repoPath, err)
}

// trustFileCommits returns every commit that created or changed the in-tree
// trust file.
//
// --full-history is load-bearing: git's default history simplification drops
// commits whose change to a path did not survive into the merge result, which
// is exactly where an unentitled edit would hide.
func trustFileCommits(ctx context.Context, repoPath string) ([]string, error) {
	out, err := runGitText(ctx, repoPath, "log", "--full-history", "--format=%H", "--", TrustFileName)
	if err != nil {
		return nil, fmt.Errorf("list %s history of %s: %w", TrustFileName, repoPath, err)
	}
	return nonEmptyLines(out), nil
}

// verifyAgainstSeeds checks one commit's signature against the seed set alone.
func verifyAgainstSeeds(ctx context.Context, repoPath, seedFile, commit string) error {
	if _, err := runGitTextVerify(ctx, repoPath, seedFile, "verify-commit", commit); err != nil {
		return err
	}
	return nil
}

// materializeSeedSigners renders the seed set to a temporary allowed_signers
// file for git to verify against, and returns a cleanup func.
//
// It is deliberately not written inside the repository. A trust surface stored
// where the repository can reach it is one more thing a write to that
// repository could change, and admission exists precisely to be the check no
// repository content can influence.
func materializeSeedSigners(seeds []TrustedSigner) (string, func(), error) {
	content, err := RenderSeedSigners(seeds)
	if err != nil {
		return "", nil, fmt.Errorf("render seed signers: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return "", nil, fmt.Errorf("seed signers rendered to an empty trust set")
	}

	file, err := os.CreateTemp("", "thane-seed-signers-*")
	if err != nil {
		return "", nil, fmt.Errorf("create seed signers file: %w", err)
	}
	name := file.Name()
	cleanup := func() { _ = os.Remove(name) }

	if err := file.Chmod(0o600); err != nil {
		file.Close()
		cleanup()
		return "", nil, fmt.Errorf("secure seed signers file: %w", err)
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write seed signers file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close seed signers file: %w", err)
	}
	return name, cleanup, nil
}

// seedsInclude reports whether publicKey is one of the declared seed signers,
// comparing canonical key blobs so a trailing comment or stray whitespace
// cannot make an entitled key look absent.
func seedsInclude(seeds []TrustedSigner, publicKey string) (bool, error) {
	target, err := canonicalKeyBlob(publicKey)
	if err != nil {
		return false, fmt.Errorf("agent signing key: %w", err)
	}
	for _, seed := range seeds {
		blob, err := canonicalKeyBlob(seed.PublicKey)
		if err != nil {
			return false, fmt.Errorf("seed signer %q: %w", strings.TrimSpace(seed.Principal), err)
		}
		if blob == target {
			return true, nil
		}
	}
	return false, nil
}

func nonEmptyLines(out string) []string {
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// shortCommit abbreviates a hash for operator-facing messages, where the full
// forty characters crowd out the sentence around them.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
