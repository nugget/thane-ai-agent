// Package coreintegrity checks whether an instance's core directory is
// in the state the runtime requires: present, version-controlled, with
// its config committed, free of tracked private key material, and with
// no uncommitted changes to tracked files.
//
// Signature verification resolves signers from core's own
// .allowed_signers. That is sufficient while core has no remote: an
// attacker who can rewrite the signer list already has local write
// access and does not need to forge anything. It stops being sufficient
// the moment core syncs from somewhere, because then someone who can
// push could rewrite the config and the list of who may sign it in one
// commit — so an out-of-tree anchor is a prerequisite for giving core a
// remote, not for this check.
//
// The checks live here rather than inside the boot path so one
// definition serves both `thane validate`, which reports, and the boot
// gate, which refuses. A diagnostic that drifts from the gate it
// describes is worse than no diagnostic — it sends an operator looking
// in the wrong place while the real failure stays invisible.
package coreintegrity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
)

// Status is the outcome of one check.
type Status string

const (
	// StatusPass means the check succeeded.
	StatusPass Status = "pass"
	// StatusFail means the check ran and the requirement is not met.
	StatusFail Status = "fail"
	// StatusSkipped means a prerequisite failed, so this check could not
	// run. Skipped is not failure: reporting every downstream
	// consequence of one broken prerequisite buries the cause under its
	// own symptoms.
	StatusSkipped Status = "skipped"
)

// Check is one integrity requirement and what it found.
type Check struct {
	// Name identifies the requirement in stable, greppable form.
	Name string `json:"name"`
	// Status is the outcome.
	Status Status `json:"status"`
	// Detail says what was observed, in operator terms.
	Detail string `json:"detail"`
	// Fix is the command or action that resolves a failure. Empty on
	// pass, and on skip, where the prerequisite's own fix is the one
	// that matters.
	Fix string `json:"fix,omitempty"`
}

// Report is the full integrity picture for one instance.
type Report struct {
	// Workspace is the instance directory the checks ran against.
	Workspace string `json:"workspace"`
	// CorePath is the core directory inside it.
	CorePath string `json:"core_path"`
	// ConfigPath is the canonical runtime config location.
	ConfigPath string `json:"config_path"`
	// Checks are the individual requirements, in evaluation order.
	Checks []Check `json:"checks"`
}

// OK reports whether every check passed. A skipped check is not a pass:
// it means the requirement went unverified, which the boot gate must
// treat as failure even though the report shows it separately.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Status != StatusPass {
			return false
		}
	}
	return true
}

// Failures returns the checks that did not pass, preserving order.
func (r Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Status != StatusPass {
			out = append(out, c)
		}
	}
	return out
}

// keyMaterialPatterns are filename shapes that hold private key
// material. A private key inside a version-controlled root is permanent
// once committed and leaves the machine the moment that root gains a
// remote, so the check is on the filename rather than on content: by the
// time anyone inspects content, the commit already happened.
//
// Public keys are excluded, and not merely to avoid noise — core is
// supposed to carry them. The identity public key and .allowed_signers
// are committed at init precisely so the trust set travels with the
// repository, so a pattern that swept up .pub files would flag a
// correctly initialized instance and suggest ignoring the very files
// that make its history verifiable.
var keyMaterialPatterns = []string{"*.key", "*_ed25519", "*_rsa", "*.pem", "id_*"}

// publicKeySuffix marks files that look like key material by name but
// are meant to be shared.
const publicKeySuffix = ".pub"

// gitignoreKeyMaterialLines are the patterns suggested for a core
// .gitignore: the private shapes, then an explicit re-inclusion of
// public keys, which must come after to override the broader globs.
func gitignoreKeyMaterialLines() []string {
	return append(append([]string{}, keyMaterialPatterns...), "!*"+publicKeySuffix)
}

// Options configures a check run.
type Options struct {
	// ConfigFileName is the runtime config's name inside core.
	ConfigFileName string
}

// Run evaluates every core integrity requirement for a workspace and
// returns the full report. It never returns an error for a failed
// requirement — a failure is a finding, not an operational fault. An
// error is returned only when the workspace argument itself cannot be
// resolved.
func Run(ctx context.Context, workspace string, opts Options) (Report, error) {
	configName := strings.TrimSpace(opts.ConfigFileName)
	if configName == "" {
		configName = "config.yaml"
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Report{}, fmt.Errorf("resolve workspace %q: %w", workspace, err)
	}
	corePath := filepath.Join(abs, "core")
	report := Report{
		Workspace:  abs,
		CorePath:   corePath,
		ConfigPath: filepath.Join(corePath, configName),
	}

	c := checker{ctx: ctx, core: corePath, configName: configName}

	// Every check appears in every report, including the ones a failed
	// prerequisite made unrunnable. A check that vanishes is
	// indistinguishable from one that passed, both for an operator
	// reading down the list and for anything consuming the JSON.
	coreOK := c.coreDirectory(&report)
	repoOK := c.coreRepository(&report, coreOK)
	headOK := c.coreHistory(&report, repoOK)
	c.keyMaterial(&report, repoOK)
	configOK := c.configTracked(&report, headOK)
	c.coreClean(&report, headOK)
	c.configSigned(&report, configOK)

	return report, nil
}

type checker struct {
	ctx        context.Context
	core       string
	configName string
}

func (c checker) git(args ...string) (string, error) {
	cmd := exec.CommandContext(c.ctx, "git", append([]string{"-C", c.core}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%s: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c checker) coreDirectory(r *Report) bool {
	info, err := os.Stat(c.core)
	switch {
	case err == nil && info.IsDir():
		r.Checks = append(r.Checks, Check{Name: "core_directory", Status: StatusPass,
			Detail: "core exists at " + c.core})
		return true
	case errors.Is(err, os.ErrNotExist):
		r.Checks = append(r.Checks, Check{Name: "core_directory", Status: StatusFail,
			Detail: "no core directory at " + c.core,
			Fix:    "thane init " + filepath.Dir(c.core)})
	case err == nil:
		r.Checks = append(r.Checks, Check{Name: "core_directory", Status: StatusFail,
			Detail: c.core + " exists but is not a directory",
			Fix:    "move the file aside and run: thane init " + filepath.Dir(c.core)})
	default:
		r.Checks = append(r.Checks, Check{Name: "core_directory", Status: StatusFail,
			Detail: "cannot stat " + c.core + ": " + err.Error(),
			Fix:    "check ownership and permissions for the user Thane runs as"})
	}
	return false
}

func (c checker) coreRepository(r *Report, coreOK bool) bool {
	if !coreOK {
		r.Checks = append(r.Checks, Check{Name: "core_repository", Status: StatusSkipped,
			Detail: "core directory is missing"})
		return false
	}
	out, err := c.git("rev-parse", "--is-inside-work-tree")
	if err == nil && strings.TrimSpace(out) == "true" {
		r.Checks = append(r.Checks, Check{Name: "core_repository", Status: StatusPass,
			Detail: "core is a git repository"})
		return true
	}
	// A core that has a .git directory git nonetheless refuses to read is a
	// different failure with a different fix, and "git init" is actively
	// wrong advice for it: the repository exists and its history is intact.
	// The common cause is ownership — git refuses a repository owned by
	// another user — which produces an error mentioning nothing about
	// repositories being absent. Reporting that as "core is not a git
	// repository" sends an operator to re-initialize a core that is fine.
	if _, statErr := os.Stat(filepath.Join(c.core, ".git")); statErr == nil {
		detail := "core has a .git directory but git cannot read it as a repository"
		if err != nil {
			detail += ": " + err.Error()
		}
		r.Checks = append(r.Checks, Check{Name: "core_repository", Status: StatusFail,
			Detail: detail,
			Fix: "check that " + filepath.Join(c.core, ".git") + " is intact and owned by the user Thane runs as" +
				" — do not run 'git init', which would discard a history that is still there"})
		return false
	}
	r.Checks = append(r.Checks, Check{Name: "core_repository", Status: StatusFail,
		Detail: "core is not a git repository, so its contents carry no history and cannot be signed",
		Fix:    "git -C " + c.core + " init"})
	return false
}

func (c checker) coreHistory(r *Report, repoOK bool) bool {
	if !repoOK {
		r.Checks = append(r.Checks, Check{Name: "core_history", Status: StatusSkipped,
			Detail: "core is not a git repository"})
		return false
	}
	if _, err := c.git("rev-parse", "--verify", "HEAD"); err == nil {
		r.Checks = append(r.Checks, Check{Name: "core_history", Status: StatusPass,
			Detail: "core has commit history"})
		return true
	}
	// A repository with staged-but-never-committed files looks
	// version-controlled from the filesystem and has no history at all.
	r.Checks = append(r.Checks, Check{Name: "core_history", Status: StatusFail,
		Detail: "core is a git repository with no commits, so nothing in it is version-controlled yet",
		Fix:    "review what is staged (git -C " + c.core + " status), then: git -C " + c.core + " commit -S -m 'core baseline'"})
	return false
}

// keyMaterial fails when a private key is tracked, and separately when
// nothing prevents one from becoming tracked. Both are reported through
// one check because the operator's action is the same: exclude key
// material before the next commit.
func (c checker) keyMaterial(r *Report, repoOK bool) {
	if !repoOK {
		r.Checks = append(r.Checks, Check{Name: "key_material_excluded", Status: StatusSkipped,
			Detail: "core is not a git repository"})
		return
	}
	tracked, err := c.git("ls-files", "--cached", "--", "*.key", "*_ed25519", "*_rsa", "*.pem", "id_*")
	if err != nil {
		r.Checks = append(r.Checks, Check{Name: "key_material_excluded", Status: StatusFail,
			Detail: "cannot list tracked files: " + err.Error(),
			Fix:    "check that git can read " + c.core})
		return
	}
	files := privateKeyFiles(tracked)
	if len(files) > 0 {
		r.Checks = append(r.Checks, Check{Name: "key_material_excluded", Status: StatusFail,
			Detail: "private key material is tracked in core: " + strings.Join(files, ", ") +
				" — committing it puts the key in history permanently, and it leaves the machine if core ever gains a remote",
			Fix: "git -C " + c.core + " rm --cached " + strings.Join(files, " ") +
				" && printf '%s\\n' " + strings.Join(quoteAll(gitignoreKeyMaterialLines()), " ") + " >> " + filepath.Join(c.core, ".gitignore")})
		return
	}
	if _, err := os.Stat(filepath.Join(c.core, ".gitignore")); errors.Is(err, os.ErrNotExist) {
		r.Checks = append(r.Checks, Check{Name: "key_material_excluded", Status: StatusFail,
			Detail: "core has no .gitignore, so nothing stops key material from being committed by a later 'git add'",
			Fix: "printf '%s\\n' " + strings.Join(quoteAll(gitignoreKeyMaterialLines()), " ") + " >> " +
				filepath.Join(c.core, ".gitignore")})
		return
	}
	r.Checks = append(r.Checks, Check{Name: "key_material_excluded", Status: StatusPass,
		Detail: "no private key material is tracked"})
}

func (c checker) configTracked(r *Report, headOK bool) bool {
	if !headOK {
		r.Checks = append(r.Checks, Check{Name: "config_committed", Status: StatusSkipped,
			Detail: "core has no commit history"})
		return false
	}
	if _, err := c.git("cat-file", "-e", "HEAD:"+c.configName); err != nil {
		r.Checks = append(r.Checks, Check{Name: "config_committed", Status: StatusFail,
			Detail: c.configName + " is not committed in core, so the running config has no change history",
			Fix:    "git -C " + c.core + " add " + c.configName + " && git -C " + c.core + " commit -S -m 'adopt runtime config into core'"})
		return false
	}
	r.Checks = append(r.Checks, Check{Name: "config_committed", Status: StatusPass,
		Detail: c.configName + " is committed in core"})
	return true
}

func (c checker) coreClean(r *Report, headOK bool) {
	if !headOK {
		r.Checks = append(r.Checks, Check{Name: "core_clean", Status: StatusSkipped,
			Detail: "core has no commit history"})
		return
	}
	out, err := c.git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		r.Checks = append(r.Checks, Check{Name: "core_clean", Status: StatusFail,
			Detail: "cannot read core status: " + err.Error(),
			Fix:    "check that git can read " + c.core})
		return
	}
	if out != "" {
		r.Checks = append(r.Checks, Check{Name: "core_clean", Status: StatusFail,
			Detail: "core has uncommitted changes to tracked files, so what is running differs from what is signed:\n" + indent(out),
			Fix:    "review with: git -C " + c.core + " diff, then commit them: git -C " + c.core + " commit -aS -m 'core update'"})
		return
	}
	r.Checks = append(r.Checks, Check{Name: "core_clean", Status: StatusPass,
		Detail: "no uncommitted changes to tracked files"})
}

// configSigned verifies that the running config is covered by a commit
// signed by a key the instance trusts. This is the check the others
// exist to make meaningful: history without signatures records what
// changed but not who was entitled to change it.
//
// The failure mode is determined here rather than inferred from the
// verifier's message, because the two common failures need different
// commands — an uncommitted edit needs staging before it can be signed,
// while a committed-but-unsigned config needs the existing commit
// amended. Telling an operator to amend a commit that does not yet
// contain their change would waste the one attempt they read carefully.
func (c checker) configSigned(r *Report, configOK bool) {
	if !configOK {
		r.Checks = append(r.Checks, Check{Name: "config_signed", Status: StatusSkipped,
			Detail: "config is not committed in core"})
		return
	}

	if status, err := c.git("status", "--porcelain", "--", c.configName); err == nil && strings.TrimSpace(status) != "" {
		r.Checks = append(r.Checks, Check{Name: "config_signed", Status: StatusFail,
			Detail: c.configName + " has uncommitted changes, so the running config is not the one any signature covers",
			Fix:    "git -C " + c.core + " add " + c.configName + " && git -C " + c.core + " commit -S -m 'update runtime config'"})
		return
	}

	verifier, err := provenance.NewVerifier(c.core, slog.New(slog.DiscardHandler), provenance.Options{})
	if err != nil {
		r.Checks = append(r.Checks, Check{Name: "config_signed", Status: StatusFail,
			Detail: "cannot resolve the trusted signer set: " + err.Error(),
			Fix:    "ensure " + filepath.Join(c.core, ".allowed_signers") + " exists and lists the keys entitled to sign this instance"})
		return
	}

	// VerifyFile reports a failure through both the result and a non-nil
	// error carrying the same message, so the result is the authority
	// and the error is not a separate case.
	result, _ := verifier.VerifyFile(c.ctx, c.configName)
	if result.Trusted() {
		r.Checks = append(r.Checks, Check{Name: "config_signed", Status: StatusPass,
			Detail: c.configName + " is covered by a trusted signature"})
		return
	}

	detail := c.configName + " is not covered by a commit signed by a trusted key"
	if result.Message != "" {
		detail += ": " + result.Message
	}
	r.Checks = append(r.Checks, Check{Name: "config_signed", Status: StatusFail,
		Detail: detail,
		Fix: "re-sign the commit that carries it with a key listed in " + filepath.Join(c.core, ".allowed_signers") +
			": git -C " + c.core + " commit -S --amend --no-edit"})
}

// privateKeyFiles filters a git ls-files result down to the entries that
// are actually private key material, dropping public keys that matched
// the same name shapes.
func privateKeyFiles(lsFiles string) []string {
	lsFiles = strings.TrimSpace(lsFiles)
	if lsFiles == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(lsFiles, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || strings.HasSuffix(name, publicKeySuffix) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "'" + v + "'"
	}
	return out
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "      " + line
	}
	return strings.Join(lines, "\n")
}
